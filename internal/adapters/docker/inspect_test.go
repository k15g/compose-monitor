package docker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"

	"github.com/k15g/compose-monitor/internal/domain"
)

func TestRedactEnvironmentHidesSecretLookingValues(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  domain.Label
	}{
		{"plain value is shown", "POSTGRES_DB=example", domain.Label{Key: "POSTGRES_DB", Value: "example"}},
		{"password", "POSTGRES_PASSWORD=hunter2", domain.Label{Key: "POSTGRES_PASSWORD", Value: redacted}},
		{"secret", "CLIENT_SECRET=abc", domain.Label{Key: "CLIENT_SECRET", Value: redacted}},
		{"token", "CF_DNS_API_TOKEN=abc", domain.Label{Key: "CF_DNS_API_TOKEN", Value: redacted}},
		{"api key", "SOME_API_KEY=abc", domain.Label{Key: "SOME_API_KEY", Value: redacted}},
		{"private key", "TLS_PRIVATE_KEY=abc", domain.Label{Key: "TLS_PRIVATE_KEY", Value: redacted}},
		{"lowercase still matches", "db_password=hunter2", domain.Label{Key: "db_password", Value: redacted}},
		{"no value at all", "FLAG", domain.Label{Key: "FLAG", Value: ""}},
		{"empty value", "EMPTY=", domain.Label{Key: "EMPTY", Value: ""}},
		{"value containing an equals sign", "DSN=postgres://a=b", domain.Label{Key: "DSN", Value: "postgres://a=b"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := redactEnvironment([]string{test.entry})
			assert.Equal(t, []domain.Label{test.want}, got)
		})
	}
}

func TestRedactEnvironmentAlwaysShowsTheKey(t *testing.T) {
	// Matching on the name is crude and will miss a secret named something
	// unexpected, so it fails towards hiding: the value goes, the name stays,
	// and the page still says the variable is set.
	got := redactEnvironment([]string{"APP_PASSWORD=hunter2"})

	assert.Equal(t, "APP_PASSWORD", got[0].Key)
	assert.NotContains(t, got[0].Value, "hunter2")
}

func TestRedactEnvironmentIsOrdered(t *testing.T) {
	got := redactEnvironment([]string{"ZULU=1", "ALPHA=2", "MIKE=3"})

	assert.Equal(t, []string{"ALPHA", "MIKE", "ZULU"}, []string{got[0].Key, got[1].Key, got[2].Key},
		"a reordering from the daemon must not reorder the page")
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		name  string
		value string
		zero  bool
	}{
		{"empty", "", true},
		{"the runtime's zero timestamp", "0001-01-01T00:00:00Z", true},
		{"unparseable", "not a time", true},
		{"a real time", "2026-08-14T18:00:00.123456789Z", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseTime(test.value)
			assert.Equal(t, test.zero, got.IsZero(),
				"a container that never ran must not render as a date in year 1")
			if !test.zero {
				assert.Equal(t, 2026, got.Year())
				assert.Equal(t, time.UTC, got.Location())
			}
		})
	}
}

func TestToHealth(t *testing.T) {
	assert.Equal(t, domain.HealthHealthy, toHealth("healthy"))
	assert.Equal(t, domain.HealthUnhealthy, toHealth("unhealthy"))
	assert.Equal(t, domain.HealthStarting, toHealth("starting"))
	assert.Equal(t, domain.HealthNone, toHealth("none"))
	assert.Equal(t, domain.HealthNone, toHealth(""))
}

func TestToLabelsIsOrdered(t *testing.T) {
	got := toLabels(map[string]string{"b": "2", "a": "1", "c": "3"})

	assert.Equal(t, []domain.Label{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}, {Key: "c", Value: "3"}}, got)
}

func TestToLabelsOfNothing(t *testing.T) {
	assert.Empty(t, toLabels(nil))
}

func TestConfiguredPortsAreUsedWhenNothingIsBound(t *testing.T) {
	// A container that has stopped has no active bindings, so the runtime
	// reports no ports — which reads as "exposes none" rather than "not
	// listening just now".
	inspected := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:    "c1",
			Name:  "/example-web-1",
			State: &container.State{Status: "exited"},
			HostConfig: &container.HostConfig{PortBindings: nat.PortMap{
				"80/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}},
			}},
		},
		Config: &container.Config{
			Labels: map[string]string{labelProject: "example", labelService: "web"},
			ExposedPorts: nat.PortSet{
				"80/tcp":   struct{}{},
				"5432/tcp": struct{}{},
			},
		},
		NetworkSettings: &container.NetworkSettings{},
	}

	detail := toDetail(inspected)

	assert.True(t, detail.PortsConfigured, "the page says these are configured, not live")
	assert.Equal(t, []domain.Port{
		// Published, from the host bindings.
		{Host: 8080, Container: 80, Protocol: "tcp"},
		// Exposed with no binding: reachable only from the project's networks,
		// and so absent from the bindings entirely.
		{Host: 0, Container: 5432, Protocol: "tcp"},
	}, detail.Ports)
}

func TestLivePortsWinOverConfiguredOnes(t *testing.T) {
	// Assigned through the promoted field rather than named: the struct it
	// lives on is deprecated, and folds into NetworkSettings in v29.
	settings := &container.NetworkSettings{}
	settings.Ports = nat.PortMap{"80/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}}}

	inspected := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:    "c1",
			Name:  "/example-web-1",
			State: &container.State{Status: "running"},
			HostConfig: &container.HostConfig{PortBindings: nat.PortMap{
				"80/tcp": {{HostPort: "9999"}},
			}},
		},
		Config:          &container.Config{Labels: map[string]string{labelProject: "example"}},
		NetworkSettings: settings,
	}

	detail := toDetail(inspected)

	assert.False(t, detail.PortsConfigured)
	assert.Equal(t, []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}}, detail.Ports,
		"what it is listening on beats what it was told to listen on")
}

func TestAContainerWithNoPortsAtAll(t *testing.T) {
	detail := toDetail(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID: "c1", Name: "/example-worker-1", State: &container.State{Status: "exited"},
			HostConfig: &container.HostConfig{},
		},
		Config:          &container.Config{Labels: map[string]string{labelProject: "example"}},
		NetworkSettings: &container.NetworkSettings{},
	})

	assert.Empty(t, detail.Ports)
	assert.False(t, detail.PortsConfigured, "there is nothing to explain")
}
