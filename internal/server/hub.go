package server

import (
	"sync"

	"github.com/KonMam/buntline/internal/agent"
)

// hub fans one event stream out to any number of SSE subscribers. Slow
// subscribers get events dropped rather than blocking the producer: the
// UI reloads full state on reconnect, and the durable record is on disk
// regardless.
type hub[T any] struct {
	mu   sync.Mutex
	subs map[chan T]struct{}
}

func newHub[T any]() *hub[T] {
	return &hub[T]{subs: map[chan T]struct{}{}}
}

func (h *hub[T]) subscribe() chan T {
	ch := make(chan T, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub[T]) unsubscribe(ch chan T) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *hub[T]) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *hub[T]) broadcast(ev T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // subscriber too slow; drop
		}
	}
}

// globalEvent wraps an agent event with the session it came from, for
// the cross-session stream /api/events. One subscriber watches the whole
// harness: the notification bell needs events from sessions the UI is
// not currently showing. Token deltas are excluded upstream, so the
// global stream carries structure, not chatter.
type globalEvent struct {
	SessionID string      `json:"session_id"`
	Event     agent.Event `json:"event"`
}
