// Package handlers serves the health endpoint.
package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/k15g/compose-monitor/internal/app"
)

// Handler answers health probes.
type Handler struct {
	monitor *app.MonitorService
}

// New creates the handler.
func New(_ context.Context, monitor *app.MonitorService) *Handler {
	return &Handler{monitor: monitor}
}

// Mount registers the health route.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.health)
}

// health reports whether the process is serving.
//
// It deliberately does not fail when the container runtime is unreachable,
// even though it says so in the body. That is a state the service is designed
// to sit in and recover from by itself, so reporting it as unhealthy would
// have an orchestrator restart a process that is working correctly and waiting
// for something outside it to be fixed — which is the restart loop this
// project has already been bitten by once.
//
// What it does catch is the process being up but unable to serve: a wedged or
// deadlocked server answers nothing, and that is the failure a restart fixes.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	status := struct {
		Status  string `json:"status"`
		Runtime string `json:"runtime"`
		Detail  string `json:"detail,omitempty"`
	}{Status: "ok", Runtime: "reachable"}

	if _, err := h.monitor.List(ctx); err != nil {
		status.Runtime = "unreachable"
		status.Detail = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(status)
}
