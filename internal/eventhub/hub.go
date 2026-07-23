package eventhub

import (
	"sync"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

type Hub struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan platform.Event]struct{}
}

func New() *Hub {
	return &Hub{subscribers: make(map[string]map[chan platform.Event]struct{})}
}

func (h *Hub) Subscribe(taskID string) (<-chan platform.Event, func()) {
	channel := make(chan platform.Event, 128)
	h.mu.Lock()
	if h.subscribers[taskID] == nil {
		h.subscribers[taskID] = make(map[chan platform.Event]struct{})
	}
	h.subscribers[taskID][channel] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers[taskID], channel)
			if len(h.subscribers[taskID]) == 0 {
				delete(h.subscribers, taskID)
			}
			close(channel)
			h.mu.Unlock()
		})
	}
	return channel, cancel
}

func (h *Hub) Publish(event platform.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for subscriber := range h.subscribers[event.TaskID] {
		select {
		case subscriber <- event:
		default:
			// History is durable in SQLite. A slow client reconnects from its last sequence.
		}
	}
}
