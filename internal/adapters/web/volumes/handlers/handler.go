// Package handlers serves the volumes pages.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	shared "github.com/k15g/compose-monitor/internal/adapters/web/shared/templates"
	"github.com/k15g/compose-monitor/internal/adapters/web/volumes/templates"
	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Handler renders the project's volumes and removes them.
type Handler struct {
	volumes *app.VolumeService
	project string
}

// New creates the handler.
func New(ctx context.Context, volumes *app.VolumeService) *Handler {
	cfg := config.GetConfig(ctx)
	return &Handler{volumes: volumes, project: cfg.Project.DisplayTitle()}
}

// Mount registers the handler's routes.
func (h *Handler) Mount(mux *http.ServeMux) {
	// Both spellings are registered rather than letting the mux redirect
	// /volumes to /volumes/: the navigation links to the bare path, and a
	// redirect on every tab click is a hop that buys nothing.
	mux.HandleFunc("GET /volumes", h.list)
	mux.HandleFunc("GET /volumes/{$}", h.list)
	mux.HandleFunc("GET /volumes/{name}", h.detail)
	mux.HandleFunc("GET /volumes/{name}/remove", h.confirmRemove)
	mux.HandleFunc("POST /volumes/{name}/remove", h.remove)
}

// list renders the project's volumes.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	found, err := h.volumes.List(ctx)
	if err != nil {
		h.unavailable(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.List(h.project, found, h.volumes.CanControl()).Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering volumes page failed", "error", err)
	}
}

// detail renders everything known about one volume.
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	found, err := h.volumes.Inspect(ctx, r.PathValue("name"))
	switch {
	case errors.Is(err, ports.ErrNotFound):
		h.notFound(w, r)
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.Detail(h.project, found, h.volumes.CanControl()).Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering volume detail failed", "error", err)
	}
}

// confirmRemove asks before removing.
func (h *Handler) confirmRemove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	found, err := h.volumes.Inspect(ctx, r.PathValue("name"))
	switch {
	case errors.Is(err, ports.ErrNotFound):
		h.notFound(w, r)
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}

	name := found.Name
	back := templ.SafeURL("/volumes")
	action := templ.SafeURL(r.URL.Path)
	consequence := "The volume and everything stored in it are deleted. This is the only action here that destroys data, and it cannot be undone."
	if found.InUse() {
		consequence = "This is still in use by a container, so it cannot be removed."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var page templ.Component
	if isFragment(r) {
		page = shared.Confirm("Remove volume", name, consequence, action, back)
	} else {
		page = shared.ConfirmPage(h.project, "volumes", "Remove volume", name, consequence, action, back)
	}
	if err := page.Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering the confirmation failed", "error", err)
	}
}

// remove deletes one volume.
func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !sameOrigin(r) {
		slog.WarnContext(ctx, "rejected a cross-origin removal", "path", r.URL.Path)
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}

	err := h.volumes.Remove(ctx, r.PathValue("name"))
	switch {
	case err == nil:
		http.Redirect(w, r, "/volumes", http.StatusSeeOther)

	case errors.Is(err, app.ErrControlDisabled):
		http.Error(w, "removal is disabled", http.StatusForbidden)

	case errors.Is(err, ports.ErrNotFound):
		http.Error(w, "no such volume in this project", http.StatusNotFound)

	case errors.Is(err, app.ErrInUse):
		http.Error(w, err.Error(), http.StatusConflict)

	default:
		slog.ErrorContext(ctx, "removing volume failed", "error", err)
		http.Error(w, "the removal failed", http.StatusBadGateway)
	}
}

// notFound renders the page for something this project does not own.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if err := shared.NotFound(h.project, "volumes", "volume").Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering not-found page failed", "error", err)
	}
}

// unavailable renders the page that explains an unreadable runtime.
func (h *Handler) unavailable(w http.ResponseWriter, r *http.Request, cause error) {
	ctx := r.Context()
	slog.ErrorContext(ctx, "reading volumes for page failed", "error", cause)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	if err := shared.Unavailable(h.project, "volumes", cause.Error()).Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering unavailable page failed", "error", err)
	}
}

// isFragment reports whether the caller wants just the piece rather than a
// page around it. htmx sets this header on every request it makes.
func isFragment(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// sameOrigin reports whether a state-changing request came from this site. It
// reads Sec-Fetch-Site, which the browser sets and a page cannot forge.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}
