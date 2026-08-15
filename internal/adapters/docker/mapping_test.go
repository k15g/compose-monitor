package docker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"

	"github.com/k15g/compose-monitor/internal/domain"
)

func TestToState(t *testing.T) {
	tests := []struct {
		state container.ContainerState
		want  domain.State
	}{
		{container.StateCreated, domain.StateCreated},
		{container.StateRunning, domain.StateRunning},
		{container.StatePaused, domain.StatePaused},
		{container.StateRestarting, domain.StateRestarting},
		{container.StateRemoving, domain.StateRemoving},
		{container.StateExited, domain.StateExited},
		{container.StateDead, domain.StateDead},
		{"something-new", domain.StateUnknown},
		{"", domain.StateUnknown},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			got := toState(test.state)
			assert.Equal(t, test.want, got)
			if test.want == domain.StateUnknown {
				assert.False(t, got.Online(), "an unrecognised state must never read as online")
			}
		})
	}
}

func TestHealthFromStatus(t *testing.T) {
	tests := []struct {
		status string
		want   domain.Health
	}{
		{"Up 5 minutes", domain.HealthNone},
		{"Up 5 minutes (healthy)", domain.HealthHealthy},
		{"Up 2 minutes (unhealthy)", domain.HealthUnhealthy},
		{"Up 3 seconds (health: starting)", domain.HealthStarting},
		{"Exited (0) 5 minutes ago", domain.HealthNone},
		{"", domain.HealthNone},
	}

	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			assert.Equal(t, test.want, healthFromStatus(test.status))
		})
	}
}

func TestContainerNumber(t *testing.T) {
	tests := []struct {
		label string
		want  int
	}{
		{"1", 1},
		{"3", 3},
		{"", 1},
		{"not-a-number", 1},
		{"0", 1},
		{"-2", 1},
	}

	for _, test := range tests {
		t.Run("label="+test.label, func(t *testing.T) {
			assert.Equal(t, test.want, containerNumber(test.label))
		})
	}
}

func TestContainerName(t *testing.T) {
	assert.Equal(t, "example-web-1", containerName([]string{"/example-web-1", "/other"}))
	assert.Equal(t, "", containerName(nil))
}

func TestToPorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []container.Port
		want  []domain.Port
	}{
		{
			name:  "none",
			ports: nil,
			want:  nil,
		},
		{
			name: "a port published on both address families collapses to one",
			ports: []container.Port{
				{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			},
			want: []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}},
		},
		{
			name: "unpublished ports are kept",
			ports: []container.Port{
				{PrivatePort: 5432, Type: "tcp"},
			},
			want: []domain.Port{{Host: 0, Container: 5432, Protocol: "tcp"}},
		},
		{
			name: "order does not depend on the order the runtime reported",
			ports: []container.Port{
				{PrivatePort: 443, PublicPort: 8443, Type: "tcp"},
				{PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{PrivatePort: 53, Type: "udp"},
			},
			want: []domain.Port{
				{Host: 0, Container: 53, Protocol: "udp"},
				{Host: 8080, Container: 80, Protocol: "tcp"},
				{Host: 8443, Container: 443, Protocol: "tcp"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, toPorts(test.ports))
		})
	}
}

func TestToService(t *testing.T) {
	summary := container.Summary{
		ID:      "abcdef0123456789",
		Names:   []string{"/example-web-2"},
		Image:   "nginx:1.27",
		Created: 1700000000,
		State:   container.StateRunning,
		Status:  "Up 5 minutes (healthy)",
		Labels: map[string]string{
			labelProject:                 "example",
			labelService:                 "web",
			labelNumber:                  "2",
			domain.LabelImageTitle:       "Web Front End",
			domain.LabelImageDescription: "Serves the public site.",
		},
		Ports: []container.Port{{PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
	}

	got := toService(summary)

	assert.Equal(t, domain.Service{
		ContainerID:   "abcdef0123456789",
		Name:          "web",
		Number:        2,
		ContainerName: "example-web-2",
		Image:         "nginx:1.27",
		Title:         "Web Front End",
		Description:   "Serves the public site.",
		State:         domain.StateRunning,
		Status:        "Up 5 minutes (healthy)",
		StatusKind:    "Up (healthy)",
		Elapsed:       "5 minutes",
		Health:        domain.HealthHealthy,
		Ports:         []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}},
		Created:       time.Unix(1700000000, 0).UTC(),
	}, got)
}

func TestToServiceStoppedContainer(t *testing.T) {
	summary := container.Summary{
		ID:     "c1",
		Names:  []string{"/example-db-1"},
		Image:  "postgres:18",
		State:  container.StateExited,
		Status: "Exited (0) 2 hours ago",
		Labels: map[string]string{labelProject: "example", labelService: "db"},
	}

	got := toService(summary)

	assert.Equal(t, "db", got.Name)
	assert.Equal(t, 1, got.Number, "a container with no replica label is replica 1")
	assert.False(t, got.Online(), "a stopped container is a service that is offline")
}

func TestToServiceWithoutServiceLabel(t *testing.T) {
	summary := container.Summary{
		ID:     "c1",
		Names:  []string{"/hand-rolled"},
		State:  container.StateRunning,
		Labels: map[string]string{labelProject: "example"},
	}

	got := toService(summary)

	assert.Equal(t, "hand-rolled", got.Name,
		"a container labelled for the project but not by Compose falls back to its container name")
}

func TestToServiceWithoutImageLabels(t *testing.T) {
	// Most images declare neither. The listing then calls the service what the
	// project calls it, and says nothing further about it.
	got := toService(container.Summary{
		ID:     "c1",
		Names:  []string{"/example-db-1"},
		State:  container.StateRunning,
		Labels: map[string]string{labelProject: "example", labelService: "db"},
	})

	assert.Empty(t, got.Title)
	assert.Empty(t, got.Description)
	assert.Equal(t, "db", got.DisplayName())
	assert.False(t, got.HasTitle())
}
