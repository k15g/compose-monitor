// Package handlers serves the overview page.
package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/k15g/compose-monitor/internal/adapters/web/overview/templates"
	shared "github.com/k15g/compose-monitor/internal/adapters/web/shared/templates"
	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
)

// Handler renders everything the project is made of on one page.
type Handler struct {
	monitor  *app.MonitorService
	networks *app.NetworkService
	volumes  *app.VolumeService
	project  string
}

// New creates the handler.
func New(ctx context.Context, monitor *app.MonitorService, networks *app.NetworkService, volumes *app.VolumeService) *Handler {
	cfg := config.GetConfig(ctx)
	return &Handler{
		monitor:  monitor,
		networks: networks,
		volumes:  volumes,
		project:  cfg.Project.DisplayTitle(),
	}
}

// Mount registers the overview route.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.overview)
}

// overview shows what is running. The full lists — services including the
// stopped ones, networks, volumes — are each a tab away, so this page reads
// only the containers.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	services, err := h.monitor.List(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "reading services for the overview failed", "error", err)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		if err := shared.Unavailable(h.project, "overview", err.Error()).Render(ctx, w); err != nil {
			slog.ErrorContext(ctx, "rendering unavailable page failed", "error", err)
		}
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := templates.Overview(h.project, services, app.Summarize(services), h.monitor.CanControl())
	if err := page.Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering the overview failed", "error", err)
	}
}
