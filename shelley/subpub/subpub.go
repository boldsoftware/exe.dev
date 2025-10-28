package subpub

import (
	"context"
	"sync"
)

type SubPub[K any] struct {
	mu          sync.Mutex
	subscribers []*subscriber[K]
}

type subscriber[K any] struct {
	idx    int64
	ch     chan K
	ctx    context.Context
	cancel context.CancelFunc
}

func New[K any]() *SubPub[K] {
	return &SubPub[K]{
		subscribers: make([]*subscriber[K], 0),
	}
}

// Subscribe registers an interest in messages after the given index, subject to the
// expiration/cancellation of the provided context. The returned function blocks
// until a new message, and can return false as the second arguent if the subscription
// is done for.
func (sp *SubPub[K]) Subscribe(ctx context.Context, idx int64) func() (K, bool) {
	// Create a child context so we can cancel the subscription independently
	subCtx, cancel := context.WithCancel(ctx)

	// Buffered channel to avoid blocking publishers
	ch := make(chan K, 10)
	sub := &subscriber[K]{
		idx:    idx,
		ch:     ch,
		ctx:    subCtx,
		cancel: cancel,
	}

	sp.mu.Lock()
	sp.subscribers = append(sp.subscribers, sub)
	sp.mu.Unlock()

	// Return a function that blocks until the next message
	return func() (K, bool) {
		select {
		case msg, ok := <-ch:
			if !ok {
				var zero K
				return zero, false
			}
			return msg, true
		case <-subCtx.Done():
			// Context cancelled, but drain any buffered messages first
			select {
			case msg, ok := <-ch:
				if ok {
					return msg, true
				}
			default:
			}
			var zero K
			return zero, false
		}
	}
}

// Publish sends a message to all subscribers waiting for messages after the given index.
// Subscribers that are "behind" should get a disconnection message.
func (sp *SubPub[K]) Publish(idx int64, message K) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Notify subscribers and filter out disconnected ones
	remaining := sp.subscribers[:0]
	for _, sub := range sp.subscribers {
		// Check if context is still valid
		select {
		case <-sub.ctx.Done():
			// Context cancelled, close channel and don't keep subscriber
			close(sub.ch)
			continue
		default:
		}

		// Only send to subscribers waiting for messages after an index < idx
		if sub.idx < idx {
			// Try to send the message
			select {
			case sub.ch <- message:
				// Success, update subscriber's index and keep them
				sub.idx = idx
				remaining = append(remaining, sub)
			default:
				// Channel full, subscriber is behind - disconnect them
				close(sub.ch)
				sub.cancel()
			}
		} else {
			// This subscriber is not interested yet (already has this index or beyond)
			remaining = append(remaining, sub)
		}
	}
	sp.subscribers = remaining
}
