// Package web is the HTTP adapter. The root Adapter composes the handlers
// that own routes and delegates mounting to them.
package web

import (
	"context"
	"fmt"
	"net/http"

	health "github.com/k15g/compose-monitor/internal/adapters/web/health/handlers"
	networks "github.com/k15g/compose-monitor/internal/adapters/web/networks/handlers"
	overview "github.com/k15g/compose-monitor/internal/adapters/web/overview/handlers"
	services "github.com/k15g/compose-monitor/internal/adapters/web/services/handlers"
	static "github.com/k15g/compose-monitor/internal/adapters/web/static/handlers"
	volumes "github.com/k15g/compose-monitor/internal/adapters/web/volumes/handlers"
	"github.com/k15g/compose-monitor/internal/app"
)

// Adapter composes the web handlers.
type Adapter struct {
	overview *overview.Handler
	services *services.Handler
	networks *networks.Handler
	volumes  *volumes.Handler
	health   *health.Handler
	static   *static.Handler
}

// New creates the adapter and its handlers.
//
// Each handler takes the application service it renders, never the ports
// underneath — the adapter's job is to present what the application decides.
func New(
	ctx context.Context,
	monitor *app.MonitorService,
	networkService *app.NetworkService,
	volumeService *app.VolumeService,
) (*Adapter, error) {
	staticHandler, err := static.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating static handler: %w", err)
	}

	return &Adapter{
		overview: overview.New(ctx, monitor, networkService, volumeService),
		services: services.New(ctx, monitor),
		networks: networks.New(ctx, networkService),
		volumes:  volumes.New(ctx, volumeService),
		health:   health.New(ctx, monitor),
		static:   staticHandler,
	}, nil
}

// Mount registers every route the adapter owns.
func (a *Adapter) Mount(mux *http.ServeMux) {
	a.overview.Mount(mux)
	a.services.Mount(mux)
	a.networks.Mount(mux)
	a.volumes.Mount(mux)
	a.health.Mount(mux)
	a.static.Mount(mux)
}
