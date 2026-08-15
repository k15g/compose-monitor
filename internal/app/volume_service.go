package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// VolumeService reports on the project's volumes and removes them.
type VolumeService struct {
	source     ports.VolumeSource
	containers ports.ContainerSource
	control    ports.VolumeControl
}

// NewVolumeService creates the service. control may be nil, in which case
// removal is refused and no button is drawn for it.
func NewVolumeService(
	_ context.Context,
	source ports.VolumeSource,
	containers ports.ContainerSource,
	control ports.VolumeControl,
) *VolumeService {
	return &VolumeService{source: source, containers: containers, control: control}
}

// CanControl reports whether this deployment may remove volumes.
func (s *VolumeService) CanControl() bool {
	return s.control != nil
}

// List returns the project's volumes, each carrying how many containers mount
// it.
func (s *VolumeService) List(ctx context.Context) ([]domain.Volume, error) {
	volumes, err := s.source.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	return s.withUsage(ctx, volumes), nil
}

// Inspect returns one of the project's volumes, with its usage.
func (s *VolumeService) Inspect(ctx context.Context, name string) (domain.Volume, error) {
	found, err := s.source.Inspect(ctx, name)
	if err != nil {
		return domain.Volume{}, fmt.Errorf("inspecting volume: %w", err)
	}
	return s.withUsage(ctx, []domain.Volume{found})[0], nil
}

// withUsage fills in how many containers mount each volume.
//
// A failure to count is logged and ignored: it decides whether a remove button
// is drawn and nothing else, and a page without the button is still worth
// serving.
func (s *VolumeService) withUsage(ctx context.Context, volumes []domain.Volume) []domain.Volume {
	usage, err := s.containers.Usage(ctx)
	if err != nil {
		slog.WarnContext(ctx, "counting volume usage failed; removal will not be offered", "error", err)
		return volumes
	}

	for i := range volumes {
		volumes[i].UsedBy = usage.Volumes[volumes[i].Name]
	}
	return volumes
}

// Remove deletes one of the project's volumes, and the data in it.
//
// This is the only action in the service that destroys something a container
// cannot recreate, so it refuses a volume anything still mounts — including a
// stopped container, which is what the runtime refuses on too.
func (s *VolumeService) Remove(ctx context.Context, name string) error {
	if s.control == nil {
		return ErrControlDisabled
	}

	found, err := s.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if found.InUse() {
		return fmt.Errorf("%w: %s is mounted by %d container(s)", ErrInUse, found.Name, found.UsedBy)
	}

	if err := s.control.Remove(ctx, name); err != nil {
		return fmt.Errorf("removing volume %s: %w", found.Name, err)
	}
	slog.InfoContext(ctx, "removed volume", "volume", found.Name)
	return nil
}
