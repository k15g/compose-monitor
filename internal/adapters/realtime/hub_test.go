package realtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k15g/compose-monitor/internal/domain"
)

func event(id string) domain.Event {
	return domain.Event{
		Action:  domain.ActionUpdated,
		Service: domain.Service{ContainerID: id, Name: "web"},
	}
}

func TestHubDeliversToEverySubscriber(t *testing.T) {
	ctx := t.Context()
	hub := NewHub()
	defer func() { _ = hub.Close() }()

	first, unsubFirst := hub.Subscribe(ctx)
	defer unsubFirst()
	second, unsubSecond := hub.Subscribe(ctx)
	defer unsubSecond()

	require.NoError(t, hub.Publish(ctx, event("c1")))

	assert.Equal(t, event("c1"), <-first)
	assert.Equal(t, event("c1"), <-second)
	assert.Equal(t, 2, hub.Subscribers())
}

func TestHubDropsForASubscriberThatCannotKeepUp(t *testing.T) {
	ctx := t.Context()
	hub := NewHub()
	defer func() { _ = hub.Close() }()

	slow, unsubscribe := hub.Subscribe(ctx)
	defer unsubscribe()

	// One more than the buffer holds. The publisher must not block on the
	// overflow — a stalled browser tab cannot be allowed to stop the watch
	// loop or any other client.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range subscriberBuffer + 5 {
			_ = hub.Publish(ctx, event(string(rune('a'+i))))
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a subscriber that was not reading")
	}

	assert.Len(t, slow, subscriberBuffer, "the subscriber keeps a buffer's worth and no more")
}

func TestHubUnsubscribeIsIdempotent(t *testing.T) {
	ctx := t.Context()
	hub := NewHub()
	defer func() { _ = hub.Close() }()

	events, unsubscribe := hub.Subscribe(ctx)
	unsubscribe()

	assert.NotPanics(t, unsubscribe, "unsubscribing twice must be safe")
	assert.Equal(t, 0, hub.Subscribers())

	_, open := <-events
	assert.False(t, open, "unsubscribing closes the channel")

	// Publishing to a hub with no subscribers is a no-op, not an error.
	assert.NoError(t, hub.Publish(ctx, event("c1")))
}

func TestHubCloseEndsEverySubscription(t *testing.T) {
	ctx := t.Context()
	hub := NewHub()

	events, unsubscribe := hub.Subscribe(ctx)
	defer unsubscribe()

	require.NoError(t, hub.Close())

	_, open := <-events
	assert.False(t, open, "closing the hub closes every subscriber, which is what lets shutdown finish")

	assert.NoError(t, hub.Close(), "closing twice is safe")
	assert.NoError(t, hub.Publish(ctx, event("c1")), "publishing after close is a no-op")
}

func TestHubSubscribeAfterCloseReturnsAClosedChannel(t *testing.T) {
	ctx := t.Context()
	hub := NewHub()
	require.NoError(t, hub.Close())

	events, unsubscribe := hub.Subscribe(ctx)
	defer unsubscribe()

	select {
	case _, open := <-events:
		assert.False(t, open)
	case <-time.After(time.Second):
		t.Fatal("subscribing to a closed hub must not block the caller forever")
	}
}
