package server

import (
	"sync"
	"time"
)

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// LogBuffer is a thread-safe ring buffer for log entries
type LogBuffer struct {
	entries     []LogEntry
	size        int
	head        int
	tail        int
	count       int
	mu          sync.RWMutex
	subscribers map[string]chan LogEntry
}

// NewLogBuffer creates a new log buffer with the given size
func NewLogBuffer(size int) *LogBuffer {
	return &LogBuffer{
		entries:     make([]LogEntry, size),
		size:        size,
		subscribers: make(map[string]chan LogEntry),
	}
}

// Add adds a new log entry to the buffer
func (lb *LogBuffer) Add(entry LogEntry) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	lb.entries[lb.tail] = entry
	lb.tail = (lb.tail + 1) % lb.size

	if lb.count < lb.size {
		lb.count++
	} else {
		// Buffer is full, move head
		lb.head = (lb.head + 1) % lb.size
	}

	// Notify subscribers
	for _, ch := range lb.subscribers {
		select {
		case ch <- entry:
		default:
			// Channel is full, skip
		}
	}
}

// GetAll returns all current log entries in chronological order
func (lb *LogBuffer) GetAll() []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	if lb.count == 0 {
		return nil
	}

	result := make([]LogEntry, lb.count)
	for i := 0; i < lb.count; i++ {
		result[i] = lb.entries[(lb.head+i)%lb.size]
	}
	return result
}

// Subscribe adds a subscriber that will receive new log entries
func (lb *LogBuffer) Subscribe(id string, ch chan LogEntry) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.subscribers[id] = ch
}

// Unsubscribe removes a subscriber
func (lb *LogBuffer) Unsubscribe(id string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if ch, exists := lb.subscribers[id]; exists {
		close(ch)
		delete(lb.subscribers, id)
	}
}
