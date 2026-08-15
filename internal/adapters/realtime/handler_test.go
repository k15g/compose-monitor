package realtime

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// stubSource is a ContainerSource with a fixed answer.
type stubSource struct {
	services []domain.Service
	err      error
	usage    domain.ResourceUsage
}

var _ ports.ContainerSource = (*stubSource)(nil)

func (s *stubSource) List(context.Context) ([]domain.Service, error) {
	return s.services, s.err
}

func (s *stubSource) Inspect(_ context.Context, containerID string) (domain.ServiceDetail, error) {
	for _, service := range s.services {
		if service.ContainerID == containerID {
			return domain.ServiceDetail{Service: service}, nil
		}
	}
	if s.err != nil {
		return domain.ServiceDetail{}, s.err
	}
	return domain.ServiceDetail{}, ports.ErrNotFound
}

func (s *stubSource) Logs(_ context.Context, containerID string, tail int) (domain.Logs, error) {
	if s.err != nil {
		return domain.Logs{}, s.err
	}
	return domain.Logs{
		Lines: []domain.LogLine{{Stream: domain.LogStreamStdout, Text: "hello from " + containerID}},
		Tail:  tail,
	}, nil
}

func (s *stubSource) Usage(context.Context) (domain.ResourceUsage, error) {
	if s.err != nil {
		return domain.ResourceUsage{}, s.err
	}
	return s.usage, nil
}

func (s *stubSource) Watch(ctx context.Context) <-chan struct{} {
	changes := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(changes)
	}()
	return changes
}

// frame is one parsed SSE message.
type frame struct {
	name string
	data string
}

// stream opens the endpoint and reads frames off it in the background, so a
// test can wait on the next frame with a timeout rather than blocking forever
// on a stream that never produces one.
func stream(t *testing.T, url string) <-chan frame {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	require.Equal(t, "no-cache", response.Header.Get("Cache-Control"))

	frames := make(chan frame, 8)
	go func() {
		defer close(frames)

		reader := bufio.NewReader(response.Body)
		var current frame
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")

			switch {
			case strings.HasPrefix(line, "event: "):
				current.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				current.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				// A blank line ends a message. A keep-alive comment produces
				// one with nothing in it, which is not a frame.
				if current.name != "" {
					frames <- current
					current = frame{}
				}
			}
		}
	}()

	return frames
}

func next(t *testing.T, frames <-chan frame) frame {
	t.Helper()
	select {
	case f, open := <-frames:
		require.True(t, open, "stream closed before the expected frame arrived")
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return frame{}
	}
}

func newStreamServer(t *testing.T, source ports.ContainerSource) (*httptest.Server, *Hub) {
	t.Helper()

	ctx := config.WithConfig(t.Context(), &config.Config{
		Project: config.ProjectConfig{Name: "example", Interval: time.Hour, Debounce: time.Millisecond},
		Http:    config.HttpConfig{KeepAlive: time.Hour},
		Control: config.ControlConfig{Enabled: true},
	})

	hub := NewHub()
	t.Cleanup(func() { _ = hub.Close() })

	renderer := NewRenderer(ctx)
	monitor := app.NewMonitorService(ctx, source, nil, hub)

	mux := http.NewServeMux()
	NewAdapter(ctx, hub, renderer, monitor).Mount(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, hub
}

func TestStreamOpensWithASnapshot(t *testing.T) {
	source := &stubSource{services: []domain.Service{
		{ContainerID: "c1", Name: "web", Number: 1, State: domain.StateRunning, Status: "Up 1 minute"},
		{ContainerID: "c2", Name: "db", Number: 1, State: domain.StateExited, Status: "Exited (0) 1 hour ago"},
	}}
	server, _ := newStreamServer(t, source)

	frames := stream(t, server.URL+"/events")

	first := next(t, frames)
	require.Equal(t, "sync", first.name, "every connection begins with the full set of rows")

	var payload syncPayload
	require.NoError(t, json.Unmarshal([]byte(first.data), &payload))
	require.Len(t, payload.Services, 2)
	assert.Equal(t, []string{"c2", "c1"}, []string{payload.Services[0].ID, payload.Services[1].ID},
		"the snapshot arrives in display order — db before web — not in the order the runtime listed them")
	assert.Contains(t, payload.Services[0].HTML, `data-service-id="c2"`)
	assert.Contains(t, payload.Services[1].HTML, `data-service-id="c1"`)
}

func TestStreamDeliversChanges(t *testing.T) {
	server, hub := newStreamServer(t, &stubSource{})

	frames := stream(t, server.URL+"/events")
	require.Equal(t, "sync", next(t, frames).name)

	require.NoError(t, hub.Publish(t.Context(), domain.Event{
		Action:  domain.ActionUpdated,
		Service: domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
	}))

	changed := next(t, frames)
	require.Equal(t, "change", changed.name)

	var payload changePayload
	require.NoError(t, json.Unmarshal([]byte(changed.data), &payload))
	assert.Equal(t, domain.ActionUpdated, payload.Action)
	assert.Equal(t, "c1", payload.ID)
	assert.Contains(t, payload.HTML, `data-service-id="c1"`,
		"the connection renders the event on its way out")
}

func TestStreamFrameIsASingleLine(t *testing.T) {
	server, hub := newStreamServer(t, &stubSource{})

	frames := stream(t, server.URL+"/events")
	require.Equal(t, "sync", next(t, frames).name)

	// A raw newline in an SSE data field ends the field, so a multi-line row
	// would arrive truncated. JSON encoding is what prevents it, and this is
	// the test that would catch its removal.
	require.NoError(t, hub.Publish(t.Context(), domain.Event{
		Action:  domain.ActionAdded,
		Service: domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
	}))

	changed := next(t, frames)
	assert.NotContains(t, changed.data, "\n")

	var payload changePayload
	require.NoError(t, json.Unmarshal([]byte(changed.data), &payload))
	assert.Contains(t, payload.HTML, `data-service-id="c1"`)
}

func TestStreamRemovalCarriesNoHTML(t *testing.T) {
	server, hub := newStreamServer(t, &stubSource{})

	frames := stream(t, server.URL+"/events")
	require.Equal(t, "sync", next(t, frames).name)

	require.NoError(t, hub.Publish(t.Context(), domain.Event{
		Action:  domain.ActionRemoved,
		Service: domain.Service{ContainerID: "c1", Name: "web"},
	}))

	var payload changePayload
	require.NoError(t, json.Unmarshal([]byte(next(t, frames).data), &payload))
	assert.Equal(t, domain.ActionRemoved, payload.Action)
	assert.Empty(t, payload.HTML, "a removal takes the row away, so there is nothing to draw")
}

func TestStreamRefusesWhenTheRuntimeIsUnreachable(t *testing.T) {
	source := &stubSource{err: ports.ErrSourceUnavailable}
	server, _ := newStreamServer(t, source)

	response, err := http.Get(server.URL + "/events")
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode,
		"a stream that cannot be filled must fail the request rather than hang open")
}

func TestChangesSayWhetherTheyAreWorthLookingAt(t *testing.T) {
	server, hub := newStreamServer(t, &stubSource{})

	frames := stream(t, server.URL+"/events")
	require.Equal(t, "sync", next(t, frames).name)

	// Something happened.
	require.NoError(t, hub.Publish(t.Context(), domain.Event{
		Action:  domain.ActionUpdated,
		Service: domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
		Notable: true,
	}))

	var notable changePayload
	require.NoError(t, json.Unmarshal([]byte(next(t, frames).data), &notable))
	assert.True(t, notable.Notable)

	// Only the elapsed time in the status moved on. The row is still sent, so
	// the page stops saying "Up 1 minute" an hour later — but the client is
	// told not to draw the eye to it.
	require.NoError(t, hub.Publish(t.Context(), domain.Event{
		Action:  domain.ActionUpdated,
		Service: domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
	}))

	var quiet changePayload
	require.NoError(t, json.Unmarshal([]byte(next(t, frames).data), &quiet))
	assert.False(t, quiet.Notable)
	assert.NotEmpty(t, quiet.HTML, "the row is redrawn either way")
}

func TestTheStreamRendersWhatTheSubscriberAskedFor(t *testing.T) {
	source := &stubSource{services: []domain.Service{
		{ContainerID: "c1", Name: "web", Number: 1, State: domain.StateRunning, Status: "Up 1 minute"},
	}}
	server, _ := newStreamServer(t, source)

	// Both pages draw a listing; the state is what differs. Which one a
	// subscriber gets is the only thing the view decides, and it is why
	// rendering happens on the way out rather than where the event is
	// produced.
	services := snapshot(t, server.URL+"/events?view=services")
	assert.Contains(t, services, "badge-running", "the services page leads with the state")

	overview := snapshot(t, server.URL+"/events?view=overview")
	assert.NotContains(t, overview, "badge-running",
		"everything on the front page is running, so saying so on each line says nothing")
	assert.Contains(t, overview, "badge-uptime", "and both carry the rest")
}

// snapshot opens a stream and returns the markup of the first row of its sync
// frame. The frame is JSON, so the markup has to be decoded before it can be
// recognised as markup at all.
func snapshot(t *testing.T, url string) string {
	t.Helper()

	frame := next(t, stream(t, url))
	require.Equal(t, "sync", frame.name)

	var payload syncPayload
	require.NoError(t, json.Unmarshal([]byte(frame.data), &payload))
	require.NotEmpty(t, payload.Services)
	return payload.Services[0].HTML
}

func TestAnUnknownViewLeavesNothingOut(t *testing.T) {
	assert.Equal(t, ViewServices, ParseView(""))
	assert.Equal(t, ViewServices, ParseView("nonsense"))
	assert.Equal(t, ViewServices, ParseView("services"))
	assert.Equal(t, ViewOverview, ParseView("overview"))
}
