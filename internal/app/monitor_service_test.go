package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// --- test doubles ----------------------------------------------------------

type fakeSource struct {
	mu       sync.Mutex
	services []domain.Service
	err      error
	calls    int
	changes  chan struct{}
	logTail  int
	logsErr  error
	usage    domain.ResourceUsage
}

func newFakeSource(services ...domain.Service) *fakeSource {
	return &fakeSource{services: services, changes: make(chan struct{}, 8)}
}

func (f *fakeSource) List(context.Context) ([]domain.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]domain.Service(nil), f.services...), nil
}

func (f *fakeSource) Inspect(_ context.Context, containerID string) (domain.ServiceDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.ServiceDetail{}, f.err
	}
	for _, service := range f.services {
		if service.ContainerID == containerID {
			return domain.ServiceDetail{Service: service}, nil
		}
	}
	return domain.ServiceDetail{}, fmt.Errorf("%w: %s", ports.ErrNotFound, containerID)
}

func (f *fakeSource) Logs(_ context.Context, containerID string, tail int) (domain.Logs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logTail = tail
	if f.logsErr != nil {
		return domain.Logs{}, f.logsErr
	}
	return domain.Logs{
		Lines: []domain.LogLine{{Stream: domain.LogStreamStdout, Text: "log of " + containerID}},
		Tail:  tail,
	}, nil
}

func (f *fakeSource) requestedTail() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logTail
}

func (f *fakeSource) Usage(context.Context) (domain.ResourceUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.ResourceUsage{}, f.err
	}
	return f.usage, nil
}

func (f *fakeSource) Watch(context.Context) <-chan struct{} { return f.changes }

func (f *fakeSource) set(services ...domain.Service) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services = services
}

func (f *fakeSource) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeBroadcaster struct {
	mu     sync.Mutex
	events []domain.Event
}

func (f *fakeBroadcaster) Publish(_ context.Context, event domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *fakeBroadcaster) Subscribe(context.Context) (<-chan domain.Event, func()) {
	return make(chan domain.Event), func() {}
}

func (f *fakeBroadcaster) published() []domain.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Event(nil), f.events...)
}

type fakeControl struct {
	mu      sync.Mutex
	stopped []string
	started []string
	removed []string
	err     error

	// onAction stands in for the runtime actually starting or stopping the
	// container, so a test can make the next read reflect it.
	onAction func()
}

func (f *fakeControl) Start(_ context.Context, containerID string) error {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return f.err
	}
	f.started = append(f.started, containerID)
	onAction := f.onAction
	f.mu.Unlock()

	if onAction != nil {
		onAction()
	}
	return nil
}

func (f *fakeControl) startCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}

func (f *fakeControl) Remove(_ context.Context, containerID string) error {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return f.err
	}
	f.removed = append(f.removed, containerID)
	onAction := f.onAction
	f.mu.Unlock()

	if onAction != nil {
		onAction()
	}
	return nil
}

func (f *fakeControl) removeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *fakeControl) Stop(_ context.Context, containerID string) error {
	f.mu.Lock()
	if f.err != nil {
		f.mu.Unlock()
		return f.err
	}
	f.stopped = append(f.stopped, containerID)
	onAction := f.onAction
	f.mu.Unlock()

	if onAction != nil {
		onAction()
	}
	return nil
}

func (f *fakeControl) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

// --- helpers ---------------------------------------------------------------

func testContext(t *testing.T) context.Context {
	t.Helper()
	return config.WithConfig(t.Context(), &config.Config{
		Project: config.ProjectConfig{
			Name:     "example",
			Interval: time.Hour,
			Debounce: 10 * time.Millisecond,
		},
	})
}

func service(id, name string, state domain.State) domain.Service {
	return domain.Service{
		ContainerID: id,
		Name:        name,
		Number:      1,
		State:       state,
		Status:      "Up 1 minute",
		StatusKind:  "Up",
	}
}

// aged is the same service one poll later: nothing has happened to it, but the
// clock in its status has moved on.
func aged(s domain.Service, status string) domain.Service {
	s.Status = status
	return s
}

// --- pure logic ------------------------------------------------------------

func TestDiffSnapshots(t *testing.T) {
	web := service("c1", "web", domain.StateRunning)
	db := service("c2", "db", domain.StateRunning)
	dbStopped := db
	dbStopped.State = domain.StateExited

	tests := []struct {
		name     string
		previous []domain.Service
		next     []domain.Service
		want     []domain.Event
	}{
		{
			name: "empty to empty",
		},
		{
			name: "first observation is all additions",
			next: []domain.Service{web, db},
			want: []domain.Event{
				{Action: domain.ActionAdded, Service: db},
				{Action: domain.ActionAdded, Service: web},
			},
		},
		{
			name:     "unchanged produces nothing",
			previous: []domain.Service{web, db},
			next:     []domain.Service{web, db},
		},
		{
			name:     "state change is an update",
			previous: []domain.Service{web, db},
			next:     []domain.Service{web, dbStopped},
			want:     []domain.Event{{Action: domain.ActionUpdated, Service: dbStopped}},
		},
		{
			name:     "vanished container is a removal",
			previous: []domain.Service{web, db},
			next:     []domain.Service{web},
			want:     []domain.Event{{Action: domain.ActionRemoved, Service: db}},
		},
		{
			name:     "recreated container is a removal and an addition",
			previous: []domain.Service{service("old", "web", domain.StateRunning)},
			next:     []domain.Service{service("new", "web", domain.StateRunning)},
			want: []domain.Event{
				{Action: domain.ActionAdded, Service: service("new", "web", domain.StateRunning)},
				{Action: domain.ActionRemoved, Service: service("old", "web", domain.StateRunning)},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DiffSnapshots(IndexServices(test.previous), IndexServices(test.next))
			assert.Equal(t, test.want, nonEmpty(got))
		})
	}
}

// nonEmpty normalises the empty slice DiffSnapshots returns to nil, so a test
// case that expects no events can leave the field out.
//
// It also clears Notable. This test is about which events come out of a diff;
// whether each one is worth looking at is its own test below, and repeating the
// flag in every literal here would only obscure what is being checked.
func nonEmpty(events []domain.Event) []domain.Event {
	if len(events) == 0 {
		return nil
	}
	for i := range events {
		events[i].Notable = false
	}
	return events
}

func TestChanged(t *testing.T) {
	base := domain.Service{
		ContainerID:   "c1",
		Name:          "web",
		Number:        1,
		ContainerName: "example-web-1",
		Image:         "nginx:1",
		State:         domain.StateRunning,
		Status:        "Up 2 minutes",
		Health:        domain.HealthHealthy,
		Ports:         []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}},
		Created:       time.Unix(1000, 0),
	}

	tests := []struct {
		name   string
		mutate func(*domain.Service)
		want   bool
	}{
		{name: "identical", mutate: func(*domain.Service) {}, want: false},
		{name: "created is ignored", mutate: func(s *domain.Service) { s.Created = time.Unix(9999, 0) }, want: false},
		{name: "state", mutate: func(s *domain.Service) { s.State = domain.StateExited }, want: true},
		{name: "status", mutate: func(s *domain.Service) { s.Status = "Up 3 minutes" }, want: true},
		{name: "health", mutate: func(s *domain.Service) { s.Health = domain.HealthUnhealthy }, want: true},
		{name: "image", mutate: func(s *domain.Service) { s.Image = "nginx:2" }, want: true},
		{name: "name", mutate: func(s *domain.Service) { s.Name = "api" }, want: true},
		{name: "number", mutate: func(s *domain.Service) { s.Number = 2 }, want: true},
		{name: "container name", mutate: func(s *domain.Service) { s.ContainerName = "example-web-2" }, want: true},
		{name: "ports", mutate: func(s *domain.Service) { s.Ports = nil }, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			after := base
			after.Ports = append([]domain.Port(nil), base.Ports...)
			test.mutate(&after)
			assert.Equal(t, test.want, Changed(base, after))
		})
	}
}

func TestSortServices(t *testing.T) {
	services := []domain.Service{
		{Name: "web", Number: 10, ContainerID: "c"},
		{Name: "db", Number: 1, ContainerID: "b"},
		{Name: "web", Number: 2, ContainerID: "a"},
	}

	SortServices(services)

	assert.Equal(t, []string{"db", "web", "web"}, names(services))
	assert.Equal(t, []int{1, 2, 10}, numbers(services), "replica 10 must sort after replica 2")
}

func names(services []domain.Service) []string {
	out := make([]string, len(services))
	for i, s := range services {
		out[i] = s.Name
	}
	return out
}

func numbers(services []domain.Service) []int {
	out := make([]int, len(services))
	for i, s := range services {
		out[i] = s.Number
	}
	return out
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name     string
		services []domain.Service
		want     domain.Summary
	}{
		{name: "none", want: domain.Summary{}},
		{
			name: "mixed",
			services: []domain.Service{
				service("c1", "web", domain.StateRunning),
				service("c2", "db", domain.StateExited),
				service("c3", "cache", domain.StateRestarting),
			},
			want: domain.Summary{Total: 3, Online: 1, Offline: 2},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, Summarize(test.services))
		})
	}
}

func TestStateOnline(t *testing.T) {
	tests := []struct {
		state domain.State
		want  bool
	}{
		{domain.StateRunning, true},
		{domain.StateExited, false},
		{domain.StateCreated, false},
		{domain.StatePaused, false},
		{domain.StateRestarting, false},
		{domain.StateDead, false},
		{domain.StateRemoving, false},
		{domain.StateUnknown, false},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			assert.Equal(t, test.want, test.state.Online())
		})
	}
}

// --- service behaviour -----------------------------------------------------

func TestMonitorServiceList(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(
		service("c1", "web", domain.StateRunning),
		service("c2", "db", domain.StateExited),
	)
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	services, err := monitor.List(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"db", "web"}, names(services), "List returns display order")
}

func TestMonitorServiceListError(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource()
	source.fail(errors.New("socket gone"))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	_, err := monitor.List(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "socket gone")
}

func TestMonitorServiceRefreshPublishesWhatChanged(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, nil, broadcaster)

	require.NoError(t, monitor.refresh(ctx))

	events := broadcaster.published()
	require.Len(t, events, 1)
	assert.Equal(t, domain.ActionAdded, events[0].Action)

	// The event carries the service, not a rendering of it: a service is drawn
	// differently on different pages, and which page a subscriber is on is
	// something only that subscriber's connection knows.
	assert.Equal(t, "web", events[0].Service.Name)
}

func TestMonitorServiceRefreshIsQuietWhenNothingChanged(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, nil, broadcaster)

	require.NoError(t, monitor.refresh(ctx))
	require.NoError(t, monitor.refresh(ctx))

	assert.Len(t, broadcaster.published(), 1, "the second read of an unchanged project publishes nothing")
}

func TestMonitorServiceReportsARemoval(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, nil, broadcaster)

	require.NoError(t, monitor.refresh(ctx))
	source.set()
	require.NoError(t, monitor.refresh(ctx))

	events := broadcaster.published()
	require.Len(t, events, 2)
	assert.Equal(t, domain.ActionRemoved, events[1].Action)
	assert.Equal(t, "web", events[1].Service.Name)
}

func TestMonitorServiceRefreshReturnsSourceError(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource()
	source.fail(errors.New("socket gone"))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	err := monitor.refresh(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "socket gone")
}

func TestMonitorServiceRunReactsToChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	source := newFakeSource(service("c1", "web", domain.StateRunning))
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, nil, broadcaster)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = monitor.Run(ctx)
	}()

	// The initial read publishes the service as added.
	assert.Eventually(t, func() bool {
		return len(broadcaster.published()) == 1
	}, time.Second, 5*time.Millisecond)

	source.set(service("c1", "web", domain.StateExited))
	source.changes <- struct{}{}

	assert.Eventually(t, func() bool {
		events := broadcaster.published()
		return len(events) == 2 && events[1].Action == domain.ActionUpdated
	}, time.Second, 5*time.Millisecond)

	cancel()
	<-done
}

func TestMonitorServiceRunCoalescesBurstsOfChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	source := newFakeSource(service("c1", "web", domain.StateRunning))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = monitor.Run(ctx)
	}()

	// Wait for the initial read so the burst below is the only thing left to
	// account for.
	require.Eventually(t, func() bool { return source.callCount() == 1 }, time.Second, 5*time.Millisecond)

	for range 8 {
		source.changes <- struct{}{}
	}

	// The debounce is 10ms; give it well over that and confirm the burst
	// turned into one read rather than eight.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 2, source.callCount(), "a burst of changes must collapse into a single read")

	cancel()
	<-done
}

func TestMonitorServiceRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	source := newFakeSource()
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	cancel()
	err := monitor.Run(ctx)

	assert.ErrorIs(t, err, context.Canceled)
}

// --- stopping ---------------------------------------------------------------

func TestStopRefusesWhenControlIsDisabled(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	err := monitor.Stop(ctx, "c1")

	assert.ErrorIs(t, err, ErrControlDisabled)
	assert.False(t, monitor.CanControl())
}

func TestStopRefusesAContainerOutsideTheProject(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	control := &fakeControl{}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	// The source lists only this project's containers, so an id naming
	// anything else finds nothing. This is the whole of the endpoint's
	// authorisation, so it is worth a test of its own.
	err := monitor.Stop(ctx, "a-container-in-another-project")

	assert.ErrorIs(t, err, ErrNotInProject)
	assert.Empty(t, control.calls(), "the runtime is never asked about a container we do not watch")
}

func TestStopRefusesAServiceThatIsNotRunning(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "db", domain.StateExited))
	control := &fakeControl{}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	err := monitor.Stop(ctx, "c1")

	assert.ErrorIs(t, err, ErrNotRunning)
	assert.Empty(t, control.calls())
}

func TestStopAnnouncesTheChangeWithoutWaitingForTheRuntime(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	control := &fakeControl{}
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, control, broadcaster)

	// Establish the baseline so the stop below is the only change left.
	require.NoError(t, monitor.refresh(ctx))
	require.Len(t, broadcaster.published(), 1)

	// The container is still running when Stop checks, and stopped by the time
	// the read that follows it happens — which is the order the real runtime
	// produces, and the order the pre-flight check depends on.
	control.onAction = func() { source.set(service("c1", "web", domain.StateExited)) }
	require.NoError(t, monitor.Stop(ctx, "c1"))

	assert.Equal(t, []string{"c1"}, control.calls())

	events := broadcaster.published()
	require.Len(t, events, 2, "the stop is announced immediately, not on the next tick")
	assert.Equal(t, domain.ActionUpdated, events[1].Action)
	assert.Equal(t, domain.StateExited, events[1].Service.State)
}

func TestStopReportsAFailureFromTheRuntime(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	control := &fakeControl{err: errors.New("daemon said no")}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	err := monitor.Stop(ctx, "c1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon said no")
	assert.Contains(t, err.Error(), "web", "the error names the service, not just the id")
}

func TestCanControlReflectsWhetherAHandleWasGiven(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource()

	assert.True(t, NewMonitorService(ctx, source, &fakeControl{}, &fakeBroadcaster{}).CanControl())
	assert.False(t, NewMonitorService(ctx, source, nil, &fakeBroadcaster{}).CanControl())
}

func TestInspectReturnsNotFoundForAContainerOutsideTheProject(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	_, err := monitor.Inspect(ctx, "someone-elses-container")

	assert.ErrorIs(t, err, ports.ErrNotFound)
}

func TestInspectReturnsTheService(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	detail, err := monitor.Inspect(ctx, "c1")

	require.NoError(t, err)
	assert.Equal(t, "web", detail.Name)
}

func TestStartStartsAStoppedService(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateExited))
	control := &fakeControl{}
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, control, broadcaster)

	require.NoError(t, monitor.refresh(ctx))
	control.onAction = func() { source.set(service("c1", "web", domain.StateRunning)) }

	require.NoError(t, monitor.Start(ctx, "c1"))

	assert.Equal(t, []string{"c1"}, control.startCalls())
	events := broadcaster.published()
	require.Len(t, events, 2, "the start is announced immediately")
	assert.Equal(t, domain.StateRunning, events[1].Service.State)
}

func TestStartRefusesAServiceAlreadyRunning(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	control := &fakeControl{}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	err := monitor.Start(ctx, "c1")

	assert.ErrorIs(t, err, ErrAlreadyRunning)
	assert.Empty(t, control.startCalls())
}

func TestStartRefusesAContainerOutsideTheProject(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateExited))
	control := &fakeControl{}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	err := monitor.Start(ctx, "someone-elses-container")

	assert.ErrorIs(t, err, ErrNotInProject)
	assert.Empty(t, control.startCalls())
}

func TestStartRefusesWhenControlIsDisabled(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateExited))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	assert.ErrorIs(t, monitor.Start(ctx, "c1"), ErrControlDisabled)
}

func TestClampLogTail(t *testing.T) {
	tests := []struct {
		name string
		tail int
		want int
	}{
		{"absent falls back to the default", 0, DefaultLogTail},
		{"negative falls back to the default", -5, DefaultLogTail},
		{"a reasonable number is kept", 500, 500},
		{"an unreasonable one is capped", 10_000_000, MaxLogTail},
		{"exactly the cap is kept", MaxLogTail, MaxLogTail},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ClampLogTail(test.tail))
		})
	}
}

func TestLogsClampWhatIsAskedFor(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	// A query parameter must not be able to make the service read an
	// unbounded log, and must not fail the page either.
	logs, err := monitor.Logs(ctx, "c1", 10_000_000)

	require.NoError(t, err)
	assert.Equal(t, MaxLogTail, source.requestedTail())
	assert.Equal(t, MaxLogTail, logs.Tail)
}

func TestLogsReportAFailure(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	source.logsErr = errors.New("container has no log")
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	_, err := monitor.Logs(ctx, "c1", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "container has no log")
}

func TestRemoveDeletesAStoppedService(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateExited))
	control := &fakeControl{}
	broadcaster := &fakeBroadcaster{}
	monitor := NewMonitorService(ctx, source, control, broadcaster)

	require.NoError(t, monitor.refresh(ctx))
	control.onAction = func() { source.set() }

	require.NoError(t, monitor.Remove(ctx, "c1"))

	assert.Equal(t, []string{"c1"}, control.removeCalls())

	events := broadcaster.published()
	require.Len(t, events, 2)
	assert.Equal(t, domain.ActionRemoved, events[1].Action,
		"the row goes away over the event stream, on every open page")
}

func TestRemoveRefusesARunningService(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateRunning))
	control := &fakeControl{}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	err := monitor.Remove(ctx, "c1")

	assert.ErrorIs(t, err, ErrStillRunning)
	assert.Empty(t, control.removeCalls(), "the runtime is never asked")
}

func TestRemoveRefusesAContainerOutsideTheProject(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateExited))
	control := &fakeControl{}
	monitor := NewMonitorService(ctx, source, control, &fakeBroadcaster{})

	err := monitor.Remove(ctx, "someone-elses-container")

	assert.ErrorIs(t, err, ErrNotInProject)
	assert.Empty(t, control.removeCalls())
}

func TestRemoveRefusesWhenControlIsDisabled(t *testing.T) {
	ctx := testContext(t)
	source := newFakeSource(service("c1", "web", domain.StateExited))
	monitor := NewMonitorService(ctx, source, nil, &fakeBroadcaster{})

	assert.ErrorIs(t, monitor.Remove(ctx, "c1"), ErrControlDisabled)
}

// --- what counts as news ----------------------------------------------------

func TestOnlyTheClockMovingIsAChangeButNotNews(t *testing.T) {
	before := service("c1", "web", domain.StateRunning)
	after := aged(before, "Up 2 minutes")

	// The row still has to be redrawn — a page that says "Up 1 minute" an hour
	// later is wrong — but nothing happened to the container.
	assert.True(t, Changed(before, after), "the page shows the status, so it must be redrawn")
	assert.False(t, Notable(before, after), "the clock advancing is not something to look at")
}

func TestNotable(t *testing.T) {
	base := domain.Service{
		ContainerID: "c1", Name: "web", Number: 1, ContainerName: "example-web-1",
		Image: "nginx:1", State: domain.StateRunning,
		Status: "Up 2 minutes", StatusKind: "Up", Health: domain.HealthHealthy,
		Ports: []domain.Port{{Host: 8080, Container: 80, Protocol: "tcp"}},
	}

	tests := []struct {
		name   string
		mutate func(*domain.Service)
		want   bool
	}{
		{"identical", func(*domain.Service) {}, false},
		{"only the elapsed time", func(s *domain.Service) { s.Status = "Up 3 hours" }, false},
		{"stopped", func(s *domain.Service) {
			s.State = domain.StateExited
			s.Status = "Exited (0) 1 second ago"
			s.StatusKind = "Exited (0)"
		}, true},
		{"a different exit code, still exited", func(s *domain.Service) {
			s.StatusKind = "Exited (137)"
		}, true},
		{"health", func(s *domain.Service) { s.Health = domain.HealthUnhealthy }, true},
		{"image", func(s *domain.Service) { s.Image = "nginx:2" }, true},
		{"ports", func(s *domain.Service) { s.Ports = nil }, true},
		{"container name", func(s *domain.Service) { s.ContainerName = "example-web-2" }, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			after := base
			after.Ports = append([]domain.Port(nil), base.Ports...)
			test.mutate(&after)

			assert.Equal(t, test.want, Notable(base, after))
			if test.want {
				assert.True(t, Changed(base, after), "anything notable is also a change")
			}
		})
	}
}

func TestEventsSayWhetherTheyAreWorthLookingAt(t *testing.T) {
	web := service("c1", "web", domain.StateRunning)
	stopped := service("c1", "web", domain.StateExited)
	stopped.Status = "Exited (0) 1 second ago"
	stopped.StatusKind = "Exited (0)"

	tests := []struct {
		name        string
		previous    []domain.Service
		next        []domain.Service
		wantAction  domain.Action
		wantNotable bool
	}{
		{"appeared", nil, []domain.Service{web}, domain.ActionAdded, true},
		{"vanished", []domain.Service{web}, nil, domain.ActionRemoved, true},
		{"stopped", []domain.Service{web}, []domain.Service{stopped}, domain.ActionUpdated, true},
		{
			"just the clock",
			[]domain.Service{web},
			[]domain.Service{aged(web, "Up 9 minutes")},
			domain.ActionUpdated, false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := DiffSnapshots(IndexServices(test.previous), IndexServices(test.next))

			require.Len(t, events, 1)
			assert.Equal(t, test.wantAction, events[0].Action)
			assert.Equal(t, test.wantNotable, events[0].Notable)
		})
	}
}
