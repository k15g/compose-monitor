package docker

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/k15g/compose-monitor/internal/domain"
)

// toService maps one container summary onto a domain service.
func toService(summary container.Summary) domain.Service {
	name := summary.Labels[labelService]
	containerName := containerName(summary.Names)
	if name == "" {
		// A container carrying the project label but no service label is not
		// something Compose creates, but it is reachable by hand with
		// `docker run --label`. Showing it under its container name beats
		// dropping it from a page whose job is to show what is there.
		name = containerName
	}

	return domain.Service{
		ContainerID:   summary.ID,
		Name:          name,
		Number:        containerNumber(summary.Labels[labelNumber]),
		ContainerName: containerName,
		Image:         summary.Image,
		Title:         summary.Labels[domain.LabelImageTitle],
		Description:   summary.Labels[domain.LabelImageDescription],
		URL:           serviceURL(summary.Labels),
		State:         toState(summary.State),
		Status:        summary.Status,
		StatusKind:    statusKind(summary.Status),
		Elapsed:       statusElapsed(summary.Status),
		Health:        healthFromStatus(summary.Status),
		Ports:         toPorts(summary.Ports),
		Created:       time.Unix(summary.Created, 0).UTC(),
	}
}

// containerName takes the first of the runtime's names and strips the leading
// slash it always carries.
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// containerNumber parses the replica number. An unscaled service is 1, which
// is also the answer for a container that does not carry the label.
func containerNumber(value string) int {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 1
	}
	return number
}

// toState maps the runtime's state onto the domain's. An unrecognised state
// becomes StateUnknown rather than being passed through, so a future runtime
// state cannot be mistaken for online.
func toState(state container.ContainerState) domain.State {
	switch state {
	case container.StateCreated:
		return domain.StateCreated
	case container.StateRunning:
		return domain.StateRunning
	case container.StatePaused:
		return domain.StatePaused
	case container.StateRestarting:
		return domain.StateRestarting
	case container.StateRemoving:
		return domain.StateRemoving
	case container.StateExited:
		return domain.StateExited
	case container.StateDead:
		return domain.StateDead
	default:
		return domain.StateUnknown
	}
}

// healthFromStatus reads the healthcheck outcome out of the status line.
//
// The container list endpoint does not return health as a field — it is only
// in the status string, as a parenthesised suffix like "Up 5 minutes
// (healthy)". The alternative is inspecting every container on every read,
// which is a request per container per refresh for one word.
func healthFromStatus(status string) domain.Health {
	switch {
	case strings.Contains(status, "(healthy)"):
		return domain.HealthHealthy
	case strings.Contains(status, "(unhealthy)"):
		return domain.HealthUnhealthy
	case strings.Contains(status, "(health: starting)"):
		return domain.HealthStarting
	default:
		return domain.HealthNone
	}
}

// toPorts maps the container's ports, dropping duplicates and ordering them.
//
// The runtime reports a published port once per host address it is bound to,
// so a port published on both IPv4 and IPv6 arrives twice and would otherwise
// be drawn twice — and would make two identical observations of one container
// compare as different, depending on the order the bindings came back in.
func toPorts(ports []container.Port) []domain.Port {
	if len(ports) == 0 {
		return nil
	}

	seen := make(map[domain.Port]struct{}, len(ports))
	mapped := make([]domain.Port, 0, len(ports))
	for _, port := range ports {
		p := domain.Port{
			Host:      port.PublicPort,
			Container: port.PrivatePort,
			Protocol:  port.Type,
		}
		if _, duplicate := seen[p]; duplicate {
			continue
		}
		seen[p] = struct{}{}
		mapped = append(mapped, p)
	}

	slices.SortFunc(mapped, func(a, b domain.Port) int {
		return cmp.Or(
			cmp.Compare(a.Container, b.Container),
			cmp.Compare(a.Host, b.Host),
			cmp.Compare(a.Protocol, b.Protocol),
		)
	})
	return mapped
}
