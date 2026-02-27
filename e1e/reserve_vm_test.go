//reservevms:ok — tests pool infrastructure, does not create VMs.
package e1e

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestVMPool_Reserve(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newVMPool(5)
		p.reserve(t, 3)

		if !p.sem.TryAcquire(2) {
			t.Fatal("expected 2 slots still available")
		}
		p.sem.Release(2)

		if p.sem.TryAcquire(3) {
			t.Fatal("should not be able to acquire 3 more slots")
		}
	})
}

func TestVMPool_Cleanup(t *testing.T) {
	p := newVMPool(4)
	t.Run("hold", func(t *testing.T) {
		p.reserve(t, 3)
		if p.sem.TryAcquire(2) {
			t.Fatal("should not be able to acquire 2 more")
		}
	})
	// Subtest finished, cleanup released 3 slots. All 4 should be available.
	if !p.sem.TryAcquire(4) {
		t.Fatal("expected all 4 slots available after cleanup")
	}
	p.sem.Release(4)
}

func TestVMPool_NestedCleanup(t *testing.T) {
	p := newVMPool(4)
	t.Run("outer", func(t *testing.T) {
		p.reserve(t, 2)
		t.Run("inner", func(t *testing.T) {
			p.reserve(t, 2)
			// All 4 consumed.
			if p.sem.TryAcquire(1) {
				t.Fatal("expected 0 slots available")
			}
		})
		// Inner cleanup freed 2.
		if !p.sem.TryAcquire(2) {
			t.Fatal("expected 2 slots after inner cleanup")
		}
		p.sem.Release(2)
	})
	// Outer cleanup freed 2 more. All 4 available.
	if !p.sem.TryAcquire(4) {
		t.Fatal("expected all 4 slots after outer cleanup")
	}
	p.sem.Release(4)
}

func TestVMPool_ZeroIsNoop(t *testing.T) {
	p := newVMPool(3)
	p.reserve(t, 0) // should not consume any slots
	if !p.sem.TryAcquire(3) {
		t.Fatal("expected all 3 slots still available")
	}
	p.sem.Release(3)
}

func TestVMPool_OverCapacityPanics(t *testing.T) {
	p := newVMPool(2)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for n > capacity")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "wants 5 VMs but capacity=2") {
			t.Errorf("unexpected panic message: %s", msg)
		}
	}()
	p.reserve(t, 5)
}

func TestVMPool_BlocksUntilRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newVMPool(2)
		// Consume all slots.
		p.sem.Acquire(t.Context(), 2)

		reserved := make(chan struct{})
		go func() {
			p.reserve(t, 2)
			close(reserved)
		}()

		// Goroutine should be blocked waiting for slots.
		synctest.Wait()
		select {
		case <-reserved:
			t.Fatal("reserve should block when no slots available")
		default:
		}

		// Free the slots.
		p.sem.Release(2)

		// Should unblock.
		<-reserved
	})
}

func TestVMPool_BlocksUntilPartialRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newVMPool(3)
		// Consume all 3 slots.
		p.sem.Acquire(t.Context(), 3)

		reserved := make(chan struct{})
		go func() {
			p.reserve(t, 2)
			close(reserved)
		}()

		// Still blocked: only 1 slot freed, need 2.
		p.sem.Release(1)
		synctest.Wait()
		select {
		case <-reserved:
			t.Fatal("should still block with only 1 of 2 needed slots free")
		default:
		}

		// Free one more — now 2 available.
		p.sem.Release(1)
		<-reserved
	})
}

func TestVMPool_Serializes(t *testing.T) {
	// Two goroutines each want the full capacity.
	// They must take turns; neither should deadlock.
	synctest.Test(t, func(t *testing.T) {
		p := newVMPool(2)

		var mu sync.Mutex
		var maxHeld int
		var held int

		done := make(chan struct{}, 2)

		for range 2 {
			go func() {
				defer func() { done <- struct{}{} }()
				p.sem.Acquire(t.Context(), 2)

				mu.Lock()
				held++
				if held > maxHeld {
					maxHeld = held
				}
				mu.Unlock()

				time.Sleep(time.Second)

				mu.Lock()
				held--
				mu.Unlock()
				p.sem.Release(2)
			}()
		}

		<-done
		<-done

		mu.Lock()
		if maxHeld > 1 {
			t.Errorf("expected at most 1 concurrent holder, got %d", maxHeld)
		}
		mu.Unlock()
	})
}
