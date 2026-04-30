package guestmetrics

import (
	"sync"
	"testing"
	"time"
)

// TestClassifierConcurrentUpdateAndTier exercises the data-race fix:
// before Classifier.current was an atomic.Int32, concurrent calls to
// Update() (from the dispatcher) and Tier() (from the HTTP debug
// handler / RM poll) raced. Run under `go test -race`.
func TestClassifierConcurrentUpdateAndTier(t *testing.T) {
	c := NewClassifier(DefaultTierThresholds)

	// Two host samples that flip the classifier between calm and
	// pressured each Update() call.
	calm := HostSample{MemTotalBytes: 100, MemAvailableBytes: 99}
	pressured := HostSample{MemTotalBytes: 100, MemAvailableBytes: 1}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		toggle := false
		for {
			select {
			case <-done:
				return
			default:
			}
			if toggle {
				c.Update(calm)
			} else {
				c.Update(pressured)
			}
			toggle = !toggle
		}
	}()
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				_ = c.Tier()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(done)
	wg.Wait()
}
