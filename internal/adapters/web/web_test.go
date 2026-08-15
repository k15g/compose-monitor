package web_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/adapters/web"
	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

type stubSource struct {
	services []domain.Service
	// details overrides what Inspect returns, for tests that need more of a
	// container than a row carries.
	details map[string]domain.ServiceDetail
	err     error
	logsErr error
	usage   domain.ResourceUsage
}

var _ ports.ContainerSource = (*stubSource)(nil)

func (s *stubSource) List(context.Context) ([]domain.Service, error) { return s.services, s.err }

func (s *stubSource) Inspect(_ context.Context, containerID string) (domain.ServiceDetail, error) {
	if detail, ok := s.details[containerID]; ok {
		return detail, nil
	}
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
	if s.logsErr != nil {
		return domain.Logs{}, s.logsErr
	}
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

func (s *stubSource) Watch(context.Context) <-chan struct{} { return nil }

// stubControl records the stops it is asked for.
type stubControl struct {
	stopped []string
	started []string
	removed []string
	err     error
}

var _ ports.ContainerControl = (*stubControl)(nil)

func (s *stubControl) Start(_ context.Context, containerID string) error {
	if s.err != nil {
		return s.err
	}
	s.started = append(s.started, containerID)
	return nil
}

func (s *stubControl) Remove(_ context.Context, containerID string) error {
	if s.err != nil {
		return s.err
	}
	s.removed = append(s.removed, containerID)
	return nil
}

func (s *stubControl) Stop(_ context.Context, containerID string) error {
	if s.err != nil {
		return s.err
	}
	s.stopped = append(s.stopped, containerID)
	return nil
}

// stubNetworks and stubVolumes stand in for the other two sections, so the
// server under test carries the same routes the real one does.
type stubNetworks struct {
	networks []domain.Network
	err      error
	removed  []string
}

func (s *stubNetworks) Remove(_ context.Context, id string) error {
	if s.err != nil {
		return s.err
	}
	s.removed = append(s.removed, id)
	return nil
}

var _ ports.NetworkSource = (*stubNetworks)(nil)

func (s *stubNetworks) List(context.Context) ([]domain.Network, error) { return s.networks, s.err }

func (s *stubNetworks) Inspect(_ context.Context, id string) (domain.Network, error) {
	for _, found := range s.networks {
		if found.ID == id {
			return found, nil
		}
	}
	if s.err != nil {
		return domain.Network{}, s.err
	}
	return domain.Network{}, ports.ErrNotFound
}

type stubVolumes struct {
	volumes []domain.Volume
	err     error
	removed []string
}

func (s *stubVolumes) Remove(_ context.Context, name string) error {
	if s.err != nil {
		return s.err
	}
	s.removed = append(s.removed, name)
	return nil
}

var _ ports.VolumeSource = (*stubVolumes)(nil)

func (s *stubVolumes) List(context.Context) ([]domain.Volume, error) { return s.volumes, s.err }

func (s *stubVolumes) Inspect(_ context.Context, name string) (domain.Volume, error) {
	for _, found := range s.volumes {
		if found.Name == name {
			return found, nil
		}
	}
	if s.err != nil {
		return domain.Volume{}, s.err
	}
	return domain.Volume{}, ports.ErrNotFound
}

type nopBroadcaster struct{}

func (nopBroadcaster) Publish(context.Context, domain.Event) error { return nil }

func (nopBroadcaster) Subscribe(context.Context) (<-chan domain.Event, func()) {
	return nil, func() {}
}

func newServer(t *testing.T, source ports.ContainerSource) *httptest.Server {
	t.Helper()
	return newServerWithControl(t, source, nil)
}

func newServerWithControl(t *testing.T, source ports.ContainerSource, control ports.ContainerControl) *httptest.Server {
	t.Helper()
	return newFullServer(t, source, control, &stubNetworks{}, &stubVolumes{})
}

// networkControl and volumeControl turn removal on for a test exactly when
// container control is on, which is how the real wiring does it.
func networkControl(source ports.NetworkSource, control ports.ContainerControl) ports.NetworkControl {
	if control == nil {
		return nil
	}
	return source.(*stubNetworks)
}

func volumeControl(source ports.VolumeSource, control ports.ContainerControl) ports.VolumeControl {
	if control == nil {
		return nil
	}
	return source.(*stubVolumes)
}

func newFullServer(
	t *testing.T,
	source ports.ContainerSource,
	control ports.ContainerControl,
	networks ports.NetworkSource,
	volumes ports.VolumeSource,
) *httptest.Server {
	t.Helper()

	ctx := config.WithConfig(t.Context(), &config.Config{
		Project: config.ProjectConfig{
			Name:     "example",
			Title:    "Example project",
			Interval: time.Hour,
			Debounce: time.Millisecond,
		},
		Control: config.ControlConfig{Enabled: control != nil},
	})

	monitor := app.NewMonitorService(ctx, source, control, nopBroadcaster{})
	adapter, err := web.New(ctx, monitor,
		app.NewNetworkService(ctx, networks, source, networkControl(networks, control)),
		app.NewVolumeService(ctx, volumes, source, volumeControl(volumes, control)))
	require.NoError(t, err)

	mux := http.NewServeMux()
	adapter.Mount(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	return getWith(t, url, nil)
}

func getWith(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, string(body)
}

func TestPageListsOnlineAndOfflineServices(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{
		{ContainerID: "c1", Name: "web", Number: 1, Image: "nginx:1", State: domain.StateRunning, Status: "Up 1 minute"},
		{ContainerID: "c2", Name: "db", Number: 1, Image: "postgres:18", State: domain.StateExited, Status: "Exited (0) 1 hour ago"},
	}})

	response, body := get(t, server.URL+"/services")

	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, body, "Example project", "the page is titled by PROJECT_TITLE when one is set")
	assert.Contains(t, body, `data-service-id="c1"`)
	assert.Contains(t, body, `data-service-id="c2"`, "a stopped service is listed, not omitted")
	assert.Contains(t, body, `data-online="true"`)
	assert.Contains(t, body, `data-online="false"`)
	assert.Contains(t, body, `<span class="count" id="count-online">1</span>`)
	assert.Contains(t, body, `<span class="count" id="count-offline">1</span>`)
}

func TestPageEscapesWhatTheRuntimeReports(t *testing.T) {
	// Image references and container names come from the daemon, and a
	// container can be created with whatever name its author liked.
	server := newServer(t, &stubSource{services: []domain.Service{
		{ContainerID: "c1", Name: "<script>alert(1)</script>", Number: 1, State: domain.StateRunning},
	}})

	_, body := get(t, server.URL+"/services")

	assert.NotContains(t, body, "<script>alert(1)</script>")
	assert.Contains(t, body, "&lt;script&gt;")
}

func TestPageReportsAnUnreachableRuntime(t *testing.T) {
	server := newServer(t, &stubSource{err: ports.ErrSourceUnavailable})

	response, body := get(t, server.URL+"/services")

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "The Docker socket could not be read")
}

func TestStaticAssetsAreServed(t *testing.T) {
	server := newServer(t, &stubSource{})

	for path, contentType := range map[string]string{
		"/static/css/app.css": "text/css",
		"/static/js/app.js":   "javascript",
		"/favicon.svg":        "image/svg+xml",
	} {
		t.Run(path, func(t *testing.T) {
			response, body := get(t, server.URL+path)
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Contains(t, response.Header.Get("Content-Type"), contentType)
			assert.NotEmpty(t, body)
		})
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	server := newServer(t, &stubSource{})

	// The page is mounted at "/{$}" rather than "/", so an unknown path is a
	// 404 instead of the services page.
	response, _ := get(t, server.URL+"/nope")

	assert.Equal(t, http.StatusNotFound, response.StatusCode)
}

// --- stopping ---------------------------------------------------------------

func running(id, name string) domain.Service {
	return domain.Service{ContainerID: id, Name: name, Number: 1, State: domain.StateRunning, Status: "Up 1 minute"}
}

func post(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()

	request, err := http.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, err)
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	// Do not follow the redirect a plain form post gets, so the test can see
	// the status the handler actually chose.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	response, err := client.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, string(body)
}

func TestStopButtonIsDrawnOnlyForRunningServices(t *testing.T) {
	server := newServerWithControl(t, &stubSource{services: []domain.Service{
		running("c1", "web"),
		{ContainerID: "c2", Name: "db", Number: 1, State: domain.StateExited},
	}}, &stubControl{})

	_, body := get(t, server.URL+"/services")

	assert.Contains(t, body, `action="/services/c1/stop"`)
	assert.NotContains(t, body, `action="/services/c2/stop"`, "a stopped service has nothing to stop")
}

func TestStopButtonIsAbsentWhenControlIsDisabled(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	_, body := get(t, server.URL+"/services")

	assert.NotContains(t, body, "stop-form",
		"no button is drawn where the endpoint behind it would refuse")
}

func TestStopStopsTheService(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	response, _ := post(t, server.URL+"/services/c1/stop", nil)

	assert.Equal(t, http.StatusSeeOther, response.StatusCode, "a plain form post is redirected, so it works without JavaScript")
	assert.Equal(t, "/", response.Header.Get("Location"))
	assert.Equal(t, []string{"c1"}, control.stopped)
}

func TestStopFromTheScriptGetsNoContent(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	response, _ := post(t, server.URL+"/services/c1/stop", map[string]string{"X-Requested-With": "fetch"})

	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	assert.Equal(t, []string{"c1"}, control.stopped)
}

func TestStopRefusesAContainerOutsideTheProject(t *testing.T) {
	control := &stubControl{}
	// The source is project-scoped, so a container that is not in it is simply
	// not in the list — which is exactly how an id from the request naming
	// someone else's container gets refused.
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	response, _ := post(t, server.URL+"/services/somebody-elses-container/stop", nil)

	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	assert.Empty(t, control.stopped, "the runtime is never asked")
}

func TestStopRefusesAServiceThatIsNotRunning(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{
		{ContainerID: "c2", Name: "db", Number: 1, State: domain.StateExited},
	}}, control)

	response, _ := post(t, server.URL+"/services/c2/stop", nil)

	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Empty(t, control.stopped)
}

func TestStopIsRefusedWhenControlIsDisabled(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	response, _ := post(t, server.URL+"/services/c1/stop", nil)

	assert.Equal(t, http.StatusForbidden, response.StatusCode,
		"the endpoint refuses even though no button offered it")
}

func TestStopRefusesACrossSiteRequest(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	// What a form on another site posting here looks like. The page has no
	// authentication, so this header is what stops it working.
	response, _ := post(t, server.URL+"/services/c1/stop", map[string]string{"Sec-Fetch-Site": "cross-site"})

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Empty(t, control.stopped)
}

func TestHealthzStaysHealthyWhenTheRuntimeIsUnreachable(t *testing.T) {
	server := newServer(t, &stubSource{err: ports.ErrSourceUnavailable})

	response, body := get(t, server.URL+"/healthz")

	// An unreachable runtime is a state the service recovers from by itself.
	// Failing the probe would have an orchestrator restart a working process.
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, body, `"status":"ok"`)
	assert.Contains(t, body, `"runtime":"unreachable"`)
}

func TestHealthzReportsAReachableRuntime(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	response, body := get(t, server.URL+"/healthz")

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, body, `"runtime":"reachable"`)
}

// --- detail pages -----------------------------------------------------------

func TestServiceDetailShowsEverythingAboutTheContainer(t *testing.T) {
	server := newServer(t, &stubSource{
		services: []domain.Service{running("c1", "web")},
		details: map[string]domain.ServiceDetail{
			"c1": {
				Service:     domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
				Environment: []domain.Label{{Key: "POSTGRES_DB", Value: "example"}},
			},
		},
	})

	response, body := get(t, server.URL+"/services/c1")

	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, body, "Lifecycle")
	assert.Contains(t, body, "Environment")
}

func TestServiceDetailIsNotFoundOutsideTheProject(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	response, body := get(t, server.URL+"/services/someone-elses-container")

	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	assert.Contains(t, body, "No such service in this project")
	assert.Contains(t, body, "belong to another project",
		"the page does not confirm whether it exists elsewhere")
}

func TestServiceRowLinksToItsDetailPage(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	_, body := get(t, server.URL+"/services")

	assert.Contains(t, body, `href="/services/c1"`)
}

func TestNetworksListAndDetail(t *testing.T) {
	networks := &stubNetworks{networks: []domain.Network{{
		ID:      "n1abcdef01234567",
		Name:    "example_default",
		Driver:  "bridge",
		Scope:   "local",
		Subnets: []domain.Subnet{{Range: "172.20.0.0/16", Gateway: "172.20.0.1"}},
		Members: []domain.NetworkMember{{ContainerID: "c1", Name: "example-web-1", IPv4Address: "172.20.0.2/16"}},
		Labels:  []domain.Label{{Key: "com.docker.compose.project", Value: "example"}},
	}}}
	server := newFullServer(t, &stubSource{}, nil, networks, &stubVolumes{})

	response, list := get(t, server.URL+"/networks")
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, list, "example_default")
	assert.Contains(t, list, `href="/networks/n1abcdef01234567"`)

	response, detail := get(t, server.URL+"/networks/n1abcdef01234567")
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, detail, "172.20.0.0/16")
	assert.Contains(t, detail, "Connected containers")
	assert.Contains(t, detail, `href="/services/c1"`, "a member links through to the service")
}

func TestNetworkDetailIsNotFoundOutsideTheProject(t *testing.T) {
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, &stubVolumes{})

	response, body := get(t, server.URL+"/networks/somebody-elses")

	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	assert.Contains(t, body, "No such network in this project")
}

func TestVolumesListAndDetail(t *testing.T) {
	volumes := &stubVolumes{volumes: []domain.Volume{{
		Name:       "example_pgdata",
		Driver:     "local",
		Mountpoint: "/var/lib/docker/volumes/example_pgdata/_data",
		Scope:      "local",
		Size:       -1,
		Labels:     []domain.Label{{Key: "com.docker.compose.project", Value: "example"}},
	}}}
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, volumes)

	response, list := get(t, server.URL+"/volumes")
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, list, "example_pgdata")
	assert.Contains(t, list, `href="/volumes/example_pgdata"`)

	response, detail := get(t, server.URL+"/volumes/example_pgdata")
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, detail, "/var/lib/docker/volumes/example_pgdata/_data")
	assert.NotContains(t, detail, "<dt>Size</dt>",
		"the size is only shown when the daemon was asked to measure it, and this page never asks")
}

func TestVolumeDetailIsNotFoundOutsideTheProject(t *testing.T) {
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, &stubVolumes{})

	response, body := get(t, server.URL+"/volumes/somebody-elses")

	assert.Equal(t, http.StatusNotFound, response.StatusCode)
	assert.Contains(t, body, "No such volume in this project")
}

func TestEverySectionIsReachableFromEveryPage(t *testing.T) {
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, &stubVolumes{})

	for _, path := range []string{"/", "/services", "/networks", "/volumes"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)
			assert.Contains(t, body, `href="/networks"`)
			assert.Contains(t, body, `href="/volumes"`)
			assert.Contains(t, body, `class="tab active"`, "the current section is marked")
		})
	}
	// With no lists of their own on the front page, the tabs are the only way
	// to reach networks and volumes.
}

func TestNetworksReportAnUnreachableRuntime(t *testing.T) {
	server := newFullServer(t, &stubSource{}, nil,
		&stubNetworks{err: ports.ErrSourceUnavailable}, &stubVolumes{})

	response, body := get(t, server.URL+"/networks")

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "The Docker socket could not be read")
}

// --- start, and logs --------------------------------------------------------

func stopped(id, name string) domain.Service {
	return domain.Service{ContainerID: id, Name: name, Number: 1, State: domain.StateExited, Status: "Exited (0) 1 hour ago"}
}

func TestTheButtonMatchesTheState(t *testing.T) {
	server := newServerWithControl(t, &stubSource{services: []domain.Service{
		running("c1", "web"),
		stopped("c2", "db"),
	}}, &stubControl{})

	_, body := get(t, server.URL+"/services")

	assert.Contains(t, body, `action="/services/c1/stop"`, "a running service can be stopped")
	assert.Contains(t, body, `action="/services/c2/start"`, "a stopped service can be started")
	assert.NotContains(t, body, `action="/services/c1/start"`)
	assert.NotContains(t, body, `action="/services/c2/stop"`)
}

func TestStartStartsTheService(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{stopped("c2", "db")}}, control)

	response, _ := post(t, server.URL+"/services/c2/start", map[string]string{"X-Requested-With": "fetch"})

	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	assert.Equal(t, []string{"c2"}, control.started)
}

func TestStartRefusesAServiceAlreadyRunning(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	response, _ := post(t, server.URL+"/services/c1/start", nil)

	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Empty(t, control.started)
}

func TestStartIsRefusedCrossSite(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{stopped("c2", "db")}}, control)

	response, _ := post(t, server.URL+"/services/c2/start", map[string]string{"Sec-Fetch-Site": "cross-site"})

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Empty(t, control.started)
}

func TestActionReturnsToThePageTheFormWasOn(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	response, _ := post(t, server.URL+"/services/c1/stop",
		map[string]string{"Referer": server.URL + "/services/c1?tail=50"})

	assert.Equal(t, http.StatusSeeOther, response.StatusCode)
	assert.Equal(t, "/services/c1?tail=50", response.Header.Get("Location"),
		"acting from a detail page stays on it")
}

func TestActionWillNotRedirectOffSite(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	// Only the path of the referrer is used. Taking the header whole would
	// turn this endpoint into an open redirect.
	response, _ := post(t, server.URL+"/services/c1/stop",
		map[string]string{"Referer": "https://example.invalid/phishing"})

	assert.Equal(t, "/phishing", response.Header.Get("Location"))
	assert.NotContains(t, response.Header.Get("Location"), "example.invalid")
}

func TestServiceDetailDefersTheLog(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	response, body := get(t, server.URL+"/services/c1")

	require.Equal(t, http.StatusOK, response.StatusCode)

	// Reading a log costs a request to the daemon and can be large, and most
	// visits to a detail page are not about it — so the panel carries the URL
	// to fill itself from rather than the content.
	assert.Contains(t, body, `data-log-url="/services/c1/log"`)
	assert.NotContains(t, body, "hello from c1")
}

func TestLogEndpointServesAFragmentToTheScriptAndAPageToABrowser(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{running("c1", "web")}})

	_, fragment := getWith(t, server.URL+"/services/c1/log", map[string]string{"HX-Request": "true"})
	assert.Contains(t, fragment, "hello from c1")
	assert.NotContains(t, fragment, "<html", "a fragment is swapped into a page that already exists")

	// The panel has a no-script link straight to this URL, so it has to stand
	// on its own too.
	_, page := get(t, server.URL+"/services/c1/log")
	assert.Contains(t, page, "hello from c1")
	assert.Contains(t, page, "<html")
}

func TestLogSizeCanBeChosen(t *testing.T) {
	source := &stubSource{services: []domain.Service{running("c1", "web")}}
	server := newServer(t, source)

	_, body := getWith(t, server.URL+"/services/c1/log?tail=1000", map[string]string{"HX-Request": "true"})

	assert.Contains(t, body, `data-log-tail="1000"`)
	assert.Contains(t, body, `class="log-size active"`)
}

func TestLogPanelSaysSoWhenTheLogCannotBeRead(t *testing.T) {
	// A container that has never run has no log. The panel says so; the
	// request the panel made still succeeds, because failing it would leave
	// the panel showing nothing at all.
	source := &stubSource{services: []domain.Service{running("c1", "web")}, logsErr: errors.New("no such log")}
	server := newServer(t, source)

	detail, page := get(t, server.URL+"/services/c1")
	assert.Equal(t, http.StatusOK, detail.StatusCode)
	assert.Contains(t, page, "Lifecycle", "the detail page never reads the log, so it cannot be hurt by it")

	response, body := getWith(t, server.URL+"/services/c1/log", map[string]string{"HX-Request": "true"})
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, body, "The log could not be read")
}

// --- removing ---------------------------------------------------------------

func TestRemoveButtonIsDrawnOnlyForStoppedServices(t *testing.T) {
	server := newServerWithControl(t, &stubSource{services: []domain.Service{
		running("c1", "web"),
		stopped("c2", "db"),
	}}, &stubControl{})

	_, body := get(t, server.URL+"/services")

	assert.Contains(t, body, `href="/services/c2/remove"`)
	assert.NotContains(t, body, `href="/services/c1/remove"`,
		"a running container cannot be removed, so it is not offered")
}

func TestRemoveAsksBeforeActing(t *testing.T) {
	server := newServerWithControl(t, &stubSource{services: []domain.Service{stopped("c2", "db")}}, &stubControl{})

	// The control is a link to a server-rendered question, enhanced into a
	// modal — so the confirmation is never text the client was holding.
	_, list := get(t, server.URL+"/services")
	assert.Contains(t, list, `hx-target="#confirm-body"`)

	_, fragment := getWith(t, server.URL+"/services/c2/remove", map[string]string{"HX-Request": "true"})
	assert.Contains(t, fragment, "Remove container")
	assert.Contains(t, fragment, "volumes are left alone")
	assert.Contains(t, fragment, `action="/services/c2/remove"`, "the dialog carries the real form")
	assert.NotContains(t, fragment, "<html", "the fragment goes inside the dialog")

	// Without the script the same link navigates to the same question.
	_, page := get(t, server.URL+"/services/c2/remove")
	assert.Contains(t, page, "<html")
	assert.Contains(t, page, "Remove container")
}

func TestRemoveConfirmationSaysWhenItWouldBeRefused(t *testing.T) {
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, &stubControl{})

	_, body := getWith(t, server.URL+"/services/c1/remove", map[string]string{"HX-Request": "true"})

	assert.Contains(t, body, "still running")
}

func TestRemoveRemovesTheService(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{stopped("c2", "db")}}, control)

	response, _ := post(t, server.URL+"/services/c2/remove", map[string]string{"X-Requested-With": "fetch"})

	assert.Equal(t, http.StatusNoContent, response.StatusCode)
	assert.Equal(t, []string{"c2"}, control.removed)
}

func TestRemoveRefusesARunningService(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{running("c1", "web")}}, control)

	response, body := post(t, server.URL+"/services/c1/remove", nil)

	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Contains(t, body, "stop the service before removing it")
	assert.Empty(t, control.removed)
}

func TestRemoveIsRefusedCrossSite(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{stopped("c2", "db")}}, control)

	response, _ := post(t, server.URL+"/services/c2/remove", map[string]string{"Sec-Fetch-Site": "cross-site"})

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Empty(t, control.removed)
}

func TestRemoveIsRefusedWhenControlIsDisabled(t *testing.T) {
	server := newServer(t, &stubSource{services: []domain.Service{stopped("c2", "db")}})

	response, _ := post(t, server.URL+"/services/c2/remove", nil)

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
}

func TestRemoveReturnsToTheListNotTheDetailPage(t *testing.T) {
	control := &stubControl{}
	server := newServerWithControl(t, &stubSource{services: []domain.Service{stopped("c2", "db")}}, control)

	// The detail page the form was on describes a container that no longer
	// exists, so going back to it would be a 404.
	response, _ := post(t, server.URL+"/services/c2/remove",
		map[string]string{"Referer": server.URL + "/services/c2"})

	assert.Equal(t, http.StatusSeeOther, response.StatusCode)
	assert.Equal(t, "/", response.Header.Get("Location"))
}

// --- how the detail page reads in each state --------------------------------

func TestDetailDoesNotCallAStoppedContainerHealthy(t *testing.T) {
	// The runtime keeps the last healthcheck result after a container stops,
	// so inspect reports it — and the header used to draw it as a badge, next
	// to "exited". The list never did, because it reads health out of the
	// status line, which a stopped container has none in. The two pages
	// disagreed about the same container.
	server := newServer(t, &stubSource{
		services: []domain.Service{stopped("c2", "db")},
		details: map[string]domain.ServiceDetail{
			"c2": {Service: domain.Service{
				ContainerID: "c2", Name: "db", Number: 1,
				State: domain.StateExited, Health: domain.HealthHealthy,
			}},
		},
	})

	_, body := get(t, server.URL+"/services/c2")
	header := body[:strings.Index(body, `class="detail-body"`)]

	assert.Contains(t, header, "badge-exited")
	assert.NotContains(t, header, "badge-health-healthy",
		"a stopped container has no current health")

	// The information is not thrown away, just moved somewhere it cannot be
	// read as the container's present state.
	assert.Contains(t, body, "Health when last running")
}

func TestDetailShowsHealthWhileRunning(t *testing.T) {
	server := newServer(t, &stubSource{
		services: []domain.Service{running("c1", "web")},
		details: map[string]domain.ServiceDetail{
			"c1": {Service: domain.Service{
				ContainerID: "c1", Name: "web", Number: 1,
				State: domain.StateRunning, Health: domain.HealthHealthy,
			}},
		},
	})

	_, body := get(t, server.URL+"/services/c1")
	header := body[:strings.Index(body, `class="detail-body"`)]

	assert.Contains(t, header, "badge-health-healthy")
	assert.NotContains(t, body, "Health when last running",
		"there is nothing past-tense to say about a container that is running")
}

func TestDetailShowsTheExitCodeOfAContainerThatHasStopped(t *testing.T) {
	finished := time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC)
	server := newServer(t, &stubSource{
		services: []domain.Service{stopped("c2", "db")},
		details: map[string]domain.ServiceDetail{
			"c2": {
				Service:    domain.Service{ContainerID: "c2", Name: "db", State: domain.StateExited},
				FinishedAt: finished,
				ExitCode:   137,
			},
		},
	})

	_, body := get(t, server.URL+"/services/c2")

	assert.Contains(t, body, "137")
}

func TestTheLifecyclePanelLeavesOutWhatSaysNothing(t *testing.T) {
	tests := []struct {
		name    string
		detail  domain.ServiceDetail
		absent  []string
		present []string
	}{
		{
			// A created-but-never-started container has an exit code of 0 and
			// no finish time, and has restarted no times. None of that is
			// information.
			name:   "never run",
			detail: domain.ServiceDetail{Service: domain.Service{ContainerID: "c3", Name: "new", State: domain.StateCreated}},
			absent: []string{"<dt>Exit code</dt>", "<dt>Finished</dt>", "<dt>Restart count</dt>", "<dt>User</dt>"},
		},
		{
			// Exited cleanly: it says so by being exited.
			name: "exited with zero",
			detail: domain.ServiceDetail{
				Service:    domain.Service{ContainerID: "c3", Name: "new", State: domain.StateExited},
				FinishedAt: time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC),
			},
			absent:  []string{"<dt>Exit code</dt>", "<dt>Restart count</dt>"},
			present: []string{"<dt>Finished</dt>"},
		},
		{
			name: "everything worth saying",
			detail: domain.ServiceDetail{
				Service:      domain.Service{ContainerID: "c3", Name: "new", State: domain.StateExited},
				FinishedAt:   time.Date(2026, 8, 15, 6, 0, 0, 0, time.UTC),
				ExitCode:     137,
				RestartCount: 3,
				User:         "postgres",
			},
			present: []string{"<dt>Exit code</dt>", "137", "<dt>Restart count</dt>", "<dt>User</dt>", "postgres"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newServer(t, &stubSource{
				services: []domain.Service{stopped("c3", "new")},
				details:  map[string]domain.ServiceDetail{"c3": test.detail},
			})

			_, body := get(t, server.URL+"/services/c3")

			for _, absent := range test.absent {
				assert.NotContains(t, body, absent)
			}
			for _, present := range test.present {
				assert.Contains(t, body, present)
			}
		})
	}
}

func TestActionButtonsAreGroupedWhereverTheyAppear(t *testing.T) {
	// As bare children of the detail header they picked up that header's own
	// much wider gap, so the pair sat further apart there than on the list.
	source := &stubSource{services: []domain.Service{stopped("c2", "db")}}
	server := newServerWithControl(t, source, &stubControl{})

	_, list := get(t, server.URL+"/services")
	_, detail := get(t, server.URL+"/services/c2")

	for name, body := range map[string]string{"list": list, "detail": detail} {
		assert.Contains(t, body, `<div class="actions">`, name+" groups its controls")

		// Start (a form) and Remove (a link to the confirmation) sit in the
		// one wrapper, so their spacing comes from it and not from whatever
		// surrounds it — as bare children of the detail header they picked up
		// that header's own much wider gap.
		group := body[strings.Index(body, `<div class="actions">`):]
		group = group[:strings.Index(group, "</div>")]
		assert.Contains(t, group, `class="action-form"`, name+" has the start form")
		assert.Contains(t, group, `class="action remove"`, name+" has the remove control")
	}
}

// --- overview, global header, resource removal -------------------------------

func fullServer(t *testing.T) (*httptest.Server, *stubNetworks, *stubVolumes) {
	t.Helper()

	source := &stubSource{
		services: []domain.Service{running("c1", "web"), stopped("c2", "db")},
		usage: domain.ResourceUsage{
			Networks: map[string]int{"example_default": 2},
			Volumes:  map[string]int{"example_pgdata": 1},
		},
	}
	networks := &stubNetworks{networks: []domain.Network{
		{ID: "n1", Name: "example_default", Driver: "bridge"},
		{ID: "n2", Name: "example_spare", Driver: "bridge"},
	}}
	volumes := &stubVolumes{volumes: []domain.Volume{
		{Name: "example_pgdata", Driver: "local"},
		{Name: "example_scratch", Driver: "local"},
	}}

	return newFullServer(t, source, &stubControl{}, networks, volumes), networks, volumes
}

func TestOverviewShowsWhatIsRunning(t *testing.T) {
	server, _, _ := fullServer(t)

	response, body := get(t, server.URL+"/")

	require.Equal(t, http.StatusOK, response.StatusCode)

	// Only the running services. The full lists are each a tab away.
	assert.Contains(t, body, `class="listing running-only"`)
	assert.NotContains(t, body, `href="/networks/n1"`)
	assert.NotContains(t, body, `href="/volumes/example_pgdata"`)

	// It keeps the live table's id, so the event stream patches this page
	// exactly as it patches the services page.
	assert.Contains(t, body, `id="service-rows"`)
}

func TestOverviewStillCountsWhatIsNotRunning(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/")

	// The stopped rows are rendered and hidden rather than left out, so the
	// totals stay counted from them — a stopped service is not concealed, it
	// is in the offline count.
	assert.Contains(t, body, `<span class="count" id="count-online">1</span>`)
	assert.Contains(t, body, `<span class="count" id="count-offline">1</span>`)
	assert.Contains(t, body, `data-online="false"`)
}

func TestOverviewSaysSoWhenNothingIsRunning(t *testing.T) {
	server := newFullServer(t,
		&stubSource{services: []domain.Service{stopped("c2", "db")}},
		nil, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/")

	// Rows exist, but none of them are shown — which is not the same as having
	// no services at all.
	assert.Contains(t, body, "Nothing is running")
	assert.NotContains(t, body, `id="empty" hidden`)
}

func TestGlobalHeaderNamesTheProjectOnEveryPage(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/", "/services", "/networks", "/volumes", "/services/c1"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			header := body[:strings.Index(body, "</header>")]
			assert.Contains(t, header, `class="brand"`,
				"the project's name is the one thing true of every page")
			assert.Contains(t, header, "Example project")
			assert.Contains(t, header, `class="logo" src="/static/favicon.svg"`)
			assert.Contains(t, body, `class="tab active"`)
		})
	}
}

func TestPagesNoLongerRepeatTheProjectAsTheirHeading(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/networks")

	assert.Contains(t, body, "<h1>Networks</h1>",
		"with the project in the global header, a page can say what it actually is")
}

func TestRemoveIsOfferedOnlyForUnusedNetworksAndVolumes(t *testing.T) {
	server, _, _ := fullServer(t)

	_, networks := get(t, server.URL+"/networks")
	assert.NotContains(t, networks, `href="/networks/n1/remove"`, "two containers are on it")
	assert.Contains(t, networks, `href="/networks/n2/remove"`)

	_, volumes := get(t, server.URL+"/volumes")
	assert.NotContains(t, volumes, `href="/volumes/example_pgdata/remove"`, "a container mounts it")
	assert.Contains(t, volumes, `href="/volumes/example_scratch/remove"`)
}

func TestNetworkRemoval(t *testing.T) {
	server, networks, _ := fullServer(t)

	response, _ := post(t, server.URL+"/networks/n2/remove", nil)

	assert.Equal(t, http.StatusSeeOther, response.StatusCode)
	assert.Equal(t, "/networks", response.Header.Get("Location"))
	assert.Equal(t, []string{"n2"}, networks.removed)
}

func TestNetworkRemovalIsRefusedWhileInUse(t *testing.T) {
	server, networks, _ := fullServer(t)

	response, body := post(t, server.URL+"/networks/n1/remove", nil)

	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Contains(t, body, "still in use")
	assert.Empty(t, networks.removed)
}

func TestVolumeRemoval(t *testing.T) {
	server, _, volumes := fullServer(t)

	response, _ := post(t, server.URL+"/volumes/example_scratch/remove", nil)

	assert.Equal(t, http.StatusSeeOther, response.StatusCode)
	assert.Equal(t, []string{"example_scratch"}, volumes.removed)
}

func TestVolumeRemovalIsRefusedWhileMounted(t *testing.T) {
	server, _, volumes := fullServer(t)

	response, body := post(t, server.URL+"/volumes/example_pgdata/remove", nil)

	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Contains(t, body, "still in use")
	assert.Empty(t, volumes.removed, "this is the only action that destroys data")
}

func TestVolumeRemovalIsRefusedCrossSite(t *testing.T) {
	server, _, volumes := fullServer(t)

	response, _ := post(t, server.URL+"/volumes/example_scratch/remove",
		map[string]string{"Sec-Fetch-Site": "cross-site"})

	assert.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Empty(t, volumes.removed)
}

func TestVolumeConfirmationWarnsThatDataIsDestroyed(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := getWith(t, server.URL+"/volumes/example_scratch/remove", map[string]string{"HX-Request": "true"})

	assert.Contains(t, body, "destroys data")
	assert.Contains(t, body, "cannot be undone")
}

func TestDetailHeaderPrefersTheImageTitleAndDescription(t *testing.T) {
	server := newServer(t, &stubSource{
		services: []domain.Service{running("c1", "web")},
		details: map[string]domain.ServiceDetail{
			"c1": {
				Service: domain.Service{
					ContainerID: "c1", Name: "web", State: domain.StateRunning,
					Title:       "Compose Monitor",
					Description: "Lists the services of one Compose project.",
				},
				Labels: []domain.Label{
					{Key: domain.LabelImageTitle, Value: "Compose Monitor"},
					{Key: domain.LabelImageDescription, Value: "Lists the services of one Compose project."},
					{Key: "com.docker.compose.project", Value: "example"},
					{Key: "traefik.enable", Value: "true"},
					{Key: "something.else", Value: "kept"},
				},
			},
		},
	})

	_, body := get(t, server.URL+"/services/c1")

	assert.Contains(t, body, "<h1>Compose Monitor</h1>")
	assert.Contains(t, body, "Lists the services of one Compose project.")
	assert.Contains(t, body, "<code>web</code>", "the name inside the project is still shown")

	// Every family folds separately, and anything unrecognised still appears.
	for _, group := range []string{"Compose labels", "Image (OCI) labels", "Traefik labels", "Other labels"} {
		assert.Contains(t, body, group)
	}
	assert.Contains(t, body, "something.else")
}

func TestTheStatusDotIsInTheGlobalHeaderOnEveryPage(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/", "/services", "/networks", "/volumes", "/services/c1"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			header := body[:strings.Index(body, "</header>")]
			assert.Contains(t, header, `id="connection"`,
				"the dot sits beside the project's name, not inside one page's own header")
			assert.Contains(t, header, `class="status-dot"`)

			// A dot alone says nothing to a reader who cannot hover over it.
			assert.Contains(t, header, `title="Connecting to the update stream"`)
			assert.Contains(t, header, `class="visually-hidden">connecting<`)
		})
	}
}

func TestPagesNoLongerCarryTheirOwnConnectionBlock(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/", "/services"} {
		_, body := get(t, server.URL+path)
		assert.NotContains(t, body, `class="connection"`)
		assert.Equal(t, 1, strings.Count(body, `id="connection"`),
			"exactly one status indicator per page, or the script would drive the wrong one")
	}
}

func TestDetailPagesCarryABreadcrumbTrail(t *testing.T) {
	server, _, _ := fullServer(t)

	tests := []struct {
		path  string
		trail []string
		links []string
	}{
		{"/services/c1", []string{"Example project", "Services", "web"},
			[]string{`href="/"`, `href="/services"`}},
		{"/networks/n1", []string{"Example project", "Networks", "example_default"},
			[]string{`href="/"`, `href="/networks"`}},
		{"/volumes/example_pgdata", []string{"Example project", "Volumes", "example_pgdata"},
			[]string{`href="/"`, `href="/volumes"`}},

		// The log sits three steps down, which is the case a single "back"
		// link could not describe.
		{"/services/c1/log", []string{"Example project", "Services", "web", "Log"},
			[]string{`href="/"`, `href="/services"`, `href="/services/c1"`}},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			_, body := get(t, server.URL+test.path)

			nav := body[strings.Index(body, `class="breadcrumbs"`):]
			nav = nav[:strings.Index(nav, "</nav>")]

			for _, label := range test.trail {
				assert.Contains(t, nav, label)
			}
			for _, link := range test.links {
				assert.Contains(t, nav, link)
			}

			// The last step is the page itself: marked, not linked.
			assert.Contains(t, nav, `aria-current="page">`+test.trail[len(test.trail)-1]+`<`)
		})
	}
}

func TestBreadcrumbsStartAtTheProject(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/services/c1", "/networks/n1", "/volumes/example_pgdata"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)
			nav := body[strings.Index(body, `class="breadcrumbs"`):]
			nav = nav[:strings.Index(nav, "</nav>")]

			// The root is the project, and it is the way back to the front page.
			assert.Contains(t, nav, `<li><a href="/">Example project</a></li>`)
			assert.True(t, strings.Index(nav, "Example project") < strings.Index(nav, "aria-current"),
				"the project comes first")
		})
	}
}

func TestEmptyPanelsAreLeftOutRatherThanFilledWithNothing(t *testing.T) {
	// A container with no mounts, no environment and only compose's own labels.
	server := newServer(t, &stubSource{
		services: []domain.Service{running("c1", "web")},
		details: map[string]domain.ServiceDetail{
			"c1": {
				Service: domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
				Labels:  []domain.Label{{Key: "com.docker.compose.project", Value: "example"}},
			},
		},
	})

	_, body := get(t, server.URL+"/services/c1")

	assert.NotContains(t, body, "<h2>Mounts</h2>")
	assert.NotContains(t, body, "Environment")
	for _, absent := range []string{"Image (OCI) labels", "Traefik labels", "Other labels"} {
		assert.NotContains(t, body, absent)
	}

	// The one group that has members is still there, as is the rest of the page.
	assert.Contains(t, body, "Compose labels")
	assert.Contains(t, body, "Lifecycle")
	assert.Contains(t, body, `<span class="fold-title">Log</span>`,
		"the log cannot know it is empty without being read, so its fold stays")
}

func TestPanelsWithContentAreStillDrawn(t *testing.T) {
	server := newServer(t, &stubSource{
		services: []domain.Service{running("c1", "web")},
		details: map[string]domain.ServiceDetail{
			"c1": {
				Service:     domain.Service{ContainerID: "c1", Name: "web", State: domain.StateRunning},
				Mounts:      []domain.Mount{{Type: "volume", Source: "example_pgdata", Destination: "/data", ReadWrite: true}},
				Environment: []domain.Label{{Key: "POSTGRES_DB", Value: "example"}},
			},
		},
	})

	_, body := get(t, server.URL+"/services/c1")

	assert.Contains(t, body, "<h2>Mounts</h2>")
	assert.Contains(t, body, "example_pgdata")
	assert.Contains(t, body, "Environment")
	assert.Contains(t, body, "POSTGRES_DB")
}

func TestTheTwoListingsDifferOnlyByTheState(t *testing.T) {
	server, _, _ := fullServer(t)

	_, overview := get(t, server.URL+"/")
	_, services := get(t, server.URL+"/services")

	// Everything on the front page is running, so a state on every entry would
	// be one repeated word. It is not hidden; it is not rendered.
	assert.Contains(t, overview, `class="listing running-only"`)
	assert.NotContains(t, overview, "badge-running")

	// The services page carries every state, so it leads each entry with it.
	assert.Contains(t, services, "badge-running")
	assert.Contains(t, services, "badge-exited")
	assert.NotContains(t, services, "running-only")

	// Everything else is the same listing.
	for _, shared := range []string{`class="listing"`, "badge-container", "badge-uptime", `class="entry-head"`} {
		assert.Contains(t, services, shared)
	}
}

func TestTheStylesheetStillHidesStoppedServicesOnTheFrontPage(t *testing.T) {
	server, _, _ := fullServer(t)

	_, css := get(t, server.URL+"/static/css/app.css")

	// The stopped ones are rendered and hidden, which is what keeps the totals
	// above them counted from the rows.
	assert.Contains(t, css, `.running-only .service[data-online="false"]`)
}

func TestListingsShortenADigestButKeepItReachable(t *testing.T) {
	const pinned = "ghcr.io/example/api@sha256:1010acc839eccd5694743efd676ada2ff40e0dedc6dc75025ecbc33976821a9c"

	server := newServer(t, &stubSource{services: []domain.Service{{
		ContainerID: "c1", Name: "api", Number: 1,
		State: domain.StateRunning, Status: "Up 1 minute", Image: pinned,
	}}})

	_, body := get(t, server.URL+"/services")

	assert.Contains(t, body, "ghcr.io/example/api@sha256:1010acc839ec")
	assert.NotContains(t, body, ">"+pinned+"<", "the full digest is not what the column shows")

	// It is still there to read, in the cell's title.
	assert.Contains(t, body, `title="`+pinned+`"`)
}

func TestDetailKeepsTheWholeImageReference(t *testing.T) {
	const pinned = "ghcr.io/example/api@sha256:1010acc839eccd5694743efd676ada2ff40e0dedc6dc75025ecbc33976821a9c"

	server := newServer(t, &stubSource{
		services: []domain.Service{running("c1", "api")},
		details: map[string]domain.ServiceDetail{
			"c1": {Service: domain.Service{
				ContainerID: "c1", Name: "api", State: domain.StateRunning, Image: pinned,
			}},
		},
	})

	_, body := get(t, server.URL+"/services/c1")

	// A listing is a column with everything else competing for the width. The
	// detail page is where the whole reference belongs.
	assert.Contains(t, body, pinned)
}

func TestEverySectionPageCarriesTheTrailToo(t *testing.T) {
	server, _, _ := fullServer(t)

	for path, section := range map[string]string{
		"/services": "Services",
		"/networks": "Networks",
		"/volumes":  "Volumes",
	} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			nav := body[strings.Index(body, `class="breadcrumbs"`):]
			nav = nav[:strings.Index(nav, "</nav>")]

			assert.Contains(t, nav, `<li><a href="/">Example project</a></li>`)
			assert.Contains(t, nav, `aria-current="page">`+section+`<`,
				"a section page is the end of its own trail")
		})
	}
}

func TestTheFrontPageIsTheRootAndHasNoTrail(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/")

	// There is nowhere above the root to trace back to, and the project is
	// already named in the header.
	assert.NotContains(t, body, `class="breadcrumbs"`)
}

func TestTheOverviewShowsHealthAndUptimeBesideTheContainerId(t *testing.T) {
	source := &stubSource{services: []domain.Service{{
		ContainerID: "c1", Name: "web", Number: 1, ContainerName: "example-web-1",
		State: domain.StateRunning, Status: "Up 2 hours (healthy)", Elapsed: "2 hours",
		Health: domain.HealthHealthy,
		Image:  "nginx:1.27",
		Ports:  []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}},
	}}}
	server := newFullServer(t, source, nil, &stubNetworks{}, &stubVolumes{})

	_, overview := get(t, server.URL+"/")

	entry := overview[strings.Index(overview, `<li class="entry service"`):]
	entry = entry[:strings.Index(entry, "</li>")]
	sub := entry[strings.Index(entry, `class="sub"`):]

	assert.Contains(t, sub, "example-web-1")
	uptime := sub[strings.Index(sub, "badge-uptime"):]
	assert.Contains(t, uptime[:strings.Index(uptime, "</span></span>")+len("</span>")], "<span>2 hours</span>",
		`every line here is up, so the word "Up" says nothing; the duration is what differs`)

	// Container id, then uptime, then health, then image, then ports.
	order := []string{"badge-container", "badge-uptime", "badge-health-healthy", "badge-image", "badge-port"}
	for i := 1; i < len(order); i++ {
		assert.Less(t, strings.Index(sub, order[i-1]), strings.Index(sub, order[i]),
			order[i-1]+" comes before "+order[i])
	}

	// The services page draws the same entry, with the state in front of it.
	_, services := get(t, server.URL+"/services")
	assert.Contains(t, services, "badge-running")
	assert.Contains(t, services, "badge-uptime")
}

func TestServiceListingsDropTheTotal(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/", "/services"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			assert.NotContains(t, body, `id="count-total"`)
			assert.Contains(t, body, `id="count-online"`)
			assert.Contains(t, body, `id="count-offline"`)
		})
	}
}

func TestOverviewHasNoSubtitle(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/")

	assert.NotContains(t, body, "Everything Compose created")
	assert.Contains(t, body, "<h1>Overview</h1>")
}

func TestBothServicePagesAreListings(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/", "/services"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			listing := body[strings.Index(body, `id="service-rows"`):]
			listing = listing[:strings.Index(listing, "</ul>")]
			assert.Contains(t, listing, "<li")
			assert.NotContains(t, listing, "<td", "the table shape has no caller left")
		})
	}
}

func TestTheStateBadgeComesFirstOnTheServicesPage(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/services")
	entry := body[strings.Index(body, `<li class="entry service"`):]
	entry = entry[:strings.Index(entry, "</li>")]

	assert.Less(t, strings.Index(entry, "badge-running"), strings.Index(entry, "badge-uptime"),
		"the state is what tells the entries on this page apart, so it leads")
}

func TestEveryListingIsTheSameShape(t *testing.T) {
	server, _, _ := fullServer(t)

	// Services, networks and volumes are all entries with a name, controls,
	// and their facts as badges beside them.
	for _, path := range []string{"/", "/services", "/networks", "/volumes"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			listing := body[strings.Index(body, `<ul class="listing`):]
			listing = listing[:strings.Index(listing, "</ul>")]

			assert.Contains(t, listing, `<li class="entry`)
			assert.Contains(t, listing, `class="entry-head"`)
			assert.Contains(t, listing, `class="sub"`)
			assert.Contains(t, listing, "<svg", "the badges carry their marks here too")
			assert.NotContains(t, body, "<table", "nothing is laid out as a table any more")
		})
	}
}

func TestEveryBadgeCarriesAMark(t *testing.T) {
	source := &stubSource{services: []domain.Service{{
		ContainerID: "c1", Name: "web", Number: 1, ContainerName: "example-web-1",
		State: domain.StateRunning, Status: "Up 2 hours (healthy)", Elapsed: "2 hours",
		Health: domain.HealthHealthy,
		Image:  "nginx:1.27",
		Ports:  []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}},
	}}}
	server := newFullServer(t, source, nil, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/")
	entry := body[strings.Index(body, `<li class="entry service"`):]
	entry = entry[:strings.Index(entry, "</li>")]
	sub := entry[strings.Index(entry, `class="sub"`):]

	// One mark per badge, inlined so each takes its badge's own colour.
	assert.Equal(t, 5, strings.Count(sub, "<svg"),
		"container, uptime, health, image and one port")
	assert.Equal(t, 5, strings.Count(sub, `stroke="currentColor"`),
		"linked rather than inlined, an icon would render in its own document and know nothing of the text beside it")
}

func TestPortsAreBadgesOnTheListing(t *testing.T) {
	server, _, _ := fullServer(t)

	_, css := get(t, server.URL+"/static/css/app.css")

	assert.Contains(t, css, ".badge-port-published")
}

func TestTheListingLeadsWithWhatTheImageCallsItself(t *testing.T) {
	source := &stubSource{services: []domain.Service{{
		ContainerID: "c1", Name: "monitor", Number: 1, ContainerName: "example-monitor-1",
		State: domain.StateRunning, Status: "Up 2 hours",
		Image: "ghcr.io/k15g/compose-monitor:1",
		Title: "Compose Monitor", Description: "Lists the services of one Compose project.",
		Ports: []domain.Port{{Host: 8080, Container: 8080, Protocol: "tcp"}},
	}}}
	server := newFullServer(t, source, nil, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/")
	entry := body[strings.Index(body, "<li class=\"entry service\""):]
	entry = entry[:strings.Index(entry, "</li>")]

	assert.Contains(t, entry, ">Compose Monitor</a>", "the image's own title leads")
	assert.Contains(t, entry, `<span class="service-name">monitor</span>`,
		"the name inside the project sits beside it")
	assert.Contains(t, entry, "Lists the services of one Compose project.")

	// Title, then the service name, then the description, then the badges.
	order := []string{"Compose Monitor", "service-name", "description", "container", "badge-uptime"}
	for i := 1; i < len(order); i++ {
		assert.Less(t, strings.Index(entry, order[i-1]), strings.Index(entry, order[i]),
			order[i-1]+" comes before "+order[i])
	}
}

func TestTheListingSaysNothingExtraWhenTheImageDoesNot(t *testing.T) {
	// Most images declare neither label, and the entry should not grow an
	// empty line for each of them.
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/")
	entry := body[strings.Index(body, "<li class=\"entry service\""):]
	entry = entry[:strings.Index(entry, "</li>")]

	assert.NotContains(t, entry, "service-name")
	assert.NotContains(t, entry, `class="description"`)
}

func TestTheListingIsSeparatedByRulesRatherThanBoxed(t *testing.T) {
	server, _, _ := fullServer(t)

	_, css := get(t, server.URL+"/static/css/app.css")

	list := css[strings.Index(css, "ul.listing {"):]
	list = list[:strings.Index(list, "}")]
	assert.NotContains(t, list, "border:", "no box around the set")
	assert.NotContains(t, list, "background:")

	entry := css[strings.Index(css, "li.entry {"):]
	entry = entry[:strings.Index(entry, "}")]
	assert.Contains(t, entry, "border-top: 1px solid var(--border)", "a line between entries")
}

func TestOpenIsOfferedWhereTheServiceAnswersOnTheWeb(t *testing.T) {
	routed := domain.Service{
		ContainerID: "c1", Name: "web", Number: 1, State: domain.StateRunning,
		Status: "Up 1 minute", URL: "https://app.example.com",
	}
	unrouted := domain.Service{
		ContainerID: "c3", Name: "worker", Number: 1, State: domain.StateRunning, Status: "Up 1 minute",
	}
	stoppedButRouted := domain.Service{
		ContainerID: "c4", Name: "old", Number: 1, State: domain.StateExited,
		Status: "Exited (0) 1 hour ago", URL: "https://old.example.com",
	}

	source := &stubSource{services: []domain.Service{routed, unrouted, stoppedButRouted}}
	server := newFullServer(t, source, &stubControl{}, &stubNetworks{}, &stubVolumes{})

	// On both listings, and on the detail page.
	for _, path := range []string{"/", "/services", "/services/c1"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			assert.Contains(t, body, `href="https://app.example.com"`)
			assert.Contains(t, body, "<span>Open</span>")
		})
	}

	_, listing := get(t, server.URL+"/services")
	assert.NotContains(t, listing, "old.example.com",
		"a link to a service that is not running goes nowhere")
}

func TestOpenIsNotAControlAndSurvivesControlBeingOff(t *testing.T) {
	source := &stubSource{services: []domain.Service{{
		ContainerID: "c1", Name: "web", Number: 1, State: domain.StateRunning,
		Status: "Up 1 minute", URL: "https://app.example.com",
	}}}
	// No control: no start, stop or remove anywhere.
	server := newFullServer(t, source, nil, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/")

	// Opening a service does nothing to it, so it is not gated with the
	// actions that do.
	assert.Contains(t, body, `href="https://app.example.com"`)
	assert.NotContains(t, body, "/stop")
	assert.NotContains(t, body, "/remove")
}

func TestTheListingEntryIsThreeLines(t *testing.T) {
	source := &stubSource{services: []domain.Service{{
		ContainerID: "c1", Name: "monitor", Number: 1, ContainerName: "example-monitor-1",
		State: domain.StateRunning, Status: "Up 2 hours",
		Title: "Compose Monitor", Description: "Lists the services of one Compose project.",
		Image: "ghcr.io/k15g/compose-monitor:1", URL: "https://monitor.example.com",
	}}}
	server := newFullServer(t, source, &stubControl{}, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/")
	entry := body[strings.Index(body, `<li class="entry service"`):]
	entry = entry[:strings.Index(entry, "</li>")]

	// The name and the controls share the first line, so the description and
	// the badges each get the width to themselves.
	head := entry[strings.Index(entry, `class="entry-head"`):]
	head = head[:strings.Index(head, `</span></span>`)+len(`</span></span>`)]
	assert.Contains(t, head, "Compose Monitor")
	assert.Contains(t, head, `class="col-actions"`)
	assert.NotContains(t, head, `class="description"`)
	assert.NotContains(t, head, `class="sub"`)

	// Then the description, then the badges, each after the controls.
	order := []string{"entry-head", "col-actions", `class="description"`, `class="sub"`}
	for i := 1; i < len(order); i++ {
		assert.Less(t, strings.Index(entry, order[i-1]), strings.Index(entry, order[i]),
			order[i-1]+" comes before "+order[i])
	}
}

func TestTheDescriptionIsNotBoxedIntoAColumn(t *testing.T) {
	server, _, _ := fullServer(t)

	_, css := get(t, server.URL+"/static/css/app.css")

	// Asserting the rule has no max-width is not the same as asserting there
	// is none: `.description` carries 60ch for the detail header, and it
	// applies to anything a more specific rule does not override. Saying
	// nothing here is how the listing's description stayed capped.
	rule := css[strings.Index(css, "li.entry .description {"):]
	rule = rule[:strings.Index(rule, "}")]
	assert.Contains(t, rule, "max-width: none",
		"it has the line to itself, so it has to say it can use it")

	entry := css[strings.Index(css, "li.entry {"):]
	entry = entry[:strings.Index(entry, "}")]
	assert.Contains(t, entry, "display: block",
		"the entry stacks its lines rather than laying them out in a row")
}

func TestNothingIsCrawlable(t *testing.T) {
	server, _, _ := fullServer(t)

	response, body := get(t, server.URL+"/robots.txt")
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, body, "User-agent: *")
	assert.Contains(t, body, "Disallow: /")

	// robots.txt only asks a crawler not to fetch a page. A crawler that has a
	// link to one anyway can still list it, so every page says so itself.
	for _, path := range []string{"/", "/services", "/networks", "/volumes", "/services/c1"} {
		t.Run(path, func(t *testing.T) {
			_, page := get(t, server.URL+path)
			assert.Contains(t, page, `<meta name="robots" content="noindex, nofollow">`)
		})
	}
}

func TestEveryControlCarriesAMark(t *testing.T) {
	source := &stubSource{services: []domain.Service{
		{ContainerID: "c1", Name: "web", Number: 1, State: domain.StateRunning,
			Status: "Up 1 minute", Elapsed: "1 minute", URL: "https://app.example.com"},
		{ContainerID: "c2", Name: "db", Number: 1, State: domain.StateExited,
			Status: "Exited (0) 1 hour ago", Elapsed: "1 hour ago"},
	}}
	server := newFullServer(t, source, &stubControl{}, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/services")

	for _, control := range []string{"open", "stop", "start", "remove"} {
		t.Run(control, func(t *testing.T) {
			at := strings.Index(body, `class="action `+control+`"`)
			require.NotEqual(t, -1, at, control+" is drawn")

			// The mark sits inside the control, before its caption — and the
			// caption is its own element so the script can change the wording
			// without taking the mark with it.
			markup := body[at:]
			markup = markup[:strings.Index(markup, "</a>")+1]
			if !strings.Contains(markup, "<svg") {
				markup = body[at:]
				markup = markup[:strings.Index(markup, "</button>")]
			}
			assert.Contains(t, markup, "<svg")
			assert.Contains(t, markup, "<span>")
		})
	}
}

func TestTheUptimeBadgeDropsTheStateForStoppedServices(t *testing.T) {
	source := &stubSource{services: []domain.Service{{
		ContainerID: "c2", Name: "db", Number: 1, State: domain.StateExited,
		Status: "Exited (0) 2 hours ago", Elapsed: "2 hours ago",
	}}}
	server := newFullServer(t, source, nil, &stubNetworks{}, &stubVolumes{})

	_, body := get(t, server.URL+"/services")
	entry := body[strings.Index(body, `<li class="entry service"`):]
	entry = entry[:strings.Index(entry, "</li>")]

	// The state has a badge of its own beside this one.
	assert.Contains(t, entry, "<span>2 hours ago</span>")
	assert.Contains(t, entry, "badge-exited")
	assert.NotContains(t, entry, "<span>Exited (0) 2 hours ago</span>")

	// The runtime's own wording is still a hover away, and it is the only
	// place the exit code is shown in the listing.
	assert.Contains(t, entry, `title="Exited (0) 2 hours ago"`)
}

func TestTheVolumesListingKeepsToWhatDistinguishesThem(t *testing.T) {
	server, _, _ := fullServer(t)

	_, body := get(t, server.URL+"/volumes")

	// Neither the creation time nor the host path tells one volume from
	// another at a glance; both are on the detail page.
	assert.NotContains(t, body, "badge-uptime")
	assert.NotContains(t, body, "/var/lib/docker/volumes")

	assert.Contains(t, body, "badge-driver")
	assert.Contains(t, body, "unused", "what can be removed is worth seeing in the listing")

	_, detail := get(t, server.URL+"/volumes/example_pgdata")
	assert.Contains(t, detail, "Mountpoint")
	assert.Contains(t, detail, "Created")
}

func TestTheNetworkDetailPageIsLaidOutLikeAService(t *testing.T) {
	networks := &stubNetworks{networks: []domain.Network{{
		ID: "n1", Name: "example_default", Driver: "bridge", Scope: "local",
		Subnets: []domain.Subnet{{Range: "172.20.0.0/16", Gateway: "172.20.0.1"}},
		Members: []domain.NetworkMember{{ContainerID: "c1", Name: "example-web-1", IPv4Address: "172.20.0.2/16"}},
		Options: []domain.Label{{Key: "com.docker.network.bridge.enable_icc", Value: "true"}},
		Labels:  []domain.Label{{Key: "com.docker.compose.project", Value: "example"}},
	}}}
	server := newFullServer(t, &stubSource{}, nil, networks, &stubVolumes{})

	_, body := get(t, server.URL+"/networks/n1")

	// Three regions, in the same order as a service: main first in the source
	// so the sidebar sits on the right and stacks below on a narrow screen.
	assert.Contains(t, body, `<main class="detail">`)
	assert.Contains(t, body, `class="detail-body"`)
	assert.Less(t, strings.Index(body, `class="detail-main"`), strings.Index(body, `class="sidebar"`))
	assert.NotContains(t, body, `class="panels"`)

	sidebar := body[strings.Index(body, `class="sidebar"`):]
	assert.Contains(t, sidebar, "<h2>Network</h2>", "what identifies it")

	main := body[strings.Index(body, `class="detail-main"`):strings.Index(body, `class="sidebar"`)]
	for _, panel := range []string{"Addressing", "Connected containers", "Options"} {
		assert.Contains(t, main, "<h2>"+panel+"</h2>", panel+" is main content")
	}

	// Labels are grouped and folded, the same way a service's are.
	assert.Contains(t, main, `class="fold-title">Compose labels<`)
}

func TestTheNetworkDetailPageLeavesOutEmptyPanels(t *testing.T) {
	networks := &stubNetworks{networks: []domain.Network{{ID: "n1", Name: "bare", Driver: "bridge"}}}
	server := newFullServer(t, &stubSource{}, nil, networks, &stubVolumes{})

	_, body := get(t, server.URL+"/networks/n1")

	assert.NotContains(t, body, "<h2>Options</h2>")
	assert.NotContains(t, body, "labels<")

	// Addressing and the members stay: on a network, having neither is worth
	// being told rather than left to infer from an absent panel.
	assert.Contains(t, body, "<h2>Addressing</h2>")
	assert.Contains(t, body, "no subnet configured")
	assert.Contains(t, body, "Nothing is on this network")
}

func TestTheVolumeDetailPageIsLaidOutLikeTheOthers(t *testing.T) {
	volumes := &stubVolumes{volumes: []domain.Volume{{
		Name: "example_pgdata", Driver: "local", Scope: "local", Size: -1,
		Mountpoint: "/var/lib/docker/volumes/example_pgdata/_data",
		Options:    []domain.Label{{Key: "type", Value: "nfs"}},
		Labels:     []domain.Label{{Key: "com.docker.compose.project", Value: "example"}},
	}}}
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, volumes)

	_, body := get(t, server.URL+"/volumes/example_pgdata")

	assert.Contains(t, body, `<main class="detail">`)
	assert.Less(t, strings.Index(body, `class="detail-main"`), strings.Index(body, `class="sidebar"`))
	assert.NotContains(t, body, `class="panels"`)

	sidebar := body[strings.Index(body, `class="sidebar"`):]
	assert.Contains(t, sidebar, "<h2>Volume</h2>")
	assert.Contains(t, sidebar, "Mountpoint")
	assert.Contains(t, sidebar, "path on the Docker host",
		"the caveat belongs beside the thing it is about")

	main := body[strings.Index(body, `class="detail-main"`):strings.Index(body, `class="sidebar"`)]
	assert.Contains(t, main, `class="fold-title">Compose labels<`)
	assert.Contains(t, main, "<h2>Options</h2>")
	assert.NotContains(t, main, "Mountpoint")
}

func TestAVolumeThatSaysNothingExtraShowsNothingExtra(t *testing.T) {
	// Most volumes have no driver options, which is why they are in the main
	// column rather than the sidebar: a sidebar entry that is usually absent
	// is a hole in the sidebar.
	volumes := &stubVolumes{volumes: []domain.Volume{{Name: "bare", Driver: "local", Size: -1}}}
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, volumes)

	_, body := get(t, server.URL+"/volumes/bare")

	assert.NotContains(t, body, "<h2>Options</h2>")
	assert.NotContains(t, body, "labels<")

	// What identifies it is still there.
	assert.Contains(t, body, "<h2>Volume</h2>")
	assert.Contains(t, body, "Mountpoint")
}

func TestSizeIsShownOnlyWhenItWasMeasured(t *testing.T) {
	// Measuring makes the daemon walk the filesystem, so this page never asks.
	// A row reading "not measured" says nothing about the volume, only about
	// what was asked for.
	unmeasured := &stubVolumes{volumes: []domain.Volume{{Name: "v", Driver: "local", Size: -1}}}
	server := newFullServer(t, &stubSource{}, nil, &stubNetworks{}, unmeasured)
	_, body := get(t, server.URL+"/volumes/v")

	assert.NotContains(t, body, "not measured")
	assert.NotContains(t, body, "<dt>Size</dt>")

	measured := &stubVolumes{volumes: []domain.Volume{{Name: "v", Driver: "local", Size: 1536}}}
	server = newFullServer(t, &stubSource{}, nil, &stubNetworks{}, measured)
	_, body = get(t, server.URL+"/volumes/v")

	assert.Contains(t, body, "<dt>Size</dt>")
	assert.Contains(t, body, "1.5 KiB")
}

func TestEveryDetailPageHasTheSameThreeRegions(t *testing.T) {
	server, _, _ := fullServer(t)

	for _, path := range []string{"/services/c1", "/networks/n1", "/volumes/example_pgdata"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			assert.Contains(t, body, `<main class="detail">`)
			assert.Contains(t, body, `class="detail-header"`)
			assert.Contains(t, body, `class="detail-body"`)

			// Main before the sidebar in the source, on all three: the sidebar
			// then sits on the right and stacks below rather than above.
			assert.Less(t, strings.Index(body, `class="detail-main"`), strings.Index(body, `class="sidebar"`))
		})
	}
}

func TestLabelsAreGroupedTheSameWayEverywhere(t *testing.T) {
	labels := []domain.Label{
		{Key: "com.docker.compose.project", Value: "example"},
		{Key: "traefik.enable", Value: "true"},
		{Key: "maintainer", Value: "someone"},
	}
	onlyCompose := []domain.Label{{Key: "com.docker.compose.project", Value: "example"}}

	networks := &stubNetworks{networks: []domain.Network{
		{ID: "n1", Name: "mixed", Driver: "bridge", Labels: labels},
		{ID: "n2", Name: "plain", Driver: "bridge", Labels: onlyCompose},
	}}
	volumes := &stubVolumes{volumes: []domain.Volume{
		{Name: "mixed", Driver: "local", Size: -1, Labels: labels},
		{Name: "plain", Driver: "local", Size: -1, Labels: onlyCompose},
	}}
	server := newFullServer(t, &stubSource{}, nil, networks, volumes)

	for _, path := range []string{"/networks/n1", "/volumes/mixed"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			for _, group := range []string{"Compose labels", "Traefik labels", "Other labels"} {
				assert.Contains(t, body, `class="fold-title">`+group+`<`)
			}
			assert.NotContains(t, body, "Image (OCI) labels", "a network carries none")
		})
	}

	// The generic group appears only when something is in it: a resource
	// carrying nothing but Compose's own labels shows one fold, not two.
	for _, path := range []string{"/networks/n2", "/volumes/plain"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, server.URL+path)

			assert.Contains(t, body, `class="fold-title">Compose labels<`)
			assert.NotContains(t, body, "Other labels")
			assert.NotContains(t, body, "Traefik labels")
		})
	}
}

func TestTheNetworkDetailPageCountsNothingItAlsoLists(t *testing.T) {
	networks := &stubNetworks{networks: []domain.Network{{
		ID: "n1", Name: "example_default", Driver: "bridge",
		Members: []domain.NetworkMember{
			{ContainerID: "c1", Name: "example-web-1", IPv4Address: "172.20.0.2/16"},
			{ContainerID: "c2", Name: "example-db-1", IPv4Address: "172.20.0.3/16"},
		},
	}}}
	volumes := &stubVolumes{volumes: []domain.Volume{{Name: "data", Driver: "local", Size: -1}}}
	server := newFullServer(t, &stubSource{}, nil, networks, volumes)

	_, body := get(t, server.URL+"/networks/n1")

	// The containers are named in the main column, so a row counting them says
	// less than the list beside it.
	assert.NotContains(t, body, "<dt>In use</dt>")
	assert.Contains(t, body, "example-web-1")
	assert.Contains(t, body, "example-db-1")

	// A volume has no such list, so its own count stays.
	_, volume := get(t, server.URL+"/volumes/data")
	assert.Contains(t, volume, "<dt>Used by</dt>")
}

func TestEveryPageIsWellFormed(t *testing.T) {
	// An unclosed element does not fail a render, a test that greps for a
	// string, or anything else short of looking at the page — it just swallows
	// everything after it into the element that was never closed. Counting the
	// tags is what catches it.
	source := &stubSource{services: []domain.Service{
		{ContainerID: "c1", Name: "web", Number: 1, State: domain.StateRunning,
			Status: "Up 1 minute", Elapsed: "1 minute", URL: "https://app.example.com"},
		{ContainerID: "c2", Name: "db", Number: 1, State: domain.StateExited,
			Status: "Exited (0) 1 hour ago", Elapsed: "1 hour ago"},
	}}
	networks := &stubNetworks{networks: []domain.Network{{ID: "n1", Name: "net", Driver: "bridge"}}}
	volumes := &stubVolumes{volumes: []domain.Volume{{Name: "vol", Driver: "local", Size: -1}}}

	paths := []string{
		"/", "/services", "/networks", "/volumes",
		"/services/c1", "/services/c2", "/networks/n1", "/volumes/vol",
	}

	// Both ways round: the controls are what differ, and the bug this catches
	// only appeared with them off.
	for _, controllable := range []bool{true, false} {
		var control ports.ContainerControl
		if controllable {
			control = &stubControl{}
		}
		server := newFullServer(t, source, control, networks, volumes)

		for _, path := range paths {
			t.Run(fmt.Sprintf("control=%v %s", controllable, path), func(t *testing.T) {
				_, body := get(t, server.URL+path)

				for _, tag := range []string{"div", "span", "ul", "li", "section", "table", "form", "p"} {
					opened := len(regexp.MustCompile(`<`+tag+`[\s>]`).FindAllString(body, -1))
					closed := strings.Count(body, "</"+tag+">")
					assert.Equal(t, opened, closed, "<"+tag+"> opened and closed the same number of times")
				}
			})
		}
	}
}
