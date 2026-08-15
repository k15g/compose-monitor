package docker

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"

	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// Inspect returns everything known about one of the project's containers.
//
// The project label is checked here rather than trusted from the id, because
// inspect takes any id the daemon knows — unlike list, it is not scoped to the
// project by a filter, so scoping it is this method's job.
func (c *Client) Inspect(ctx context.Context, containerID string) (domain.ServiceDetail, error) {
	inspected, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return domain.ServiceDetail{}, fmt.Errorf("%w: container %s", ports.ErrNotFound, containerID)
		}
		return domain.ServiceDetail{}, fmt.Errorf("inspecting container %s: %w", containerID, err)
	}

	if inspected.Config == nil || inspected.Config.Labels[labelProject] != c.project {
		return domain.ServiceDetail{}, fmt.Errorf("%w: container %s", ports.ErrNotFound, containerID)
	}

	return toDetail(inspected), nil
}

// toDetail maps an inspect response onto the domain.
func toDetail(inspected container.InspectResponse) domain.ServiceDetail {
	detail := domain.ServiceDetail{
		Service: domain.Service{
			ContainerID:   inspected.ID,
			ContainerName: strings.TrimPrefix(inspected.Name, "/"),
		},
	}

	if config := inspected.Config; config != nil {
		detail.Name = config.Labels[labelService]
		detail.Number = containerNumber(config.Labels[labelNumber])
		detail.Image = config.Image
		detail.Title = config.Labels[domain.LabelImageTitle]
		detail.Description = config.Labels[domain.LabelImageDescription]
		detail.URL = serviceURL(config.Labels)
		detail.WorkingDir = config.WorkingDir
		detail.User = config.User
		detail.Command = append(append([]string{}, config.Entrypoint...), config.Cmd...)
		detail.Environment = redactEnvironment(config.Env)
		detail.Labels = toLabels(config.Labels)
	}
	if detail.Name == "" {
		detail.Name = detail.ContainerName
	}

	if state := inspected.State; state != nil {
		detail.State = toState(container.ContainerState(state.Status))
		detail.Status = state.Status
		detail.ExitCode = state.ExitCode
		detail.Error = state.Error
		detail.StartedAt = parseTime(state.StartedAt)
		detail.FinishedAt = parseTime(state.FinishedAt)
		if state.Health != nil {
			detail.Health = toHealth(state.Health.Status)
		}
	}

	detail.Created = parseTime(inspected.Created)
	detail.RestartCount = inspected.RestartCount
	detail.Platform = strings.TrimSpace(inspected.Platform)

	if host := inspected.HostConfig; host != nil {
		detail.RestartPolicy = string(host.RestartPolicy.Name)
	}

	for _, mount := range inspected.Mounts {
		source := mount.Source
		if mount.Name != "" {
			source = mount.Name
		}
		detail.Mounts = append(detail.Mounts, domain.Mount{
			Type:        string(mount.Type),
			Source:      source,
			Destination: mount.Destination,
			ReadWrite:   mount.RW,
		})
	}
	slices.SortFunc(detail.Mounts, func(a, b domain.Mount) int {
		return cmp.Compare(a.Destination, b.Destination)
	})

	if settings := inspected.NetworkSettings; settings != nil {
		for name, endpoint := range settings.Networks {
			if endpoint == nil {
				continue
			}
			detail.Networks = append(detail.Networks, domain.NetworkAttachment{
				Name:       name,
				NetworkID:  endpoint.NetworkID,
				IPAddress:  endpoint.IPAddress,
				Gateway:    endpoint.Gateway,
				MacAddress: endpoint.MacAddress,
				Aliases:    endpoint.Aliases,
			})
		}
		slices.SortFunc(detail.Networks, func(a, b domain.NetworkAttachment) int {
			return cmp.Compare(a.Name, b.Name)
		})

		detail.Ports = toPortsFromBindings(settings.Ports)
	}

	// A container that is not running has no active bindings, so the runtime
	// reports no ports at all — which reads as "this service exposes none"
	// rather than "it is not listening just now". What it will listen on is in
	// its configuration, so that is used instead.
	if len(detail.Ports) == 0 {
		detail.Ports = configuredPorts(inspected)
		detail.PortsConfigured = len(detail.Ports) > 0
	}

	return detail
}

// secretish are the substrings that mark an environment variable as one whose
// value must not be shown.
//
// The page has no authentication, and a compose file's environment is where
// database passwords and API tokens live — the very first example project this
// was pointed at had POSTGRES_PASSWORD in it. Matching on the name is crude
// and will miss a secret named something else, so it fails towards hiding: the
// key is always shown, and only the value is withheld.
var secretish = []string{
	"PASSWORD", "SECRET", "TOKEN", "APIKEY", "API_KEY", "ACCESS_KEY",
	"PRIVATE_KEY", "CREDENTIAL", "PASSPHRASE", "AUTH", "SESSION_KEY", "SALT",
}

// redacted is what stands in for a withheld value.
const redacted = "••••••••"

// redactEnvironment splits KEY=VALUE pairs, hiding values that look secret.
func redactEnvironment(environment []string) []domain.Label {
	labels := make([]domain.Label, 0, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			key, value = entry, ""
		}
		if isSecret(key) {
			value = redacted
		}
		labels = append(labels, domain.Label{Key: key, Value: value})
	}
	slices.SortFunc(labels, func(a, b domain.Label) int { return cmp.Compare(a.Key, b.Key) })
	return labels
}

// isSecret reports whether a variable's name marks its value as one to hide.
func isSecret(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range secretish {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// toLabels turns a label map into a stable, ordered slice.
func toLabels(labels map[string]string) []domain.Label {
	out := make([]domain.Label, 0, len(labels))
	for key, value := range labels {
		out = append(out, domain.Label{Key: key, Value: value})
	}
	slices.SortFunc(out, func(a, b domain.Label) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

// toHealth maps the inspect response's health string onto the domain's.
func toHealth(status string) domain.Health {
	switch status {
	case "healthy":
		return domain.HealthHealthy
	case "unhealthy":
		return domain.HealthUnhealthy
	case "starting":
		return domain.HealthStarting
	default:
		return domain.HealthNone
	}
}

// parseTime reads one of the runtime's RFC 3339 timestamps. A container that
// has never run carries a zero timestamp, which parses to a zero time — the
// page tests for that rather than printing the year 1.
func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	if parsed.Year() <= 1 {
		return time.Time{}
	}
	return parsed.UTC()
}

// configuredPorts is what a container is set up to listen on, as opposed to
// what it is listening on now.
//
// Two places say it, and both are needed: the host bindings carry the ports
// that would be published, and the exposed set carries the rest — the ones
// reachable only from the project's own networks, which have no binding to
// appear in.
func configuredPorts(inspected container.InspectResponse) []domain.Port {
	ports := make([]container.Port, 0)

	bound := map[nat.Port]bool{}
	if host := inspected.HostConfig; host != nil {
		for port, bindings := range host.PortBindings {
			bound[port] = true
			ports = append(ports, portsFromBinding(port, bindings)...)
		}
	}

	if config := inspected.Config; config != nil {
		for port := range config.ExposedPorts {
			if bound[port] {
				continue
			}
			ports = append(ports, container.Port{
				PrivatePort: uint16(port.Int()),
				Type:        port.Proto(),
			})
		}
	}

	return toPorts(ports)
}

// portsFromBinding turns one entry of a port map into the ports it describes.
func portsFromBinding(port nat.Port, bindings []nat.PortBinding) []container.Port {
	private := uint16(port.Int())
	protocol := port.Proto()

	if len(bindings) == 0 {
		return []container.Port{{PrivatePort: private, Type: protocol}}
	}

	ports := make([]container.Port, 0, len(bindings))
	for _, binding := range bindings {
		public, err := strconv.ParseUint(binding.HostPort, 10, 16)
		if err != nil {
			// A binding with no usable host port is still an exposed port;
			// showing it unpublished beats dropping it.
			ports = append(ports, container.Port{PrivatePort: private, Type: protocol})
			continue
		}
		ports = append(ports, container.Port{
			PrivatePort: private,
			PublicPort:  uint16(public),
			Type:        protocol,
		})
	}
	return ports
}

// toPortsFromBindings maps the port map an inspect returns.
//
// It is a different shape from the one the list returns — keyed by
// "port/proto" with the host side as strings — so it needs its own mapping,
// but it goes through the same dedup and ordering so a detail page and a row
// cannot disagree about a container's ports.
func toPortsFromBindings(bindings nat.PortMap) []domain.Port {
	ports := make([]container.Port, 0, len(bindings))

	for port, hosts := range bindings {
		ports = append(ports, portsFromBinding(port, hosts)...)
	}

	return toPorts(ports)
}
