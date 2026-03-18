package server

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// Event is a server-sent event pushed to dashboard subscribers.
type Event struct {
	Type string          `json:"type"` // "status" or "report"
	Data json.RawMessage `json:"data"`
}

// StatusData is the payload for status events.
type StatusData struct {
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// subscriber is a dashboard SSE listener.
type subscriber struct {
	ch   chan Event
	done chan struct{}
}

// Hub tracks agent presence and fans out events to dashboard subscribers.
type Hub struct {
	mu          sync.Mutex
	agents      map[string]struct{}
	subscribers map[*subscriber]struct{}
	log         *slog.Logger
}

// NewHub creates a new Hub.
func NewHub(log *slog.Logger) *Hub {
	return &Hub{
		agents:      make(map[string]struct{}),
		subscribers: make(map[*subscriber]struct{}),
		log:         log,
	}
}

// AgentConnected marks an agent as connected and broadcasts a status event.
func (h *Hub) AgentConnected(name string) {
	h.mu.Lock()
	h.agents[name] = struct{}{}
	h.mu.Unlock()

	h.log.Info("agent connected via stream", "name", name)
	h.broadcastStatus(name, true)
}

// AgentDisconnected marks an agent as disconnected and broadcasts a status event.
func (h *Hub) AgentDisconnected(name string) {
	h.mu.Lock()
	delete(h.agents, name)
	h.mu.Unlock()

	h.log.Info("agent disconnected from stream", "name", name)
	h.broadcastStatus(name, false)
}

// IsAgentConnected returns whether an agent has an active stream connection.
func (h *Hub) IsAgentConnected(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[name]
	return ok
}

// ConnectedAgents returns a snapshot of all connected agent names.
func (h *Hub) ConnectedAgents() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make(map[string]bool, len(h.agents))
	for name := range h.agents {
		result[name] = true
	}
	return result
}

// Subscribe creates a new subscriber for dashboard SSE events.
func (h *Hub) Subscribe() *subscriber {
	s := &subscriber{
		ch:   make(chan Event, 64),
		done: make(chan struct{}),
	}
	h.mu.Lock()
	h.subscribers[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber.
func (h *Hub) Unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subscribers, s)
	h.mu.Unlock()
	close(s.done)
}

// Broadcast sends an event to all subscribers. Non-blocking: drops events
// for slow subscribers rather than blocking the hub.
func (h *Hub) Broadcast(event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subscribers {
		select {
		case s.ch <- event:
		default:
			// Subscriber channel full, drop event.
		}
	}
}

func (h *Hub) broadcastStatus(name string, online bool) {
	data, err := json.Marshal(StatusData{Name: name, Online: online})
	if err != nil {
		h.log.Error("marshal status event", "error", err)
		return
	}
	h.Broadcast(Event{Type: "status", Data: data})
}
