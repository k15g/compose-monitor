package ports

import (
	"context"

	"github.com/k15g/compose-monitor/internal/domain"
)

// EventBroadcaster fans a change out to every connected client.
//
// Delivery is best-effort: a subscriber that cannot keep up misses events
// rather than blocking the publisher, and nothing is retained for a
// subscriber that is not connected yet.
type EventBroadcaster interface {
	// Publish delivers the event to every current subscriber. It does not
	// block on a slow subscriber.
	Publish(ctx context.Context, event domain.Event) error

	// Subscribe registers a new subscriber and returns its channel together
	// with a function that unregisters it. The returned function is safe to
	// call more than once, and must be called to release the subscription.
	Subscribe(ctx context.Context) (<-chan domain.Event, func())
}
