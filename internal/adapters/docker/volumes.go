package docker

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Volumes reads the project's volumes.
type Volumes struct {
	api     *client.Client
	project string
}

var (
	_ ports.VolumeSource  = (*Volumes)(nil)
	_ ports.VolumeControl = (*Volumes)(nil)
)

// NewVolumes creates the volume source from an existing client.
func NewVolumes(ctx context.Context, c *Client) *Volumes {
	cfg := config.GetConfig(ctx)
	return &Volumes{api: c.api, project: cfg.Project.Name}
}

// List returns every volume labelled for the project.
//
// A volume outlives the containers that use it — `compose down` removes the
// containers and leaves the volume — so this list is the one place a project
// that is entirely stopped still shows something.
func (v *Volumes) List(ctx context.Context) ([]domain.Volume, error) {
	response, err := v.api.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", labelProject+"="+v.project)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing volumes of project %q: %w", v.project, err)
	}

	volumes := make([]domain.Volume, 0, len(response.Volumes))
	for _, listed := range response.Volumes {
		if listed == nil {
			continue
		}
		volumes = append(volumes, toVolume(*listed))
	}
	slices.SortFunc(volumes, func(a, b domain.Volume) int { return cmp.Compare(a.Name, b.Name) })
	return volumes, nil
}

// Inspect returns one of the project's volumes.
func (v *Volumes) Inspect(ctx context.Context, name string) (domain.Volume, error) {
	inspected, err := v.api.VolumeInspect(ctx, name)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return domain.Volume{}, fmt.Errorf("%w: volume %s", ports.ErrNotFound, name)
		}
		return domain.Volume{}, fmt.Errorf("inspecting volume %s: %w", name, err)
	}

	if inspected.Labels[labelProject] != v.project {
		return domain.Volume{}, fmt.Errorf("%w: volume %s", ports.ErrNotFound, name)
	}

	return toVolume(inspected), nil
}

// toVolume maps a volume onto the domain.
func toVolume(inspected volume.Volume) domain.Volume {
	mapped := domain.Volume{
		Name:       inspected.Name,
		Driver:     inspected.Driver,
		Mountpoint: inspected.Mountpoint,
		Scope:      inspected.Scope,
		Labels:     toLabels(inspected.Labels),
		Options:    toLabels(inspected.Options),
		// Size is only returned when it was asked for, and asking makes the
		// daemon walk the filesystem. Not asking is the right default for a
		// page that reloads; -1 says "not measured" rather than "empty".
		//
		// UsedBy is left at zero here and filled in by the application layer,
		// which counts it from the containers — the reference count the daemon
		// reports comes with that same filesystem walk.
		Size: -1,
	}

	if created, err := time.Parse(time.RFC3339Nano, inspected.CreatedAt); err == nil {
		mapped.Created = created.UTC()
	}

	if usage := inspected.UsageData; usage != nil {
		mapped.Size = usage.Size
	}

	return mapped
}

// Remove deletes a volume and the data in it.
//
// Force is deliberately not passed: without it the runtime refuses a volume a
// container still refers to, which backs up the application layer's own check.
// This is the only action in the service that destroys data, so it gets the
// runtime's refusal as well as its own.
func (v *Volumes) Remove(ctx context.Context, name string) error {
	if err := v.api.VolumeRemove(ctx, name, false); err != nil {
		return fmt.Errorf("removing volume %s: %w", name, err)
	}
	return nil
}
