// Package handlers serves the services page and the actions on it.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/k15g/compose-monitor/internal/adapters/web/services/templates"
	shared "github.com/k15g/compose-monitor/internal/adapters/web/shared/templates"
	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Handler renders the project's services and acts on them.
type Handler struct {
	monitor *app.MonitorService
	project string
}

// New creates the handler. It takes the application service, not the ports it
// is built on: the adapter's job is to render what the application decides.
func New(ctx context.Context, monitor *app.MonitorService) *Handler {
	cfg := config.GetConfig(ctx)
	return &Handler{
		monitor: monitor,
		project: cfg.Project.DisplayTitle(),
	}
}

// Mount registers the handler's routes.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /services", h.list)
	mux.HandleFunc("GET /services/{$}", h.list)
	mux.HandleFunc("GET /services/{id}", h.detail)
	mux.HandleFunc("GET /services/{id}/log", h.log)
	// The confirmation is a page of its own, so a browser with no script
	// navigates to it and gets the same question the modal asks.
	mux.HandleFunc("GET /services/{id}/remove", h.confirmRemove)
	mux.HandleFunc("POST /services/{id}/start", h.action(h.monitor.Start, backWhereItWas))
	mux.HandleFunc("POST /services/{id}/stop", h.action(h.monitor.Stop, backWhereItWas))
	// A removed container has no detail page left, so a removal always lands
	// on the list rather than on a page that would now be a 404.
	mux.HandleFunc("POST /services/{id}/remove", h.action(h.monitor.Remove, backToTheList))
}

// list renders the full page.
//
// It reads the project rather than serving the watcher's last observation, so
// a page load is correct even when the watch has stopped delivering. The event
// stream takes over from there.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	services, err := h.monitor.List(ctx)
	if err != nil {
		h.unavailable(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := templates.List(h.project, services, app.Summarize(services), h.monitor.CanControl())
	if err := page.Render(ctx, w); err != nil {
		// The response has already begun, so there is no status left to send.
		slog.ErrorContext(ctx, "rendering services page failed", "error", err)
	}
}

// detail renders everything known about one service.
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	detail, err := h.monitor.Inspect(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, ports.ErrNotFound):
		h.notFound(w, r, "service")
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := templates.Detail(h.project, detail, h.monitor.CanControl())
	if err := page.Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering service detail failed", "error", err)
	}
}

// log serves the container's output.
//
// The detail page renders without it and fetches it when the panel is opened,
// because reading a log costs a request to the daemon and can be large, and
// most visits are not about the log. A browser with no script asks for this
// URL directly and gets a page instead of the fragment.
func (h *Handler) log(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	detail, err := h.monitor.Inspect(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, ports.ErrNotFound):
		h.notFound(w, r, "service")
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}

	tail := app.ClampLogTail(intParam(r, "tail"))
	logs, logsErr := h.monitor.Logs(ctx, detail.ContainerID, tail)
	failure := ""
	if logsErr != nil {
		// A container that has never run has no log. Saying so in the panel
		// beats failing the request the panel made.
		slog.WarnContext(ctx, "reading logs failed", "container", detail.ContainerID, "error", logsErr)
		failure = logsErr.Error()
		logs = domain.Logs{Tail: tail}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var page templ.Component
	if isFragment(r) {
		page = templates.LogBody(detail.ContainerID, logs, failure)
	} else {
		page = templates.LogPage(h.project, detail, logs, failure)
	}
	if err := page.Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering the log failed", "error", err)
	}
}

// confirmRemove asks before removing a container.
func (h *Handler) confirmRemove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	detail, err := h.monitor.Inspect(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, ports.ErrNotFound):
		h.notFound(w, r, "service")
		return
	case err != nil:
		h.unavailable(w, r, err)
		return
	}

	back := templ.SafeURL("/services/" + detail.ContainerID)
	action := templ.SafeURL("/services/" + detail.ContainerID + "/remove")
	consequence := "The container is deleted. Its volumes are left alone, and Compose will create a new container the next time this service is brought up."
	if detail.Online() {
		consequence = "This container is still running, so it cannot be removed. Stop it first."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var page templ.Component
	if isFragment(r) {
		page = shared.Confirm("Remove container", detail.ContainerName, consequence, action, back)
	} else {
		page = shared.ConfirmPage(h.project, "services", "Remove container", detail.ContainerName, consequence, action, back)
	}
	if err := page.Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering the confirmation failed", "error", err)
	}
}

// isFragment reports whether the caller wants just the piece rather than a
// page around it. htmx sets this header on every request it makes.
func isFragment(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// intParam reads a numeric query parameter, treating anything unreadable as
// absent — a bad number in a URL should fall back to the default, not fail.
func intParam(r *http.Request, name string) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return value
}

// notFound renders the page for a service this project does not own.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, what string) {
	ctx := r.Context()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if err := shared.NotFound(h.project, "services", what).Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering not-found page failed", "error", err)
	}
}

// action turns one of the application's start/stop methods into a handler.
//
// The row the browser is looking at is not updated here. The change is
// announced through the event stream like any other, so every open page
// converges on the same state — including the ones belonging to someone else.
func (h *Handler) action(act func(context.Context, string) error, back func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if !sameOrigin(r) {
			// These endpoints change state and the page has no authentication,
			// so a form on another site posting here would otherwise work. See
			// sameOrigin for what this does and does not cover.
			slog.WarnContext(ctx, "rejected a cross-origin request",
				"path", r.URL.Path,
				"origin", r.Header.Get("Origin"), "site", r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}

		err := act(ctx, r.PathValue("id"))
		switch {
		case err == nil:
			h.acted(w, r, back)

		case errors.Is(err, app.ErrControlDisabled):
			http.Error(w, "container control is disabled", http.StatusForbidden)

		case errors.Is(err, app.ErrNotInProject):
			// Deliberately the same answer as a container that does not exist:
			// the service should not confirm what runs outside its project.
			http.Error(w, "no such service in this project", http.StatusNotFound)

		case errors.Is(err, app.ErrNotRunning):
			http.Error(w, "service is not running", http.StatusConflict)

		case errors.Is(err, app.ErrAlreadyRunning):
			http.Error(w, "service is already running", http.StatusConflict)

		case errors.Is(err, app.ErrStillRunning):
			http.Error(w, "stop the service before removing it", http.StatusConflict)

		default:
			slog.ErrorContext(ctx, "acting on service failed", "path", r.URL.Path, "error", err)
			http.Error(w, "the action failed", http.StatusBadGateway)
		}
	}
}

// acted answers a successful start or stop.
//
// A plain form post gets a redirect, so the page works with no JavaScript at
// all. The page's own script sets the header below and gets no content back,
// because it does not need any: the row it is looking at is about to be
// replaced by the event the action produced.
func (h *Handler) acted(w http.ResponseWriter, r *http.Request, back func(*http.Request) string) {
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, back(r), http.StatusSeeOther)
}

// unavailable renders the page that explains an unreadable runtime.
func (h *Handler) unavailable(w http.ResponseWriter, r *http.Request, cause error) {
	ctx := r.Context()
	slog.ErrorContext(ctx, "reading services for page failed", "error", cause)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	if err := shared.Unavailable(h.project, "services", cause.Error()).Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering unavailable page failed", "error", err)
	}
}

// backToTheList sends a plain form post to the services list, for an action
// after which the page it came from may no longer exist.
func backToTheList(*http.Request) string {
	return "/"
}

// backWhereItWas sends a plain form post back to the page the form was on, so
// acting from a detail page stays on it rather than jumping to the list.
//
// Only the path and query of the referrer are used, never its host. Taking the
// header whole would make this an open redirect — the header is set by the
// browser, but a redirect to wherever it points is not something this endpoint
// should ever do.
func backWhereItWas(r *http.Request) string {
	referrer, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || referrer.Path == "" || !strings.HasPrefix(referrer.Path, "/") {
		return "/"
	}

	target := url.URL{Path: referrer.Path, RawQuery: referrer.RawQuery}
	return target.String()
}

// sameOrigin reports whether a state-changing request came from this site.
//
// It reads Sec-Fetch-Site, which the browser sets and a page cannot forge. A
// cross-site form post arrives as "cross-site" and is refused; "same-origin"
// and "none" (a direct navigation) are allowed.
//
// A client that sends no such header — curl, an old browser — is allowed
// through. The header is a defence against a *browser* being used against the
// user by another site, and something making requests directly was never
// subject to that in the first place.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}
