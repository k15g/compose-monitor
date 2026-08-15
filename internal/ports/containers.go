// Package ports defines the interfaces the application layer depends on. It
// holds interfaces only: every implementation lives in internal/adapters.
package ports

import (
	"context"

	"github.com/k15g/compose-monitor/internal/domain"
)

// ContainerSource is the runtime the services are read from.
//
// It reports state and nothing more: deciding what counts as online, what
// changed since the last look, and what to show is the application layer's
// job.
type ContainerSource interface {
	// List returns every service of the configured project, whatever state it
	// is in — a stopped container is a service that is offline, not a service
	// that is absent.
	List(ctx context.Context) ([]domain.Service, error)

	// Inspect returns everything known about one of the project's containers.
	// It returns ErrNotFound when the id names nothing the project owns —
	// including a container that exists but belongs elsewhere, since the
	// caller must not be able to tell those apart.
	Inspect(ctx context.Context, containerID string) (domain.ServiceDetail, error)

	// Logs returns the last `tail` lines of one of the project's containers,
	// oldest first. It returns ErrNotFound on the same terms as Inspect.
	Logs(ctx context.Context, containerID string, tail int) (domain.Logs, error)

	// Usage counts how many of the project's containers refer to each network
	// and each volume. It is on this interface rather than the network and
	// volume ones because the containers are where the association is
	// recorded — a network listing does not say what is attached to it.
	Usage(ctx context.Context) (domain.ResourceUsage, error)

	// Watch returns a channel that receives a value whenever something about
	// the project's containers may have changed. The value carries no
	// information: the receiver re-reads with List.
	//
	// The channel is closed when ctx is done. Recovering from a dropped
	// connection to the runtime is the implementation's problem, so a
	// receiver never sees an error here.
	Watch(ctx context.Context) <-chan struct{}
}

// ContainerControl acts on containers, as opposed to reading them.
//
// It is a separate interface from ContainerSource on purpose. Reading and
// writing are separate privileges on the runtime — a deployment can be given
// one without the other — and keeping them apart means a build that only
// watches never holds a handle that can stop anything.
type ContainerControl interface {
	// Start starts the container. Starting one that is already running
	// succeeds.
	Start(ctx context.Context, containerID string) error

	// Stop stops the container, giving it the grace period the runtime is
	// configured with before it is killed. Stopping a container that is
	// already stopped succeeds.
	Stop(ctx context.Context, containerID string) error

	// Remove deletes the container. It does not delete the volumes the
	// container used, and it does not remove a container that is running.
	Remove(ctx context.Context, containerID string) error
}

// NetworkControl acts on networks, as opposed to reading them. It is separate
// from NetworkSource for the same reason ContainerControl is separate from
// ContainerSource: reading and destroying are different privileges.
type NetworkControl interface {
	// Remove deletes the network. The runtime refuses one that still has
	// active endpoints.
	Remove(ctx context.Context, networkID string) error
}

// VolumeControl acts on volumes.
type VolumeControl interface {
	// Remove deletes the volume and the data in it. The runtime refuses one
	// that a container still refers to.
	Remove(ctx context.Context, name string) error
}

// NetworkSource reads the project's networks.
type NetworkSource interface {
	// List returns every network labelled for the project.
	List(ctx context.Context) ([]domain.Network, error)

	// Inspect returns one of them, or ErrNotFound.
	Inspect(ctx context.Context, networkID string) (domain.Network, error)
}

// VolumeSource reads the project's volumes.
type VolumeSource interface {
	// List returns every volume labelled for the project.
	List(ctx context.Context) ([]domain.Volume, error)

	// Inspect returns one of them, or ErrNotFound.
	Inspect(ctx context.Context, name string) (domain.Volume, error)
}
