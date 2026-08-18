package server

import (
	"sync"

	"github.com/KonMam/tether/internal/agent"
)

// hub fans one session's event stream out to any number of SSE
// subscribers. Slow subscribers get events dropped rather than blocking
// the agent: the UI reloads full state on reconnect, and the durable
// record is on disk regardless.
type hub struct {
	mu   sync.Mutex
	subs map[chan agent.Event]struct{}
}

func newHub() *hub {
	return &hub{subs: map[chan agent.Event]struct{}{}}
}

func (h *hub) subscribe() chan agent.Event {
	ch := make(chan agent.Event, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan agent.Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *hub) broadcast(ev agent.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // subscriber too slow; drop
		}
	}
}
