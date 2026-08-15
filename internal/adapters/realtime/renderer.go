package realtime

import (
	"bytes"
	"context"
	"fmt"

	shared "github.com/k15g/compose-monitor/internal/adapters/web/shared/templates"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
)

// View is how a subscriber wants its services drawn. A page says which when it
// opens the stream, because only the page knows which page it is.
type View string

const (
	// ViewServices is the services page, which carries every state and so
	// leads each entry with it.
	ViewServices View = "services"
	// ViewOverview is the front page, where everything shown is running and a
	// state on every entry would be one repeated word.
	ViewOverview View = "overview"
)

// ParseView reads the view a subscriber asked for. A client that says nothing
// gets the services page's shape, which is the one that leaves nothing out.
func ParseView(value string) View {
	if View(value) == ViewOverview {
		return ViewOverview
	}
	return ViewServices
}

// Renderer turns a service into the markup a client can drop into its page.
type Renderer struct {
	// controllable is whether rows carry action controls. It is fixed for the
	// life of the process, and is read once so a row pushed over the event
	// stream matches the one the page rendered.
	controllable bool
}

// NewRenderer creates a renderer.
func NewRenderer(ctx context.Context) *Renderer {
	cfg := config.GetConfig(ctx)
	return &Renderer{controllable: cfg.Control.Enabled}
}

// Render renders one service in the shape the given view wants, using the same
// components the full page render uses — so a row that arrived over the stream
// cannot differ from the same row drawn by a reload.
func (r *Renderer) Render(ctx context.Context, service domain.Service, view View) (string, error) {
	component := shared.ServiceItem(service, r.controllable, view == ViewServices)

	var buf bytes.Buffer
	if err := component.Render(ctx, &buf); err != nil {
		return "", fmt.Errorf("rendering %s for service %q: %w", view, service.Name, err)
	}
	return buf.String(), nil
}
