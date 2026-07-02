// Package events is a tiny in-memory pub/sub used to push live updates to the
// SSE handlers. Publishing never blocks: a slow subscriber simply drops events.
package events

import "sync"

// Topics.
const (
	TopicRequests  = "requests"
	TopicInstances = "instances"
	TopicQueue     = "queue"
)

// Event carries a topic and an opaque payload.
type Event struct {
	Topic string
	Data  any
}

// Bus fans out events to all current subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// New returns an empty Bus.
func New() *Bus {
	return &Bus{subs: make(map[chan Event]struct{})}
}

// Subscribe returns a channel of events and an unsubscribe func the caller must
// invoke when done (e.g. on SSE disconnect).
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish delivers an event to every subscriber, dropping it for any subscriber
// whose buffer is full.
func (b *Bus) Publish(topic string, data any) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ev := Event{Topic: topic, Data: data}
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
