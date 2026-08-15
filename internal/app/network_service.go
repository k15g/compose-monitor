package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// NetworkService reports on the project's networks and removes them.
//
// Networks change rarely — a project creates them once and they outlast every
// container — so unlike services they are read on request rather than watched.
type NetworkService struct {
	source     ports.NetworkSource
	containers ports.ContainerSource
	control    ports.NetworkControl
}

// NewNetworkService creates the service. control may be nil, in which case
// removal is refused and no button is drawn for it.
func NewNetworkService(
	_ context.Context,
	source ports.NetworkSource,
	containers ports.ContainerSource,
	control ports.NetworkControl,
) *NetworkService {
	return &NetworkService{source: source, containers: containers, control: control}
}

// CanControl reports whether this deployment may remove networks.
func (s *NetworkService) CanControl() bool {
	return s.control != nil
}

// List returns the project's networks, each carrying how many containers are
// on it.
func (s *NetworkService) List(ctx context.Context) ([]domain.Network, error) {
	networks, err := s.source.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}

	usage, err := s.containers.Usage(ctx)
	if err != nil {
		// Usage decides whether a remove button is drawn, and nothing else. A
		// page without it is still worth serving; one that refuses to load
		// because a second call failed is not.
		slog.WarnContext(ctx, "counting network usage failed; removal will not be offered", "error", err)
		return networks, nil
	}

	for i := range networks {
		networks[i].UsedBy = usage.Networks[networks[i].Name]
	}
	return networks, nil
}

// Inspect returns one of the project's networks, with its usage.
func (s *NetworkService) Inspect(ctx context.Context, networkID string) (domain.Network, error) {
	found, err := s.source.Inspect(ctx, networkID)
	if err != nil {
		return domain.Network{}, fmt.Errorf("inspecting network: %w", err)
	}

	if usage, err := s.containers.Usage(ctx); err == nil {
		found.UsedBy = usage.Networks[found.Name]
	} else {
		slog.WarnContext(ctx, "counting network usage failed; removal will not be offered", "error", err)
	}

	// A network reports its own members on inspect, and that is the better
	// number when there is one: it is what the runtime will judge the removal
	// on. Usage only fills the gap for a network with none.
	if len(found.Members) > found.UsedBy {
		found.UsedBy = len(found.Members)
	}
	return found, nil
}

// Remove deletes one of the project's networks.
//
// It refuses a network anything is still attached to, and refuses one that is
// not the project's — the same rule as everywhere else, that this service only
// touches what it watches.
func (s *NetworkService) Remove(ctx context.Context, networkID string) error {
	if s.control == nil {
		return ErrControlDisabled
	}

	found, err := s.Inspect(ctx, networkID)
	if err != nil {
		return err
	}
	if found.InUse() {
		return fmt.Errorf("%w: %s has %d container(s) on it", ErrInUse, found.Name, found.UsedBy)
	}

	if err := s.control.Remove(ctx, networkID); err != nil {
		return fmt.Errorf("removing network %s: %w", found.Name, err)
	}
	slog.InfoContext(ctx, "removed network", "network", found.Name, "id", networkID)
	return nil
}
