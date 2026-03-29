package server

import (
	"sync"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/subpub"
)

// streamFlusher batches LLM stream deltas and flushes them periodically.
// Anthropic's SSE stream emits hundreds of tiny text_delta events per second.
// Broadcasting each one individually overwhelms the subpub channel (buffer=10),
// causing subscriber disconnections. Instead, we accumulate deltas and flush
// the combined text every interval (e.g., 50ms), yielding ~20 updates/second.
type streamFlusher struct {
	sp       *subpub.SubPub[StreamResponse]
	interval time.Duration

	mu      sync.Mutex
	buf     string // accumulated text since last flush
	index   int    // content block index of accumulated text
	timer   *time.Timer
	running bool
}

func newStreamFlusher(sp *subpub.SubPub[StreamResponse], interval time.Duration) *streamFlusher {
	return &streamFlusher{
		sp:       sp,
		interval: interval,
	}
}

// Push adds a stream delta to the buffer and schedules a flush.
func (sf *streamFlusher) Push(delta llm.StreamDelta) {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	if delta.Type == "text" {
		sf.buf += delta.Text
		sf.index = delta.Index
	} else {
		// For non-text deltas (thinking, etc.), broadcast immediately
		sf.sp.Broadcast(StreamResponse{
			StreamDelta: &delta,
		})
		return
	}

	if !sf.running {
		sf.running = true
		sf.timer = time.AfterFunc(sf.interval, sf.flush)
	}
}

func (sf *streamFlusher) flush() {
	sf.mu.Lock()
	text := sf.buf
	idx := sf.index
	sf.buf = ""
	sf.running = false
	if sf.timer != nil {
		sf.timer.Stop()
		sf.timer = nil
	}
	sf.mu.Unlock()

	if text != "" {
		sf.sp.Broadcast(StreamResponse{
			StreamDelta: &llm.StreamDelta{
				Type:  "text",
				Text:  text,
				Index: idx,
			},
		})
	}
}

// Flush forces any buffered text to be broadcast immediately.
// Call this before recording the final assistant message to ensure
// deltas reach the UI before the full message replaces them.
func (sf *streamFlusher) Flush() {
	sf.flush()
}
