// Package app holds the service's business logic. It depends on internal/ports
// and internal/domain, and on nothing else — no HTTP, no container runtime.
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/k15g/compose-monitor/internal/config"
	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// MonitorService keeps a view of one compose project's services and announces
// every change to it.
//
// It holds the last observation so it can say what changed rather than only
// what is. That comparison is the reason this lives here and not in the
// container adapter: what counts as a change, and what counts as online, are
// decisions about the product, not about Docker.
// Errors a caller is expected to tell apart, because each is a different
// answer to the user rather than a failure to report.
var (
	// ErrControlDisabled means the deployment does not allow acting on
	// containers at all.
	ErrControlDisabled = errors.New("container control is disabled")

	// ErrNotInProject means the container is not one of this project's. It is
	// what stops an id from the request being used to reach a container the
	// service was never pointed at.
	ErrNotInProject = errors.New("container does not belong to this project")

	// ErrNotRunning means the container is not up, so there is nothing to stop.
	ErrNotRunning = errors.New("container is not running")

	// ErrAlreadyRunning means the container is already up, so there is nothing
	// to start.
	ErrAlreadyRunning = errors.New("container is already running")

	// ErrStillRunning means the container is up and so cannot be removed. It
	// is separate from ErrAlreadyRunning because the answer to the user is
	// different: stop it first.
	ErrStillRunning = errors.New("container is still running")

	// ErrInUse means a network or volume is still referred to by a container,
	// so removing it would fail — and would be the wrong thing to do anyway.
	ErrInUse = errors.New("still in use by a container")
)

type MonitorService struct {
	source      ports.ContainerSource
	control     ports.ContainerControl
	broadcaster ports.EventBroadcaster

	interval time.Duration
	debounce time.Duration

	mu       sync.Mutex
	snapshot map[string]domain.Service
}

// NewMonitorService creates the service.
//
// control may be nil, and is when the deployment does not allow acting on
// containers — Stop then refuses rather than the service holding a handle it
// must remember not to use.
func NewMonitorService(
	ctx context.Context,
	source ports.ContainerSource,
	control ports.ContainerControl,
	broadcaster ports.EventBroadcaster,
) *MonitorService {
	cfg := config.GetConfig(ctx)
	return &MonitorService{
		source:      source,
		control:     control,
		broadcaster: broadcaster,
		interval:    cfg.Project.Interval,
		debounce:    cfg.Project.Debounce,
		snapshot:    map[string]domain.Service{},
	}
}

// List reads the project's services and returns them in display order.
//
// It always goes to the source rather than answering from the last
// observation, so a page load and a freshly opened event stream both start
// from the truth even if the watch has silently stopped delivering.
func (s *MonitorService) List(ctx context.Context) ([]domain.Service, error) {
	services, err := s.source.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	SortServices(services)
	return services, nil
}

// CanControl reports whether this deployment may act on containers. The page
// asks so it does not draw a button that the endpoint behind it would refuse.
func (s *MonitorService) CanControl() bool {
	return s.control != nil
}

// Stop stops one of the project's containers.
func (s *MonitorService) Stop(ctx context.Context, containerID string) error {
	target, err := s.controllable(ctx, containerID)
	if err != nil {
		return err
	}
	if !target.Online() {
		return fmt.Errorf("%w: %s is %s", ErrNotRunning, target.Name, target.State)
	}

	if err := s.control.Stop(ctx, containerID); err != nil {
		return fmt.Errorf("stopping %s: %w", target.Name, err)
	}
	slog.InfoContext(ctx, "stopped service", "service", target.Name, "container", containerID)

	s.announce(ctx, "a stop")
	return nil
}

// Start starts one of the project's containers.
func (s *MonitorService) Start(ctx context.Context, containerID string) error {
	target, err := s.controllable(ctx, containerID)
	if err != nil {
		return err
	}
	if target.Online() {
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, target.Name)
	}

	if err := s.control.Start(ctx, containerID); err != nil {
		return fmt.Errorf("starting %s: %w", target.Name, err)
	}
	slog.InfoContext(ctx, "started service", "service", target.Name, "container", containerID)

	s.announce(ctx, "a start")
	return nil
}

// Remove deletes one of the project's containers.
//
// Only a stopped container can be removed, and only the container: its volumes
// are left alone. Removing is the one action here that cannot be undone — a
// stopped container can be started again, a removed one cannot — so it refuses
// rather than forcing, and the runtime is asked to refuse too.
func (s *MonitorService) Remove(ctx context.Context, containerID string) error {
	target, err := s.controllable(ctx, containerID)
	if err != nil {
		return err
	}
	if target.Online() {
		return fmt.Errorf("%w: %s", ErrStillRunning, target.Name)
	}

	if err := s.control.Remove(ctx, containerID); err != nil {
		return fmt.Errorf("removing %s: %w", target.Name, err)
	}
	slog.InfoContext(ctx, "removed service", "service", target.Name, "container", containerID)

	s.announce(ctx, "a removal")
	return nil
}

// controllable resolves the container an action names, and refuses if acting
// on containers is off or the container is not one of the project's.
//
// The lookup is a fresh, project-scoped read rather than the id being trusted
// from the request, so an id naming a container outside the project finds
// nothing. That check is the whole of these endpoints' authorisation, which is
// why it is here and not in the adapter: the rule is that this service may
// only touch what it watches, and it must hold wherever the call comes from.
func (s *MonitorService) controllable(ctx context.Context, containerID string) (*domain.Service, error) {
	if s.control == nil {
		return nil, ErrControlDisabled
	}

	services, err := s.source.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing services before acting: %w", err)
	}

	for i := range services {
		if services[i].ContainerID == containerID {
			return &services[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotInProject, containerID)
}

// announce publishes the new state now rather than waiting for the runtime's
// event to arrive and the debounce to elapse. The runtime's event follows and
// finds nothing further changed.
func (s *MonitorService) announce(ctx context.Context, what string) {
	if err := s.refresh(ctx); err != nil {
		slog.ErrorContext(ctx, "reading project after "+what+" failed", "error", err)
	}
}

// Inspect returns everything known about one of the project's services.
//
// Scoping to the project is the source's job here, because only it can tell
// whose a container is — an id from a request that names something outside the
// project comes back as not found.
func (s *MonitorService) Inspect(ctx context.Context, containerID string) (domain.ServiceDetail, error) {
	detail, err := s.source.Inspect(ctx, containerID)
	if err != nil {
		return domain.ServiceDetail{}, fmt.Errorf("inspecting service: %w", err)
	}
	return detail, nil
}

// Log tail bounds. A page has to show a finite amount, and the numbers are a
// product decision rather than a runtime limit, so they live here.
const (
	// DefaultLogTail is how much of a container's output the detail page shows
	// when nothing else was asked for.
	DefaultLogTail = 200
	// MaxLogTail caps what can be asked for, so a query parameter cannot make
	// the service read and render an unbounded log.
	MaxLogTail = 5000
)

// Logs returns the tail of one of the project's containers.
//
// The requested size is clamped rather than rejected: the number arrives in a
// query parameter, and a page that renders slightly less than was asked for is
// a better answer than an error.
func (s *MonitorService) Logs(ctx context.Context, containerID string, tail int) (domain.Logs, error) {
	logs, err := s.source.Logs(ctx, containerID, ClampLogTail(tail))
	if err != nil {
		return domain.Logs{}, fmt.Errorf("reading logs: %w", err)
	}
	return logs, nil
}

// ClampLogTail brings a requested tail size within what the page will show.
func ClampLogTail(tail int) int {
	switch {
	case tail <= 0:
		return DefaultLogTail
	case tail > MaxLogTail:
		return MaxLogTail
	default:
		return tail
	}
}

// Run watches the project until ctx is cancelled, publishing an event for
// every change. It returns ctx.Err() when it stops.
//
// Errors reading the project are logged and retried rather than returned:
// a runtime that is briefly unreachable should leave the page stale, not
// bring the process down.
func (s *MonitorService) Run(ctx context.Context) error {
	if err := s.refresh(ctx); err != nil {
		slog.ErrorContext(ctx, "initial read of project failed", "error", err)
	}

	changes := s.source.Watch(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Created stopped: the timer is armed by an incoming change and disarmed
	// by firing. A change arriving while it is already armed is therefore
	// absorbed, which turns the burst of events a `compose up` produces into
	// a single read — and, because the timer is not re-armed on every change,
	// a continuous stream still gets read once per debounce rather than being
	// starved indefinitely.
	debounce := time.NewTimer(s.debounce)
	if !debounce.Stop() {
		<-debounce.C
	}
	defer debounce.Stop()
	armed := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case _, ok := <-changes:
			if !ok {
				// The source is finished. Receiving from a nil channel blocks
				// forever, so this case simply stops firing and the ticker
				// carries the loop.
				changes = nil
				continue
			}
			if !armed {
				debounce.Reset(s.debounce)
				armed = true
			}

		case <-debounce.C:
			armed = false
			if err := s.refresh(ctx); err != nil {
				slog.ErrorContext(ctx, "reading project after change failed", "error", err)
			}

		case <-ticker.C:
			if err := s.refresh(ctx); err != nil {
				slog.ErrorContext(ctx, "periodic read of project failed", "error", err)
			}
		}
	}
}

// refresh reads the project, compares it with the last observation, and
// publishes what changed.
func (s *MonitorService) refresh(ctx context.Context) error {
	services, err := s.source.List(ctx)
	if err != nil {
		return fmt.Errorf("listing services: %w", err)
	}

	next := IndexServices(services)

	s.mu.Lock()
	events := DiffSnapshots(s.snapshot, next)
	s.snapshot = next
	s.mu.Unlock()

	for _, event := range events {
		if err := s.broadcaster.Publish(ctx, event); err != nil {
			slog.ErrorContext(ctx, "publishing event failed",
				"service", event.Service.Name, "action", event.Action, "error", err)
		}
	}

	slog.DebugContext(ctx, "project read", "services", len(next), "events", len(events))
	return nil
}

// Summarize counts services by whether they are up.
func Summarize(services []domain.Service) domain.Summary {
	summary := domain.Summary{Total: len(services)}
	for _, service := range services {
		if service.Online() {
			summary.Online++
		} else {
			summary.Offline++
		}
	}
	return summary
}

// IndexServices keys services by container ID, which is what identifies a row.
func IndexServices(services []domain.Service) map[string]domain.Service {
	indexed := make(map[string]domain.Service, len(services))
	for _, service := range services {
		indexed[service.ContainerID] = service
	}
	return indexed
}

// SortServices puts services in display order: by declared name, then by
// replica number, then by container ID so the order is total and stable even
// for two containers that agree on everything else.
func SortServices(services []domain.Service) {
	slices.SortFunc(services, func(a, b domain.Service) int {
		return cmp.Or(
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Number, b.Number),
			cmp.Compare(a.ContainerID, b.ContainerID),
		)
	})
}

// DiffSnapshots reports what changed between two observations, in display
// order. Events carry no HTML: rendering is the caller's step.
//
// Every difference produces an event, so the page stays current; each one says
// whether it is worth drawing attention to.
func DiffSnapshots(previous, next map[string]domain.Service) []domain.Event {
	events := make([]domain.Event, 0, len(next))

	for id, service := range next {
		before, existed := previous[id]
		switch {
		case !existed:
			// A service appearing is always news.
			events = append(events, domain.Event{
				Action: domain.ActionAdded, Service: service, Notable: true,
			})
		case Changed(before, service):
			events = append(events, domain.Event{
				Action:  domain.ActionUpdated,
				Service: service,
				Notable: Notable(before, service),
			})
		}
	}

	for id, service := range previous {
		if _, still := next[id]; !still {
			events = append(events, domain.Event{
				Action: domain.ActionRemoved, Service: service, Notable: true,
			})
		}
	}

	slices.SortFunc(events, func(a, b domain.Event) int {
		return cmp.Or(
			cmp.Compare(a.Service.Name, b.Service.Name),
			cmp.Compare(a.Service.Number, b.Service.Number),
			cmp.Compare(a.Service.ContainerID, b.Service.ContainerID),
		)
	})
	return events
}

// Changed reports whether anything the page shows differs between two
// observations of the same container, and so whether the row has to be redrawn.
//
// Status counts here even though it is mostly a rendering of elapsed time: it
// is on the page, and a page that says "Up 3 minutes" an hour later is wrong.
// Created is not compared, being fixed for the life of a container.
func Changed(before, after domain.Service) bool {
	return Notable(before, after) || before.Status != after.Status
}

// Notable reports whether something happened to the container, as opposed to
// the clock advancing under it.
//
// It is the same comparison as Changed but against StatusKind, which is the
// status with the elapsed time taken out. That is the difference between "Up 3
// minutes" becoming "Up 4 minutes" — which is not news — and it becoming
// "Exited (0)", which is.
func Notable(before, after domain.Service) bool {
	return before.Name != after.Name ||
		before.Number != after.Number ||
		before.ContainerName != after.ContainerName ||
		before.Image != after.Image ||
		before.State != after.State ||
		before.StatusKind != after.StatusKind ||
		before.Health != after.Health ||
		!slices.Equal(before.Ports, after.Ports)
}
