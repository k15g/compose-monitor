package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"

	"github.com/k15g/compose-monitor/internal/domain"
)

// Usage counts how many of the project's containers refer to each network and
// each volume.
//
// It comes from one listing of the containers rather than from the networks
// and volumes themselves: listing networks does not report their members, and
// a volume's reference count is only returned when the daemon is asked to walk
// the filesystem measuring sizes. The containers know both, and they are
// already being listed for everything else.
//
// Stopped containers count. They are what the runtime itself refuses a removal
// on, so counting only running ones would offer a button that fails.
func (c *Client) Usage(ctx context.Context) (domain.ResourceUsage, error) {
	summaries, err := c.api.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelProject+"="+c.project)),
	})
	if err != nil {
		return domain.ResourceUsage{}, fmt.Errorf("listing containers of project %q: %w", c.project, err)
	}

	usage := domain.ResourceUsage{
		Networks: map[string]int{},
		Volumes:  map[string]int{},
	}

	for _, summary := range summaries {
		if summary.NetworkSettings != nil {
			for name := range summary.NetworkSettings.Networks {
				usage.Networks[name]++
			}
		}
		for _, mounted := range summary.Mounts {
			// Only named volumes are counted. A bind mount has no name and is
			// not a volume the page can offer to remove.
			if mounted.Type == mount.TypeVolume && mounted.Name != "" {
				usage.Volumes[mounted.Name]++
			}
		}
	}

	return usage, nil
}
