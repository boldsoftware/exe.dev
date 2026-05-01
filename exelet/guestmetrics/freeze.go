package guestmetrics

import "time"

// NoteActivity records a host-side activity observation for a VM.
// Cheap, lock-bounded, never blocks on I/O. When freeze is disabled
// this is a no-op.
func (p *Pool) NoteActivity(id string, w ActivityWitness) {
	if !p.cfg.Freeze.Enabled {
		return
	}
	p.mu.RLock()
	e, ok := p.entries[id]
	p.mu.RUnlock()
	if !ok {
		return
	}

	e.sched.Lock()
	e.lastCPUPct = w.CPUPercent

	var kick bool
	switch e.vmTier {
	case VMTierFrozen:
		if reason := p.shouldWake(e, w); reason != WakeNone {
			p.transitionToActiveLocked(e, w.Now, reason)
			kick = true
		}
	case VMTierActive:
		if p.shouldFreeze(e, w) {
			p.transitionToFrozenLocked(e, w.Now)
		}
	}
	e.sched.Unlock()

	if kick {
		select {
		case p.wakeCh <- struct{}{}:
		default:
		}
	}
}

// WakeForRPC forces a Frozen VM back to Active so the next dispatcher
// tick fires a scrape. Returns true if the VM was actually woken.
func (p *Pool) WakeForRPC(id string) bool {
	if !p.cfg.Freeze.Enabled {
		return false
	}
	p.mu.RLock()
	e, ok := p.entries[id]
	p.mu.RUnlock()
	if !ok {
		return false
	}

	e.sched.Lock()
	was := e.vmTier
	if was == VMTierFrozen {
		p.transitionToActiveLocked(e, time.Now(), WakeRPC)
	}
	e.sched.Unlock()

	if was == VMTierFrozen {
		select {
		case p.wakeCh <- struct{}{}:
		default:
		}
		return true
	}
	return false
}

// VMTier returns the current per-VM tier and whether the VM is known.
func (p *Pool) VMTier(id string) (VMTier, bool) {
	p.mu.RLock()
	e, ok := p.entries[id]
	p.mu.RUnlock()
	if !ok {
		return VMTierActive, false
	}
	e.sched.Lock()
	t := e.vmTier
	e.sched.Unlock()
	return t, true
}

// SetFreezeEnabled is a runtime kill switch. When disabled, all frozen
// VMs are forced back to Active.
func (p *Pool) SetFreezeEnabled(enabled bool) {
	p.cfg.Freeze.Enabled = enabled
	if enabled {
		return
	}
	// Walk entries and force all Frozen -> Active.
	p.mu.RLock()
	entries := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	p.mu.RUnlock()
	for _, e := range entries {
		e.sched.Lock()
		if e.vmTier == VMTierFrozen {
			p.transitionToActiveLocked(e, time.Now(), WakeAdmin)
		}
		e.sched.Unlock()
	}
}

// transitionToActiveLocked: caller holds e.sched.
func (p *Pool) transitionToActiveLocked(e *entry, now time.Time, reason WakeReason) {
	if e.vmTier != VMTierFrozen {
		return
	}
	e.vmTier = VMTierActive
	e.idleSince = time.Time{}
	e.lastWakeReason = reason
	e.next = now // dispatcher will fire on the next tick / kick
}

// transitionToFrozenLocked: caller holds e.sched.
func (p *Pool) transitionToFrozenLocked(e *entry, now time.Time) {
	e.vmTier = VMTierFrozen
	e.frozenSince = now
	e.next = now.Add(p.cfg.FrozenCadence)
}

// shouldFreeze decides whether an Active VM should enter Frozen.
// Caller holds e.sched.
func (p *Pool) shouldFreeze(e *entry, w ActivityWitness) bool {
	cfg := p.cfg.Freeze
	if w.HostTier == TierPressured {
		e.idleSince = time.Time{}
		return false
	}
	if w.VMUptime < cfg.MinUptime {
		return false
	}

	if w.CPUPercent >= cfg.CPUEnter {
		e.idleSince = time.Time{}
		return false
	}
	if e.idleSince.IsZero() {
		e.idleSince = w.Now
	}
	// Guard against clock jumps.
	if w.Now.Before(e.idleSince) {
		e.idleSince = w.Now
	}
	if w.Now.Sub(e.idleSince) < cfg.IdleWindow {
		return false
	}

	// Need a fresh, quiet guest sample. Don't freeze blind.
	last, ok := e.ring.Latest()
	if !ok || w.Now.Sub(last.FetchedAt) > p.cfg.StaleAfter {
		return false
	}
	if last.PSIFull.Avg60 >= cfg.RequireGuestPSIBelow {
		return false
	}
	if e.ring.RefaultRate(60*time.Second) > 0 {
		return false
	}
	return true
}

// shouldWake decides whether a Frozen VM should return to Active.
// Caller holds e.sched.
func (p *Pool) shouldWake(e *entry, w ActivityWitness) WakeReason {
	cfg := p.cfg.Freeze
	if w.CPUPercent >= cfg.CPUExit {
		return WakeCPU
	}
	if w.HostTier == TierPressured {
		return WakeHostPressure
	}
	return WakeNone
}
