package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Reconnection bounds for the event stream. The daemon restarting, or the
// socket going away with it, is ordinary rather than exceptional, so the
// stream retries forever instead of giving up.
const (
	reconnectMinDelay = 1 * time.Second
	reconnectMaxDelay = 30 * time.Second
)

// stopTimeout is how long a container is given to exit on its own before the
// runtime kills it. It matches the runtime's own default rather than picking a
// number, so a stop from the page behaves like a stop from the CLI.
const stopTimeout = 10 * time.Second

// Client reads a compose project's containers from a container runtime.
//
// It implements ports.ContainerSource.
type Client struct {
	api     *client.Client
	project string
}

// Compile-time check that the adapter satisfies the port it exists for.
var (
	_ ports.ContainerSource  = (*Client)(nil)
	_ ports.ContainerControl = (*Client)(nil)
)

// New creates a client for the runtime named in the configuration.
//
// It distinguishes two kinds of failure, because they deserve different
// answers. A host that cannot be parsed is a configuration error that no
// amount of waiting will fix, so it is returned and the process stops. A host
// that is merely unreachable — the socket not mounted, or mounted but not
// readable by this user — is not: the service starts, says so on the page, and
// picks the runtime up by itself when it becomes reachable.
//
// The alternative, refusing to start, turns a fixable mistake into a restart
// loop whose only symptom is a log line. A service whose whole job is
// reporting on containers should be able to report why it cannot.
func New(ctx context.Context) (*Client, error) {
	cfg := config.GetConfig(ctx)

	api, err := client.NewClientWithOpts(
		client.WithHost(cfg.Docker.Host),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: creating client for %s: %w",
			ports.ErrSourceUnavailable, cfg.Docker.Host, err)
	}

	// The ping is a diagnostic, not a gate. Reaching the runtime at startup is
	// the normal case, and saying plainly that it failed — once, at startup —
	// beats leaving the first symptom to whoever opens the page.
	if _, err := api.Ping(ctx); err != nil {
		slog.WarnContext(ctx, "container runtime is not reachable; the page will report it until it is",
			"host", cfg.Docker.Host, "error", err)
	}

	return &Client{api: api, project: cfg.Project.Name}, nil
}

// Close releases the connection to the runtime.
func (c *Client) Close() error {
	return c.api.Close()
}

// List returns every container of the project, running or not. All is what
// makes an offline service visible at all: without it a stopped container is
// indistinguishable from one that never existed.
func (c *Client) List(ctx context.Context) ([]domain.Service, error) {
	summaries, err := c.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelProject+"="+c.project)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers of project %q: %w", c.project, err)
	}

	services := make([]domain.Service, 0, len(summaries))
	for _, summary := range summaries {
		services = append(services, toService(summary))
	}
	return services, nil
}

// Start starts a container.
//
// As with Stop, whether this container is one the service may touch is decided
// in the application layer; this does what it is told.
func (c *Client) Start(ctx context.Context, containerID string) error {
	if err := c.api.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container %s: %w", containerID, err)
	}
	return nil
}

// Stop stops a container.
//
// The membership check — that this container belongs to the watched project —
// is deliberately not here. It is a rule about what the service is allowed to
// touch, so it lives in the application layer with the rest of them, and this
// method does what it is told.
func (c *Client) Stop(ctx context.Context, containerID string) error {
	timeout := int(stopTimeout.Seconds())
	if err := c.api.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("stopping container %s: %w", containerID, err)
	}
	return nil
}

// Remove deletes a container.
//
// Two options are deliberately left off, and both are load-bearing:
//
//   - RemoveVolumes. This deletes the container, never its data. A volume
//     outliving its container is the whole reason volumes exist, and a button
//     on a web page is not where someone should discover otherwise.
//   - Force. Without it the runtime refuses to remove a running container,
//     which backs up the application layer's own check rather than relying on
//     it alone — the two requests are not atomic, and a container can start
//     between them.
func (c *Client) Remove(ctx context.Context, containerID string) error {
	if err := c.api.ContainerRemove(ctx, containerID, container.RemoveOptions{}); err != nil {
		return fmt.Errorf("removing container %s: %w", containerID, err)
	}
	return nil
}

// Watch signals whenever the project's containers may have changed.
//
// The channel is buffered and written to without blocking: the signal carries
// no information, so a second one that arrives before the first is read adds
// nothing and is dropped.
func (c *Client) Watch(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)

	go func() {
		defer close(out)

		delay := reconnectMinDelay
		for {
			err := c.stream(ctx, out)
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(ctx, "container event stream interrupted, reconnecting",
				"error", err, "delay", delay)

			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}

			delay = min(delay*2, reconnectMaxDelay)

			// A stream that ran long enough to be worth reconnecting to
			// quickly gets the short delay back; one that fails immediately
			// keeps backing off.
			if err == nil {
				delay = reconnectMinDelay
			}
		}
	}()

	return out
}

// stream follows the runtime's event stream until it fails or ctx is done.
func (c *Client) stream(ctx context.Context, out chan<- struct{}) error {
	messages, errs := c.api.Events(ctx, events.ListOptions{Filters: c.eventFilters()})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-errs:
			if err == nil {
				return errors.New("event stream closed")
			}
			return fmt.Errorf("reading event stream: %w", err)

		case _, ok := <-messages:
			if !ok {
				return errors.New("event stream closed")
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}
}

// eventFilters narrows the stream to this project's containers, and to the
// actions that can change what the page shows.
//
// Without the action filter the stream also carries exec, attach and top
// events, which are frequent on a busy container and can never change a row.
func (c *Client) eventFilters() filters.Args {
	args := filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)),
		filters.Arg("label", labelProject+"="+c.project),
	)
	for _, action := range []string{
		"create", "start", "stop", "kill", "die", "destroy",
		"pause", "unpause", "restart", "rename", "update", "health_status",
	} {
		args.Add("event", action)
	}
	return args
}
