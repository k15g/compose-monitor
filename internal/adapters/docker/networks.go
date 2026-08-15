package docker

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Networks reads the project's networks.
//
// It is a separate type from Client, with its own connection, because the two
// are separate ports. Sharing the underlying API client keeps that to one
// connection all the same.
type Networks struct {
	api     *client.Client
	project string
}

var (
	_ ports.NetworkSource  = (*Networks)(nil)
	_ ports.NetworkControl = (*Networks)(nil)
)

// NewNetworks creates the network source from an existing client, so the whole
// service talks to the runtime over one connection.
func NewNetworks(ctx context.Context, c *Client) *Networks {
	cfg := config.GetConfig(ctx)
	return &Networks{api: c.api, project: cfg.Project.Name}
}

// List returns every network labelled for the project.
//
// Compose labels the networks it creates just as it labels containers, so the
// same filter finds them. A network declared `external: true` is not one
// Compose created and therefore carries no label — it will not appear here,
// which is correct: it belongs to whoever made it.
func (n *Networks) List(ctx context.Context) ([]domain.Network, error) {
	summaries, err := n.api.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", labelProject+"="+n.project)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing networks of project %q: %w", n.project, err)
	}

	networks := make([]domain.Network, 0, len(summaries))
	for _, summary := range summaries {
		networks = append(networks, toNetwork(summary))
	}
	slices.SortFunc(networks, func(a, b domain.Network) int { return cmp.Compare(a.Name, b.Name) })
	return networks, nil
}

// Inspect returns one of the project's networks, with the containers attached
// to it. The project label is checked here because inspect, unlike list, takes
// any id the daemon knows.
func (n *Networks) Inspect(ctx context.Context, networkID string) (domain.Network, error) {
	inspected, err := n.api.NetworkInspect(ctx, networkID, network.InspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return domain.Network{}, fmt.Errorf("%w: network %s", ports.ErrNotFound, networkID)
		}
		return domain.Network{}, fmt.Errorf("inspecting network %s: %w", networkID, err)
	}

	if inspected.Labels[labelProject] != n.project {
		return domain.Network{}, fmt.Errorf("%w: network %s", ports.ErrNotFound, networkID)
	}

	return toNetwork(inspected), nil
}

// toNetwork maps a network onto the domain.
func toNetwork(inspected network.Inspect) domain.Network {
	mapped := domain.Network{
		ID:         inspected.ID,
		Name:       inspected.Name,
		Driver:     inspected.Driver,
		Scope:      inspected.Scope,
		Created:    inspected.Created.UTC(),
		Internal:   inspected.Internal,
		Attachable: inspected.Attachable,
		IPv6:       inspected.EnableIPv6,
		Labels:     toLabels(inspected.Labels),
		Options:    toLabels(inspected.Options),
	}

	for _, config := range inspected.IPAM.Config {
		mapped.Subnets = append(mapped.Subnets, domain.Subnet{
			Range:   config.Subnet,
			Gateway: config.Gateway,
		})
	}
	slices.SortFunc(mapped.Subnets, func(a, b domain.Subnet) int { return cmp.Compare(a.Range, b.Range) })

	for id, endpoint := range inspected.Containers {
		mapped.Members = append(mapped.Members, domain.NetworkMember{
			ContainerID: id,
			Name:        endpoint.Name,
			IPv4Address: endpoint.IPv4Address,
			IPv6Address: endpoint.IPv6Address,
			MacAddress:  endpoint.MacAddress,
		})
	}
	slices.SortFunc(mapped.Members, func(a, b domain.NetworkMember) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ContainerID, b.ContainerID))
	})

	return mapped
}

// Remove deletes a network.
//
// Whether it is one of the project's, and whether anything is still on it, are
// decided in the application layer. The runtime refuses a network with active
// endpoints regardless, which is the backstop for the gap between the check
// and the call.
func (n *Networks) Remove(ctx context.Context, networkID string) error {
	if err := n.api.NetworkRemove(ctx, networkID); err != nil {
		return fmt.Errorf("removing network %s: %w", networkID, err)
	}
	return nil
}
