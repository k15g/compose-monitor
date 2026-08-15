package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
)

// Adapter serves the event stream.
type Adapter struct {
	hub       *Hub
	renderer  *Renderer
	monitor   *app.MonitorService
	keepAlive time.Duration
}

// NewAdapter creates the event-stream adapter.
func NewAdapter(ctx context.Context, hub *Hub, renderer *Renderer, monitor *app.MonitorService) *Adapter {
	cfg := config.GetConfig(ctx)
	return &Adapter{
		hub:       hub,
		renderer:  renderer,
		monitor:   monitor,
		keepAlive: cfg.Http.KeepAlive,
	}
}

// Mount registers the stream's route.
func (a *Adapter) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /events", a.stream)
}

// syncPayload is the first frame of every connection: the full set of rows, so
// a client that has just connected — or reconnected after a drop — is correct
// without needing to have seen the events it missed.
type syncPayload struct {
	Services []rowPayload `json:"services"`
}

// changePayload is one change. HTML is empty for a removal, where the row is
// only being taken away.
type changePayload struct {
	Action domain.Action `json:"action"`
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	HTML   string        `json:"html,omitempty"`

	// Notable says whether something happened, as opposed to the elapsed time
	// in the status having advanced. The client redraws either way and marks
	// only the notable ones.
	Notable bool `json:"notable"`
}

type rowPayload struct {
	ID   string `json:"id"`
	HTML string `json:"html"`
}

// stream holds the connection open and writes every change to it until the
// client goes away or the hub closes.
func (a *Adapter) stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Subscribe before reading the current state, not after. The other order
	// leaves a gap: a change landing between the read and the subscription
	// would appear in neither, and the client would hold a stale row until
	// something else happened to it. Overlapping the other way is harmless,
	// because the client applies added and updated as the same upsert.
	// Which shape this subscriber wants. Rendering happens here rather than
	// where the event is produced, because a service is drawn differently on
	// different pages and only the connection knows which page it belongs to.
	view := ParseView(r.URL.Query().Get("view"))

	events, unsubscribe := a.hub.Subscribe(ctx)
	defer unsubscribe()

	services, err := a.monitor.List(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "reading services for event stream failed", "error", err)
		http.Error(w, "container runtime unavailable", http.StatusServiceUnavailable)
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Tell a buffering reverse proxy not to. Without it the stream is held
	// until the proxy's buffer fills, which for these frames is never.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	controller := http.NewResponseController(w)

	rows := make([]rowPayload, 0, len(services))
	for _, service := range services {
		html, err := a.renderer.Render(ctx, service, view)
		if err != nil {
			slog.ErrorContext(ctx, "rendering row for snapshot failed",
				"service", service.Name, "error", err)
			continue
		}
		rows = append(rows, rowPayload{ID: service.ContainerID, HTML: html})
	}

	if err := writeEvent(w, controller, "sync", syncPayload{Services: rows}); err != nil {
		return
	}

	keepAlive := time.NewTicker(a.keepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case event, open := <-events:
			if !open {
				// The hub closed, which happens on shutdown. Ending the
				// handler lets the server's graceful shutdown complete
				// instead of waiting out every open stream.
				return
			}
			payload := changePayload{
				Action:  event.Action,
				ID:      event.Service.ContainerID,
				Name:    event.Service.Name,
				Notable: event.Notable,
			}

			// A removal takes the row away, so there is nothing to draw.
			if event.Action != domain.ActionRemoved {
				html, err := a.renderer.Render(ctx, event.Service, view)
				if err != nil {
					slog.ErrorContext(ctx, "rendering row for an event failed",
						"service", event.Service.Name, "error", err)
					continue
				}
				payload.HTML = html
			}
			if err := writeEvent(w, controller, "change", payload); err != nil {
				return
			}

		case <-keepAlive.C:
			// A comment frame. It carries nothing, and exists so an idle
			// connection is not closed by a proxy — or by the browser — during
			// a long stretch where nothing changes.
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

// writeEvent writes one named event and pushes it out.
//
// The flush is not optional: without it the frames sit in the response buffer
// and the client sees nothing until enough of them accumulate, which for a
// page that changes a few times an hour means it never updates at all.
func writeEvent(w http.ResponseWriter, controller *http.ResponseController, name string, payload any) error {
	// JSON encoding is what keeps the frame to one line: a rendered row
	// contains newlines, and a raw newline inside an SSE data field would end
	// the field early and split the frame.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding %s event: %w", name, err)
	}

	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return fmt.Errorf("writing %s event: %w", name, err)
	}
	if err := controller.Flush(); err != nil {
		return fmt.Errorf("flushing %s event: %w", name, err)
	}
	return nil
}
