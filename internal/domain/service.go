// Package domain holds the entities the service reasons about. It has no
// dependencies: nothing here knows about Docker, HTTP or configuration.
package domain

import (
	"strings"
	"time"
)

// State is the lifecycle state of the container backing a service. The values
// mirror the states the container runtime reports.
type State string

const (
	StateCreated    State = "created"
	StateRunning    State = "running"
	StatePaused     State = "paused"
	StateRestarting State = "restarting"
	StateRemoving   State = "removing"
	StateExited     State = "exited"
	StateDead       State = "dead"
	StateUnknown    State = "unknown"
)

// Online reports whether the state counts as the service being up. Restarting
// is deliberately not online: the container is flapping, and showing it as
// healthy hides exactly the problem the page exists to surface.
func (s State) Online() bool {
	return s == StateRunning
}

// Health is the outcome of a container's healthcheck, or HealthNone when it
// declares none.
type Health string

const (
	HealthNone      Health = ""
	HealthStarting  Health = "starting"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// Port is a container port, published to the host or not.
type Port struct {
	// Host is the port on the host, or 0 when the port is not published.
	Host uint16
	// Container is the port inside the container.
	Container uint16
	// Protocol is "tcp" or "udp".
	Protocol string
}

// Published reports whether the port is reachable from the host.
func (p Port) Published() bool {
	return p.Host != 0
}

// Service is one service of a compose project, as it exists right now: the
// declared name from the project, joined with the state of the container
// running it.
//
// The identity is ContainerID rather than Name, because a project may run
// several containers for one service (`--scale`), and because a container
// recreated by `up` is a different container even though the name is the same.
type Service struct {
	// ContainerID identifies the row; it is the container's full ID.
	ContainerID string
	// Name is the service name declared in the compose project.
	Name string
	// Number distinguishes the containers of a scaled service, starting at 1.
	Number int
	// ContainerName is the runtime name, e.g. "myproject-postgres-1".
	ContainerName string
	// Image is the image reference the container was created from.
	Image string
	// Title is what the image calls itself, from its
	// org.opencontainers.image.title label, or empty when it declares none.
	Title string
	// Description is the image's one-line description, from its
	// org.opencontainers.image.description label, or empty.
	Description string
	// URL is where the service answers on the web, taken from the routing
	// labels a reverse proxy reads, or empty when it carries none.
	URL string
	// State is the container's lifecycle state.
	State State
	// Status is the runtime's human-readable summary, e.g. "Up 3 hours".
	Status string
	// StatusKind is Status with the elapsed time taken out — "Up 3 hours"
	// and "Up 4 hours" both become "Up", while "Exited (0)" and
	// "Exited (137)" stay apart.
	//
	// It exists so two observations of a container that nothing has happened
	// to compare equal. Status alone changes every time the clock does, which
	// is a change to the page but not a change to the container.
	StatusKind string
	// Elapsed is the other half: the time alone, without the state in front of
	// it. "Up 3 hours" gives "3 hours" and "Exited (0) 2 hours ago" gives
	// "2 hours ago", so a listing that already says what state a service is in
	// need not say it twice. Empty for a status with no time in it, such as
	// "Created".
	Elapsed string
	// Health is the healthcheck outcome, or HealthNone when there is none.
	Health Health
	// Ports are the container's ports, published and unpublished.
	Ports []Port
	// Created is when the container was created.
	Created time.Time
}

// Online reports whether the service is up.
func (s Service) Online() bool {
	return s.State.Online()
}

// CurrentHealth is the healthcheck outcome, but only while there is one to
// have.
//
// The runtime keeps the last result on a container after it stops, so
// inspecting a stopped container reports whatever it was when it was last
// running. Presenting that as current puts a "healthy" badge next to an
// "exited" one, which is a contradiction on its face — and one the list never
// showed, because listing reads health out of the status line, which a stopped
// container has none in. Health is a property of a running container; the last
// known value is still on the Health field for anything that wants to say so
// explicitly.
func (s Service) CurrentHealth() Health {
	if !s.Online() {
		return HealthNone
	}
	return s.Health
}

// Summary counts a set of services by whether they are up. It is what the
// page's header reports.
type Summary struct {
	Total   int
	Online  int
	Offline int
}

// Mount is a path from outside the container made available inside it.
type Mount struct {
	// Type is "bind", "volume", "tmpfs" and so on.
	Type string
	// Source is the volume name or host path it comes from.
	Source string
	// Destination is where it appears inside the container.
	Destination string
	// ReadWrite is whether the container may write to it.
	ReadWrite bool
}

// NetworkAttachment is one network a container is joined to.
type NetworkAttachment struct {
	Name       string
	NetworkID  string
	IPAddress  string
	Gateway    string
	MacAddress string
	Aliases    []string
}

// Label is one key/value pair, kept as a slice rather than a map so the order
// on the page is stable.
type Label struct {
	Key   string
	Value string
}

// ServiceDetail is everything the page shows about one service, beyond what
// fits in a row. It is the result of inspecting the container rather than
// listing it.
type ServiceDetail struct {
	Service

	// Command is the entrypoint and command the container runs.
	Command []string
	// WorkingDir is the working directory inside the container.
	WorkingDir string
	// User is the user the container's process runs as, if one was set.
	User string
	// RestartPolicy is how the runtime restarts it, e.g. "unless-stopped".
	RestartPolicy string
	// Platform is the OS and architecture the image was built for.
	Platform string

	// StartedAt and FinishedAt bound the current or last run. Either may be
	// zero: a container that has never run has no start.
	StartedAt  time.Time
	FinishedAt time.Time
	// ExitCode is meaningful once the container has stopped.
	ExitCode int
	// Error is what the runtime recorded about why it stopped, if anything.
	Error string
	// RestartCount is how many times the runtime has restarted it.
	RestartCount int

	// Environment is the container's environment. Values that look like
	// secrets are redacted before they reach here — see the adapter.
	Environment []Label
	// Labels are the container's labels, compose's own included.
	Labels []Label
	// Mounts are the volumes and paths mounted into it.
	Mounts []Mount
	// Networks are the networks it is attached to.
	Networks []NetworkAttachment

	// PortsConfigured is whether Ports came from the container's configuration
	// rather than from what it is listening on now, which is the case once it
	// has stopped.
	PortsConfigured bool
}

// Well-known label keys the detail page reads by name.
const (
	// LabelImageTitle and LabelImageDescription are the OCI annotations an
	// image carries to say what it is in human terms. They are what the header
	// prefers over the compose service name, which is only what this project
	// happens to call it.
	LabelImageTitle       = "org.opencontainers.image.title"
	LabelImageDescription = "org.opencontainers.image.description"
)

// Label key prefixes the page folds together. Everything that does not match
// one of them lands in a group of its own, so no label is ever dropped just
// because nothing was expecting it.
const (
	LabelPrefixCompose = "com.docker.compose."
	LabelPrefixOCI     = "org.opencontainers."
	LabelPrefixTraefik = "traefik."
)

// LabelGroup is a set of labels shown together under one heading.
type LabelGroup struct {
	// Title is the heading, e.g. "Compose".
	Title string
	// Labels are the group's members, in the order they were given.
	Labels []Label
}

// DisplayName is what to call this service to a person: the image's own title
// if it declares one, and the name this project gives it otherwise.
func (s Service) DisplayName() string {
	if s.Title != "" {
		return s.Title
	}
	return s.Name
}

// HasTitle reports whether the image named itself, and so whether DisplayName
// is saying something the service name does not already say.
func (s Service) HasTitle() bool {
	return s.Title != "" && s.Title != s.Name
}

// GroupLabels splits labels into the sets a page folds separately, dropping
// any group that has no members.
//
// The last group is everything left over. It exists so that a label belonging
// to none of the known families is still shown — grouping is meant to make a
// long list readable, not to decide what is worth seeing.
//
// It is a function rather than a method because containers are not the only
// thing carrying labels: Compose puts the same family on the networks and
// volumes it creates, and they are worth reading the same way.
func GroupLabels(labels []Label) []LabelGroup {
	known := []struct {
		title  string
		prefix string
	}{
		{"Compose", LabelPrefixCompose},
		{"Image (OCI)", LabelPrefixOCI},
		{"Traefik", LabelPrefixTraefik},
	}

	groups := make([]LabelGroup, len(known))
	for i, group := range known {
		groups[i].Title = group.title
	}
	var other LabelGroup
	other.Title = "Other"

	for _, label := range labels {
		matched := false
		for i, group := range known {
			if strings.HasPrefix(label.Key, group.prefix) {
				groups[i].Labels = append(groups[i].Labels, label)
				matched = true
				break
			}
		}
		if !matched {
			other.Labels = append(other.Labels, label)
		}
	}

	groups = append(groups, other)

	populated := make([]LabelGroup, 0, len(groups))
	for _, group := range groups {
		if len(group.Labels) > 0 {
			populated = append(populated, group)
		}
	}
	return populated
}

// ResourceUsage counts, per name, how many of the project's containers refer
// to a network or a volume. It is derived from the containers, because that is
// the only place the association is recorded in both directions.
type ResourceUsage struct {
	Networks map[string]int
	Volumes  map[string]int
}
