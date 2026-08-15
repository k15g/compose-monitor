// Package realtime pushes changes to connected browsers over Server-Sent
// Events, and renders the fragments those events carry.
package realtime

import (
	"context"
	"sync"

	"github.com/k15g/compose-monitor/internal/domain"
	"github.com/k15g/compose-monitor/internal/ports"
)

// subscriberBuffer is how many events a subscriber may fall behind by before
// events start being dropped for it. A browser that cannot keep up with this
// is one that will be corrected by the snapshot it gets on its next
// connection, which is a better outcome than stalling every other client.
const subscriberBuffer = 16

// Hub fans events out to every connected client, in memory.
//
// It implements ports.EventBroadcaster.
type Hub struct {
	mu     sync.Mutex
	subs   map[int]chan domain.Event
	nextID int
	closed bool
}

var _ ports.EventBroadcaster = (*Hub)(nil)

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[int]chan domain.Event{}}
}

// Publish delivers the event to every current subscriber.
//
// A subscriber whose buffer is full is skipped rather than waited for, so one
// slow client cannot hold up the read loop or any other client.
func (h *Hub) Publish(_ context.Context, event domain.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil
	}

	for _, sub := range h.subs {
		select {
		case sub <- event:
		default:
		}
	}
	return nil
}

// Subscribe registers a subscriber and returns its channel with the function
// that unregisters it.
//
// Subscribing to a closed hub returns an already-closed channel, so a caller
// racing with shutdown ends its loop immediately instead of blocking forever.
func (h *Hub) Subscribe(_ context.Context) (<-chan domain.Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		closed := make(chan domain.Event)
		close(closed)
		return closed, func() {}
	}

	id := h.nextID
	h.nextID++

	ch := make(chan domain.Event, subscriberBuffer)
	h.subs[id] = ch

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if sub, ok := h.subs[id]; ok {
				delete(h.subs, id)
				close(sub)
			}
		})
	}
}

// Close ends every subscription. Handlers see their channel close and return,
// which is what lets the HTTP server's graceful shutdown finish instead of
// waiting out every open event stream.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil
	}
	h.closed = true

	for id, sub := range h.subs {
		delete(h.subs, id)
		close(sub)
	}
	return nil
}

// Subscribers reports how many clients are currently connected.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
