// Package hub fans committed board events out to live subscribers.
// See SCALING.md §8.
package hub

import (
	"sync"

	"github.com/iAccelTech/kanban/internal/store"
)

const bufferSize = 64

// Sub is one live stream for one board.
type Sub struct {
	C       chan store.Event
	boardID int64
	kicked  chan struct{}
	once    sync.Once
}

// Kicked is closed when the subscriber fell too far behind or the hub shut down.
// The client reconnects and replays what it missed from the event log.
func (s *Sub) Kicked() <-chan struct{} { return s.kicked }

func (s *Sub) kick() { s.once.Do(func() { close(s.kicked) }) }

type Hub struct {
	mu     sync.RWMutex
	subs   map[int64]map[*Sub]struct{}
	closed bool
}

func New() *Hub { return &Hub{subs: map[int64]map[*Sub]struct{}{}} }

func (h *Hub) Subscribe(boardID int64) *Sub {
	s := &Sub{C: make(chan store.Event, bufferSize), boardID: boardID, kicked: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.kick()
		return s
	}
	if h.subs[boardID] == nil {
		h.subs[boardID] = map[*Sub]struct{}{}
	}
	h.subs[boardID][s] = struct{}{}
	return s
}

func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(s)
}

func (h *Hub) remove(s *Sub) {
	if m := h.subs[s.boardID]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(h.subs, s.boardID)
		}
	}
	s.kick()
}

// Publish delivers events without ever blocking: a subscriber whose buffer is
// full is dropped rather than allowed to stall the writer.
func (h *Hub) Publish(events []store.Event) {
	var slow map[*Sub]bool
	h.mu.RLock()
	for _, ev := range events {
		for s := range h.subs[ev.BoardID] {
			if slow[s] {
				continue // never deliver past a gap
			}
			select {
			case s.C <- ev:
			default:
				if slow == nil {
					slow = map[*Sub]bool{}
				}
				slow[s] = true
			}
		}
	}
	h.mu.RUnlock()
	if len(slow) > 0 {
		h.mu.Lock()
		for s := range slow {
			h.remove(s)
		}
		h.mu.Unlock()
	}
}

// Count returns the number of live subscribers.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, m := range h.subs {
		n += len(m)
	}
	return n
}

// Close disconnects every subscriber and refuses new ones.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, m := range h.subs {
		for s := range m {
			s.kick()
		}
	}
	h.subs = map[int64]map[*Sub]struct{}{}
}
