package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// fakeDaemon is enough of the Engine API to exercise the adapter against a
// real unix socket: the version handshake, the container list, and the event
// stream. It is not a mock of the client — the request that goes over the
// socket is the one the SDK builds, so a wrong filter or a mis-decoded field
// fails here.
type fakeDaemon struct {
	host string

	containers []map[string]any

	// filters records the filters query of the last container list, which is
	// how the tests check that the project label actually narrowed the request
	// rather than the adapter filtering after the fact.
	filters chan string

	// inspected, networks and volumes are what the daemon answers for the
	// endpoints that take an id rather than a filter.
	inspected map[string]any
	networks  []map[string]any
	volumes   []map[string]any

	events chan string

	// stopped records the container ids the daemon was asked to stop, with the
	// grace period the request carried.
	stopped chan string

	// removed records the container ids the daemon was asked to delete, with
	// the options the request carried.
	removed chan string
}

func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	return startFakeDaemonAt(t, filepath.Join(t.TempDir(), "docker.sock"))
}

func startFakeDaemonAt(t *testing.T, socket string) *fakeDaemon {
	t.Helper()

	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)

	daemon := &fakeDaemon{
		host:    "unix://" + socket,
		filters: make(chan string, 8),
		events:  make(chan string, 8),
		stopped: make(chan string, 8),
		removed: make(chan string, 8),
	}

	server := &http.Server{Handler: daemon}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	return daemon
}

// answerByID returns the entry whose key field matches the last path segment.
func (d *fakeDaemon) answerByID(w http.ResponseWriter, r *http.Request, entries []map[string]any, key, kind string) {
	id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	for _, entry := range entries {
		if entry[key] == id {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(entry)
			return
		}
	}
	d.notFound(w, kind)
}

func (d *fakeDaemon) notFound(w http.ResponseWriter, kind string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such " + kind})
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/stop") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/containers/")+len("/containers/"):], ""), "/stop")
		query, _ := url.ParseQuery(r.URL.RawQuery)
		select {
		case d.stopped <- id + " t=" + query.Get("t"):
		default:
		}
		// A container that has gone away between the list and the stop is the
		// realistic failure, and the daemon answers it with a 404.
		if strings.HasPrefix(id, "gone") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container: " + id})
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case strings.Contains(r.URL.Path, "/containers/") && r.Method == http.MethodDelete:
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		query, _ := url.ParseQuery(r.URL.RawQuery)
		select {
		case d.removed <- fmt.Sprintf("%s v=%q force=%q", id, query.Get("v"), query.Get("force")):
		default:
		}
		w.WriteHeader(http.StatusNoContent)

	case strings.HasSuffix(r.URL.Path, "/_ping"):
		w.Header().Set("Api-Version", "1.51")
		w.Header().Set("Ostype", "linux")
		w.WriteHeader(http.StatusOK)

	case strings.HasSuffix(r.URL.Path, "/networks"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.networks)

	case strings.Contains(r.URL.Path, "/networks/"):
		d.answerByID(w, r, d.networks, "Id", "network")

	case strings.HasSuffix(r.URL.Path, "/volumes"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": d.volumes})

	case strings.Contains(r.URL.Path, "/volumes/"):
		d.answerByID(w, r, d.volumes, "Name", "volume")

	case strings.HasSuffix(r.URL.Path, "/containers/json"):
		query, _ := url.ParseQuery(r.URL.RawQuery)
		select {
		case d.filters <- query.Get("filters"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.containers)

	case strings.HasSuffix(r.URL.Path, "/json") && strings.Contains(r.URL.Path, "/containers/"):
		if d.inspected == nil {
			d.notFound(w, "container")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.inspected)

	case strings.HasSuffix(r.URL.Path, "/events"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(w)
		_ = controller.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case message := <-d.events:
				if _, err := fmt.Fprintln(w, message); err != nil {
					return
				}
				_ = controller.Flush()
			}
		}

	default:
		http.NotFound(w, r)
	}
}

func daemonContext(t *testing.T, daemon *fakeDaemon) context.Context {
	t.Helper()
	return config.WithConfig(t.Context(), &config.Config{
		Docker:  config.DockerConfig{Host: daemon.host},
		Project: config.ProjectConfig{Name: "example"},
	})
}

func TestClientListsBothRunningAndStoppedContainers(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.containers = []map[string]any{
		{
			"Id":     "c1",
			"Names":  []string{"/example-web-1"},
			"Image":  "nginx:1.27",
			"State":  "running",
			"Status": "Up 5 minutes (healthy)",
			"Labels": map[string]string{labelProject: "example", labelService: "web", labelNumber: "1"},
			"Ports":  []map[string]any{{"PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"}},
		},
		{
			"Id":     "c2",
			"Names":  []string{"/example-db-1"},
			"Image":  "postgres:18",
			"State":  "exited",
			"Status": "Exited (0) 2 hours ago",
			"Labels": map[string]string{labelProject: "example", labelService: "db", labelNumber: "1"},
		},
	}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	services, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, services, 2)

	assert.Equal(t, "web", services[0].Name)
	assert.True(t, services[0].Online())
	assert.Equal(t, domain.HealthHealthy, services[0].Health)
	assert.Equal(t, []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}}, services[0].Ports)

	assert.Equal(t, "db", services[1].Name)
	assert.False(t, services[1].Online(), "a stopped container is an offline service, not an absent one")

	// The `all` flag is what makes the stopped container visible at all, and
	// the label filter is what makes the list this project's.
	filters := <-daemon.filters
	assert.Contains(t, filters, labelProject+"=example")
}

func TestClientListIsEmptyForAProjectWithNothingUp(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.containers = []map[string]any{}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	services, err := client.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, services)
}

func TestClientWatchSignalsOnContainerEvents(t *testing.T) {
	daemon := startFakeDaemon(t)

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	changes := client.Watch(ctx)

	daemon.events <- `{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{}},"time":1700000000}`

	select {
	case _, open := <-changes:
		assert.True(t, open)
	case <-time.After(3 * time.Second):
		t.Fatal("a container event produced no signal")
	}
}

func TestClientWatchCoalescesUnreadSignals(t *testing.T) {
	daemon := startFakeDaemon(t)

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	changes := client.Watch(ctx)

	for range 5 {
		daemon.events <- `{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{}},"time":1700000000}`
	}

	select {
	case <-changes:
	case <-time.After(3 * time.Second):
		t.Fatal("a container event produced no signal")
	}

	// The signal carries no information, so a second one queued behind an
	// unread first adds nothing. The buffer holds one.
	assert.LessOrEqual(t, len(changes), 1)
}

func TestClientWatchClosesWhenTheContextIsDone(t *testing.T) {
	daemon := startFakeDaemon(t)

	ctx, cancel := context.WithCancel(daemonContext(t, daemon))
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	changes := client.Watch(ctx)
	cancel()

	select {
	case _, open := <-changes:
		assert.False(t, open, "the signal channel closes when the watch is cancelled")
	case <-time.After(3 * time.Second):
		t.Fatal("the signal channel was left open after cancellation")
	}
}

func TestNewStartsEvenWhenTheSocketIsNotThere(t *testing.T) {
	ctx := config.WithConfig(t.Context(), &config.Config{
		Docker:  config.DockerConfig{Host: "unix://" + filepath.Join(t.TempDir(), "absent.sock")},
		Project: config.ProjectConfig{Name: "example"},
	})

	client, err := New(ctx)

	// An unreachable runtime is a fixable mistake — a missing mount, or one the
	// container may not read. Refusing to start turns it into a restart loop
	// whose only symptom is a log line, so the service starts and reports it on
	// the page instead.
	require.NoError(t, err)
	require.NotNil(t, client)
	defer func() { _ = client.Close() }()

	_, err = client.List(ctx)
	assert.Error(t, err, "the failure surfaces on the read, which is what the page renders")
}

func TestNewFailsOnAHostItCannotParse(t *testing.T) {
	ctx := config.WithConfig(t.Context(), &config.Config{
		Docker:  config.DockerConfig{Host: "not-a-url"},
		Project: config.ProjectConfig{Name: "example"},
	})

	_, err := New(ctx)

	// Unlike an unreachable socket, a host that cannot be parsed is a
	// configuration error that no amount of waiting fixes, so it stops the
	// process.
	require.Error(t, err)
	assert.ErrorIs(t, err, ports.ErrSourceUnavailable)
}

func TestClientPicksUpARuntimeThatAppearsLater(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "docker.sock")

	ctx := config.WithConfig(t.Context(), &config.Config{
		Docker:  config.DockerConfig{Host: "unix://" + socket},
		Project: config.ProjectConfig{Name: "example"},
	})

	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.List(ctx)
	require.Error(t, err, "nothing is listening yet")

	// Bring the runtime up underneath the client that already exists. Recovery
	// must not need a restart: the same client starts working once the socket
	// answers.
	daemon := startFakeDaemonAt(t, socket)
	daemon.containers = []map[string]any{{
		"Id": "c1", "Names": []string{"/example-web-1"}, "State": "running",
		"Labels": map[string]string{labelProject: "example", labelService: "web"},
	}}

	services, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "web", services[0].Name)
}

func TestClientStopsAContainer(t *testing.T) {
	daemon := startFakeDaemon(t)

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	require.NoError(t, client.Stop(ctx, "abcdef0123456789"))

	// The id and the grace period both go over the wire, so both are checked
	// here rather than trusted from the call.
	assert.Equal(t, "abcdef0123456789 t=10", <-daemon.stopped)
}

func TestClientReportsAFailedStop(t *testing.T) {
	daemon := startFakeDaemon(t)

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// A container that has gone away between being listed and being stopped —
	// the race the page cannot avoid, since the two are separate requests.
	err = client.Stop(ctx, "gonegonegone")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gonegonegone", "the error names the container")
	assert.Contains(t, err.Error(), "No such container")
}

// --- inspect, networks and volumes ------------------------------------------

func TestClientInspectsOneOfTheProjectsContainers(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.inspected = map[string]any{
		"Id":           "abcdef0123456789",
		"Name":         "/example-postgres-1",
		"Created":      "2026-08-01T10:00:00Z",
		"RestartCount": 2,
		"Platform":     "linux",
		"State": map[string]any{
			"Status": "running", "StartedAt": "2026-08-14T09:00:00Z",
			"FinishedAt": "0001-01-01T00:00:00Z", "ExitCode": 0,
			"Health": map[string]any{"Status": "healthy"},
		},
		"Config": map[string]any{
			"Image": "postgres:18",
			"Cmd":   []string{"postgres"},
			"Env":   []string{"POSTGRES_DB=example", "POSTGRES_PASSWORD=hunter2"},
			"Labels": map[string]string{
				labelProject: "example", labelService: "postgres", labelNumber: "1",
			},
		},
		"HostConfig": map[string]any{"RestartPolicy": map[string]any{"Name": "unless-stopped"}},
		"Mounts": []map[string]any{
			{"Type": "volume", "Name": "example_pgdata", "Destination": "/var/lib/postgresql", "RW": true},
		},
		"NetworkSettings": map[string]any{
			"Networks": map[string]any{
				"example": map[string]any{"NetworkID": "n1", "IPAddress": "172.20.0.2", "Aliases": []string{"postgres"}},
			},
		},
	}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	detail, err := client.Inspect(ctx, "abcdef0123456789")
	require.NoError(t, err)

	assert.Equal(t, "postgres", detail.Name)
	assert.Equal(t, "unless-stopped", detail.RestartPolicy)
	assert.Equal(t, 2, detail.RestartCount)
	assert.Equal(t, domain.HealthHealthy, detail.Health)
	assert.True(t, detail.Online())
	assert.True(t, detail.FinishedAt.IsZero(), "a container still running has no finish time")

	require.Len(t, detail.Mounts, 1)
	assert.Equal(t, "example_pgdata", detail.Mounts[0].Source)

	require.Len(t, detail.Networks, 1)
	assert.Equal(t, "172.20.0.2", detail.Networks[0].IPAddress)

	// The environment goes through the page, so the password must not.
	assert.Contains(t, detail.Environment, domain.Label{Key: "POSTGRES_DB", Value: "example"})
	assert.Contains(t, detail.Environment, domain.Label{Key: "POSTGRES_PASSWORD", Value: redacted})
}

func TestClientRefusesToInspectAContainerOfAnotherProject(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.inspected = map[string]any{
		"Id":     "abcdef0123456789",
		"Name":   "/somebody-elses",
		"State":  map[string]any{"Status": "running"},
		"Config": map[string]any{"Labels": map[string]string{labelProject: "another-project"}},
	}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Inspect takes any id the daemon knows — unlike list, it is not scoped by
	// a filter — so this check is the only thing keeping an id from a request
	// inside the project.
	_, err = client.Inspect(ctx, "abcdef0123456789")

	assert.ErrorIs(t, err, ports.ErrNotFound)
}

func TestNetworksListAndInspect(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.networks = []map[string]any{{
		"Id": "n1", "Name": "example_default", "Driver": "bridge", "Scope": "local",
		"Created": "2026-08-01T10:00:00Z", "Internal": false, "EnableIPv6": false,
		"IPAM":   map[string]any{"Config": []map[string]any{{"Subnet": "172.20.0.0/16", "Gateway": "172.20.0.1"}}},
		"Labels": map[string]string{labelProject: "example"},
		"Containers": map[string]any{
			"c1": map[string]any{"Name": "example-web-1", "IPv4Address": "172.20.0.2/16"},
		},
	}}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	networks := NewNetworks(ctx, client)

	listed, err := networks.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "example_default", listed[0].Name)

	inspected, err := networks.Inspect(ctx, "n1")
	require.NoError(t, err)
	require.Len(t, inspected.Subnets, 1)
	assert.Equal(t, "172.20.0.0/16", inspected.Subnets[0].Range)
	require.Len(t, inspected.Members, 1)
	assert.Equal(t, "example-web-1", inspected.Members[0].Name)
}

func TestNetworksRefuseOneOfAnotherProject(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.networks = []map[string]any{{
		"Id": "n9", "Name": "someone-elses", "Driver": "bridge",
		"IPAM":   map[string]any{},
		"Labels": map[string]string{labelProject: "another-project"},
	}}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = NewNetworks(ctx, client).Inspect(ctx, "n9")

	assert.ErrorIs(t, err, ports.ErrNotFound)
}

func TestVolumesListAndInspect(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.volumes = []map[string]any{{
		"Name": "example_pgdata", "Driver": "local", "Scope": "local",
		"Mountpoint": "/var/lib/docker/volumes/example_pgdata/_data",
		"CreatedAt":  "2026-08-01T10:00:00Z",
		"Labels":     map[string]string{labelProject: "example"},
		"Options":    map[string]string{},
	}}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	volumes := NewVolumes(ctx, client)

	listed, err := volumes.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "example_pgdata", listed[0].Name)

	inspected, err := volumes.Inspect(ctx, "example_pgdata")
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/docker/volumes/example_pgdata/_data", inspected.Mountpoint)
	assert.EqualValues(t, -1, inspected.Size, "usage is not measured unless it was asked for")
	assert.False(t, inspected.Created.IsZero())
}

func TestVolumesRefuseOneOfAnotherProject(t *testing.T) {
	daemon := startFakeDaemon(t)
	daemon.volumes = []map[string]any{{
		"Name": "someone-elses", "Driver": "local",
		"Labels": map[string]string{labelProject: "another-project"},
	}}

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = NewVolumes(ctx, client).Inspect(ctx, "someone-elses")

	assert.ErrorIs(t, err, ports.ErrNotFound)
}

func TestClientRemovesAContainerWithoutItsVolumes(t *testing.T) {
	daemon := startFakeDaemon(t)

	ctx := daemonContext(t, daemon)
	client, err := New(ctx)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	require.NoError(t, client.Remove(ctx, "abcdef0123456789"))

	// Neither option is set, and both matter enough to assert on the wire
	// rather than trust from the call: `v` would delete the container's data,
	// and `force` would let it remove a running container, defeating the
	// check the application layer makes first.
	assert.Equal(t, `abcdef0123456789 v="" force=""`, <-daemon.removed)
}
