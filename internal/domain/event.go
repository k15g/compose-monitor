package domain

// Action is what happened to a service between two observations.
type Action string

const (
	// ActionAdded means a service appeared that was not there before.
	ActionAdded Action = "added"
	// ActionUpdated means a service that was already known changed.
	ActionUpdated Action = "updated"
	// ActionRemoved means a known service is gone: its container no longer
	// exists, which is what `compose down` or `rm` leaves behind.
	ActionRemoved Action = "removed"
)

// Event is a single change.
//
// It carries the service and not a rendering of it. A service is drawn
// differently on different pages, and which page a subscriber is on is
// something only that subscriber's connection knows — so rendering happens
// there, on the way out, rather than here.
type Event struct {
	Action  Action
	Service Service

	// Notable is whether something happened to the service, as opposed to the
	// clock advancing under it. An update carrying only a new elapsed time is
	// still sent — the page would otherwise say "Up 3 minutes" forever — but
	// it is not something to draw the eye to.
	Notable bool
}
