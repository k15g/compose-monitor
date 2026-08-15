package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/domain"
)

func TestCurrentHealthOnlyAppliesWhileRunning(t *testing.T) {
	tests := []struct {
		name   string
		state  domain.State
		health domain.Health
		want   domain.Health
	}{
		{"running and healthy", domain.StateRunning, domain.HealthHealthy, domain.HealthHealthy},
		{"running and unhealthy", domain.StateRunning, domain.HealthUnhealthy, domain.HealthUnhealthy},
		{"running with no healthcheck", domain.StateRunning, domain.HealthNone, domain.HealthNone},

		// The runtime keeps the last result after a container stops, so
		// inspecting a stopped one still reports it. Presenting that as
		// current puts a "healthy" badge next to an "exited" one.
		{"exited, last known healthy", domain.StateExited, domain.HealthHealthy, domain.HealthNone},
		{"exited, last known unhealthy", domain.StateExited, domain.HealthUnhealthy, domain.HealthNone},
		{"created, never ran", domain.StateCreated, domain.HealthHealthy, domain.HealthNone},
		{"restarting", domain.StateRestarting, domain.HealthHealthy, domain.HealthNone},
		{"dead", domain.StateDead, domain.HealthHealthy, domain.HealthNone},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := domain.Service{State: test.state, Health: test.health}

			assert.Equal(t, test.want, service.CurrentHealth())
			assert.Equal(t, test.health, service.Health,
				"the last known value is kept on the field for anything that says so explicitly")
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		service  domain.Service
		want     string
		hasTitle bool
	}{
		{
			name:     "the image named itself",
			service:  domain.Service{Name: "monitor", Title: "Compose Monitor"},
			want:     "Compose Monitor",
			hasTitle: true,
		},
		{
			// An image that says nothing about itself is called what this
			// project calls it.
			name:    "no title",
			service: domain.Service{Name: "web"},
			want:    "web",
		},
		{
			// Nothing is gained by showing the same word twice.
			name:    "a title that repeats the service name",
			service: domain.Service{Name: "web", Title: "web"},
			want:    "web",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, test.service.DisplayName())
			assert.Equal(t, test.hasTitle, test.service.HasTitle())
		})
	}
}

func TestGroupLabelsKeepsEveryLabel(t *testing.T) {
	labels := []domain.Label{
		{Key: "com.docker.compose.project", Value: "example"},
		{Key: "com.docker.compose.service", Value: "web"},
		{Key: "org.opencontainers.image.title", Value: "Web"},
		{Key: "traefik.enable", Value: "true"},
		{Key: "maintainer", Value: "someone"},
		{Key: "build.date", Value: "2026-08-15"},
	}

	groups := domain.GroupLabels(labels)

	titles := make([]string, len(groups))
	counted := 0
	for i, group := range groups {
		titles[i] = group.Title
		counted += len(group.Labels)
	}

	assert.Equal(t, []string{"Compose", "Image (OCI)", "Traefik", "Other"}, titles)
	assert.Equal(t, len(labels), counted,
		"grouping makes a long list readable; it does not decide what is worth seeing")
}

func TestGroupLabelsDropsEmptyGroups(t *testing.T) {
	// A resource carrying nothing but Compose's own labels shows one group,
	// not four — and no generic one.
	groups := domain.GroupLabels([]domain.Label{
		{Key: "com.docker.compose.project", Value: "example"},
	})

	require.Len(t, groups, 1)
	assert.Equal(t, "Compose", groups[0].Title)
}

func TestGroupLabelsOfNothing(t *testing.T) {
	assert.Empty(t, domain.GroupLabels(nil))
}

func TestNetworkAndVolumeUsage(t *testing.T) {
	assert.False(t, domain.Network{}.InUse())
	assert.True(t, domain.Network{UsedBy: 1}.InUse())
	assert.False(t, domain.Volume{}.InUse())
	assert.True(t, domain.Volume{UsedBy: 2}.InUse())
}
