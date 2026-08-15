// Command server serves a page listing the Docker services of one compose
// project, and keeps it current over Server-Sent Events.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/k15g/compose-monitor/internal/adapters/docker"
	"github.com/k15g/compose-monitor/internal/adapters/realtime"
	"github.com/k15g/compose-monitor/internal/adapters/web"
	"github.com/k15g/compose-monitor/internal/app"
	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/ports"
)

func main() {
	// The image has no shell and no curl, so the healthcheck is this binary
	// probing the server it would otherwise be running. One file in the image
	// stays one file.
	healthcheck := flag.Bool("healthcheck", false, "probe the running server and exit")
	flag.Parse()

	if *healthcheck {
		if err := probe(); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// probe asks the running server whether it is serving.
//
// It dials the loopback address rather than the configured one, because the
// configured host is normally 0.0.0.0 — an address to listen on, not one to
// connect to.
func probe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Http.Port)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz returned %s", response.Status)
	}
	return nil
}

// probeTimeout bounds the healthcheck. It is shorter than the interval Docker
// probes on, so a hung probe cannot pile up.
const probeTimeout = 3 * time.Second

// run is main's body, split out so every defer runs before the process exits —
// os.Exit in main would skip them.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	setupLogging(cfg)

	// The configuration is injected once, here. Every constructor below reads
	// what it needs from the context rather than being handed settings.
	ctx := config.WithConfig(context.Background(), cfg)

	// Cancelled on SIGINT/SIGTERM. It is what stops the watch loop and closes
	// the open event streams.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. Infrastructure the application layer depends on.
	source, err := docker.New(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	hub := realtime.NewHub()
	defer func() { _ = hub.Close() }()

	renderer := realtime.NewRenderer(ctx)

	// Acting on containers is opt-out, and opting out means not holding the
	// handle at all rather than remembering not to use it.
	var control ports.ContainerControl
	if cfg.Control.Enabled {
		control = source
	} else {
		slog.InfoContext(ctx, "container control is disabled; the page is read-only")
	}

	// 2. The application layer.
	monitor := app.NewMonitorService(ctx, source, control, hub)

	// Networks and volumes share the container client's connection: they are
	// separate ports because they are separate concerns, not because they need
	// separate sockets.
	networks := docker.NewNetworks(ctx, source)
	volumes := docker.NewVolumes(ctx, source)

	var networkControl ports.NetworkControl
	var volumeControl ports.VolumeControl
	if cfg.Control.Enabled {
		networkControl = networks
		volumeControl = volumes
	}

	networkService := app.NewNetworkService(ctx, networks, source, networkControl)
	volumeService := app.NewVolumeService(ctx, volumes, source, volumeControl)

	// 3. Adapters that serve HTTP, which receive the service.
	webAdapter, err := web.New(ctx, monitor, networkService, volumeService)
	if err != nil {
		return err
	}
	streamAdapter := realtime.NewAdapter(ctx, hub, renderer, monitor)

	mux := http.NewServeMux()
	webAdapter.Mount(mux)
	streamAdapter.Mount(mux)

	server := &http.Server{
		Addr:              cfg.Http.Addr(),
		Handler:           mux,
		ReadHeaderTimeout: cfg.Http.ReadHeaderTimeout,
		IdleTimeout:       cfg.Http.IdleTimeout,
		// WriteTimeout is deliberately unset. Go applies it as a deadline on
		// the connection once the headers are read, and an event stream stays
		// open for as long as the tab does. There is no way to exempt one
		// route from it, so a server that holds streams open cannot set it.
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := monitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "watch loop stopped", "error", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		shutdown(server, hub, cfg)
	}()

	slog.InfoContext(ctx, "listening",
		"addr", cfg.Http.Addr(), "project", cfg.Project.Name, "docker", cfg.Docker.Host)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		stop()
		wg.Wait()
		return err
	}

	wg.Wait()
	slog.Info("stopped")
	return nil
}

// shutdown closes the hub before draining the server.
//
// The order matters. Closing the hub ends every open event stream, so the
// handlers return and Shutdown has nothing left to wait for; draining first
// would block until every stream's timeout expired.
func shutdown(server *http.Server, hub *realtime.Hub, cfg *config.Config) {
	slog.Info("shutting down", "timeout", cfg.Http.ShutdownTimeout)

	_ = hub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Http.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
}

// setupLogging installs the configured handler as the default logger.
func setupLogging(cfg *config.Config) {
	opts := &slog.HandlerOptions{Level: cfg.Log.Level.SlogLevel()}

	var handler slog.Handler
	if cfg.Log.Format == config.LogFormatJSON {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}
