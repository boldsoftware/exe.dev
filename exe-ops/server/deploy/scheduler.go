package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Scheduler orchestrates continuous deployment of exed on a fixed schedule.
// Deploys happen every 30 minutes during business hours (Mon-Fri 9am ET to 6pm PT),
// skipping US federal holidays. CD can be enabled/disabled via API; failures
// auto-disable CD and require manual re-enable.
type Scheduler struct {
	mu             sync.Mutex
	enabled        bool
	deploying      bool
	disabledReason string
	lastDeploy     *ScheduledDeploy
	nextAt         time.Time

	manager   *Manager
	gitSHA    GitSHAProvider
	notifier  CDNotifier
	inventory InventoryProvider
	log       *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
	wakeC  chan struct{} // signals the Run loop to re-evaluate immediately

	// nowFunc allows tests to override the current time.
	nowFunc func() time.Time
}

// ScheduledDeploy records the outcome of a CD-triggered deploy.
type ScheduledDeploy struct {
	SHA       string    `json:"sha"`
	DeployID  string    `json:"deploy_id"`
	StartedAt time.Time `json:"started_at"`
	State     string    `json:"state"` // success, failed
}

// CDStatus is a point-in-time snapshot of the scheduler state, safe for JSON.
type CDStatus struct {
	Enabled        bool             `json:"enabled"`
	Deploying      bool             `json:"deploying"`
	DisabledReason string           `json:"disabled_reason,omitempty"`
	NextDeployAt   time.Time        `json:"next_deploy_at,omitempty"`
	LastDeploy     *ScheduledDeploy `json:"last_deploy,omitempty"`
	WindowOpen     bool             `json:"window_open"`
}

// GitSHAProvider provides the HEAD SHA of the main branch.
type GitSHAProvider interface {
	HeadSHA() string
}

// CDNotifier handles CD-specific Slack notifications.
type CDNotifier interface {
	CDSetTopic(topic string)
	CDPostMessage(text string)
}

// InventoryProvider provides host information for exed.
type InventoryProvider interface {
	// ExedHost returns the deploy target info for exed (host, dnsName, stage, role).
	ExedHost() (host, dnsName, stage, role string, ok bool)
}

const (
	deployInterval = 30 * time.Minute

	// Time window: first deploy at 9:00 AM ET, last at 6:00 PM PT.
	// In UTC:
	//   9:00 AM ET = 14:00 UTC (standard) or 13:00 UTC (daylight)
	//   6:00 PM PT = 02:00 UTC next day (standard) or 01:00 UTC next day (daylight)
	// We'll use America/New_York and America/Los_Angeles to handle DST properly.
	windowStartHour   = 9 // 9 AM in America/New_York
	windowStartMinute = 0
	windowEndHour     = 18 // 6 PM in America/Los_Angeles
	windowEndMinute   = 0
)

// NewScheduler creates a new CD scheduler.
func NewScheduler(
	manager *Manager,
	gitSHA GitSHAProvider,
	notifier CDNotifier,
	inventory InventoryProvider,
	log *slog.Logger,
) *Scheduler {
	return &Scheduler{
		manager:   manager,
		gitSHA:    gitSHA,
		notifier:  notifier,
		inventory: inventory,
		log:       log,
		nowFunc:   time.Now,
		wakeC:     make(chan struct{}, 1),
	}
}

// Enable starts the CD scheduler. If already enabled, this is a no-op.
func (s *Scheduler) Enable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled {
		return
	}
	s.enabled = true
	s.disabledReason = ""
	s.log.Info("CD scheduler enabled")
	if s.notifier != nil {
		now := s.nowFunc()
		next := s.nextDeployTime(now)
		s.notifier.CDPostMessage(fmt.Sprintf("🟢 exed CD enabled — first deploy at %s", next.UTC().Format("15:04 UTC")))
	}
	// Wake the Run loop so it picks up the new state immediately.
	select {
	case s.wakeC <- struct{}{}:
	default:
	}
}

// Disable stops the CD scheduler. If already disabled, this is a no-op.
func (s *Scheduler) Disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	s.enabled = false
	s.disabledReason = "manually disabled"
	s.log.Info("CD scheduler disabled")
	if s.notifier != nil {
		s.notifier.CDPostMessage("🔴 exed CD disabled")
		s.notifier.CDSetTopic("exed: 🔴 CD disabled")
	}
	// Wake the Run loop.
	select {
	case s.wakeC <- struct{}{}:
	default:
	}
}

// Status returns the current CD scheduler state.
func (s *Scheduler) Status() CDStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return CDStatus{
		Enabled:        s.enabled,
		Deploying:      s.deploying,
		DisabledReason: s.disabledReason,
		NextDeployAt:   s.nextAt,
		LastDeploy:     s.lastDeploy,
		WindowOpen:     s.isWindowOpen(s.nowFunc()),
	}
}

// Run starts the scheduler loop. It blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	defer s.wg.Done()

	s.log.Info("CD scheduler loop starting")
	defer s.log.Info("CD scheduler loop stopped")

	for {
		// Compute next deploy time.
		s.mu.Lock()
		enabled := s.enabled
		s.mu.Unlock()

		if !enabled {
			s.mu.Lock()
			s.nextAt = time.Time{}
			s.mu.Unlock()
			// Wait until woken by Enable() or context cancelled.
			select {
			case <-s.wakeC:
				continue
			case <-ctx.Done():
				return
			}
		}

		now := s.nowFunc()
		next := s.nextDeployTime(now)

		s.mu.Lock()
		s.nextAt = next
		s.mu.Unlock()

		// Update topic with next deploy time.
		if s.notifier != nil {
			s.updateTopic()
		}

		// Sleep until next deploy time.
		waitDur := time.Until(next)
		if waitDur > 0 {
			s.log.Info("CD scheduler waiting", "next_deploy", next.Format(time.RFC3339), "wait", waitDur.Round(time.Second))
			timer := time.NewTimer(waitDur)
			select {
			case <-timer.C:
			case <-s.wakeC:
				timer.Stop()
				continue // re-evaluate state
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}

		// Check if still enabled (user may have disabled during sleep).
		s.mu.Lock()
		enabled = s.enabled
		s.mu.Unlock()
		if !enabled {
			continue
		}

		// Trigger a deploy.
		s.runDeploy(ctx)
	}
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// runDeploy executes a single CD deploy: gets HEAD SHA, starts deploy, waits
// for completion, and handles success/failure.
func (s *Scheduler) runDeploy(ctx context.Context) {
	s.mu.Lock()
	s.deploying = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.deploying = false
		s.mu.Unlock()
	}()

	// Get HEAD SHA.
	sha := s.gitSHA.HeadSHA()
	if sha == "" {
		s.log.Warn("CD deploy skipped: no HEAD SHA available")
		return
	}
	if len(sha) != 40 {
		s.log.Warn("CD deploy skipped: invalid SHA", "sha", sha)
		return
	}

	// Skip if the SHA hasn't changed since the last successful deploy.
	s.mu.Lock()
	if s.lastDeploy != nil && s.lastDeploy.State == "success" && s.lastDeploy.SHA == sha {
		s.mu.Unlock()
		s.log.Info("CD deploy skipped: SHA unchanged", "sha", sha[:12])
		return
	}
	s.mu.Unlock()

	// Get exed host info from inventory.
	host, dnsName, stage, role, ok := s.inventory.ExedHost()
	if !ok {
		s.log.Warn("CD deploy skipped: exed host not found in inventory")
		return
	}

	shaShort := sha[:12]
	s.log.Info("CD deploy starting", "sha", shaShort, "host", host)

	// Start the deploy.
	req := Request{
		Stage:       stage,
		Role:        role,
		Process:     "exed",
		Host:        host,
		DNSName:     dnsName,
		SHA:         sha,
		InitiatedBy: "CD scheduler",
	}

	status, err := s.manager.Start(req)
	if err != nil {
		s.log.Error("CD deploy failed to start", "error", err)
		s.disableOnFailure(fmt.Sprintf("failed to start: %v", err))
		return
	}

	deployID := status.ID
	s.log.Info("CD deploy started", "deploy_id", deployID, "sha", shaShort)

	// Poll until deploy reaches terminal state.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			st, ok := s.manager.Get(deployID)
			if !ok {
				s.log.Warn("CD deploy vanished", "deploy_id", deployID)
				s.disableOnFailure("deploy vanished from manager")
				return
			}
			switch st.State {
			case "done":
				s.log.Info("CD deploy succeeded", "deploy_id", deployID, "sha", shaShort)
				s.mu.Lock()
				s.lastDeploy = &ScheduledDeploy{
					SHA:       sha,
					DeployID:  deployID,
					StartedAt: st.StartedAt,
					State:     "success",
				}
				s.mu.Unlock()
				if s.notifier != nil {
					// Check if this was the last deploy of the day.
					now := s.nowFunc()
					next := s.nextDeployTime(now)
					if next.Sub(now) > 12*time.Hour {
						// Next deploy is tomorrow or later — this was the last one.
						s.notifier.CDPostMessage(fmt.Sprintf("🌙 Last exed deploy of the day (%s). Next: %s",
							shaShort, next.UTC().Format("Mon 15:04 UTC")))
					}
					s.updateTopic()
				}
				return
			case "failed":
				s.log.Warn("CD deploy failed", "deploy_id", deployID, "sha", shaShort, "error", st.Error)
				s.mu.Lock()
				s.lastDeploy = &ScheduledDeploy{
					SHA:       sha,
					DeployID:  deployID,
					StartedAt: st.StartedAt,
					State:     "failed",
				}
				s.mu.Unlock()
				s.disableOnFailure(fmt.Sprintf("deploy failed: %s", st.Error))
				return
			case "cancelled":
				s.log.Warn("CD deploy cancelled", "deploy_id", deployID, "sha", shaShort)
				s.disableOnFailure("deploy was cancelled")
				return
			}
		case <-ctx.Done():
			s.log.Info("CD deploy interrupted by shutdown", "deploy_id", deployID)
			return
		}
	}
}

// disableOnFailure disables CD and posts a failure message to Slack.
func (s *Scheduler) disableOnFailure(reason string) {
	s.mu.Lock()
	s.enabled = false
	s.disabledReason = reason
	s.mu.Unlock()

	s.log.Warn("CD scheduler auto-disabled", "reason", reason)

	if s.notifier != nil {
		s.notifier.CDPostMessage(fmt.Sprintf("🔴 exed CD disabled — %s", reason))
		s.notifier.CDSetTopic("exed: 🔴 CD disabled")
	}
}

// updateTopic sets the Slack channel topic based on current CD state.
func (s *Scheduler) updateTopic() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled {
		s.notifier.CDSetTopic("exed: 🔴 CD disabled")
		return
	}

	// Don't update topic while deploying - only after completion.
	if s.deploying {
		return
	}

	now := s.nowFunc()
	if !s.isWindowOpen(now) {
		// Outside window: show when next deploy will happen.
		next := s.nextDeployTime(now)
		nextStr := next.UTC().Format("Mon 15:04 UTC")
		if s.lastDeploy != nil {
			shaShort := s.lastDeploy.SHA
			if len(shaShort) > 12 {
				shaShort = shaShort[:12]
			}
			s.notifier.CDSetTopic(fmt.Sprintf("exed: 💤 CD idle | Last: %s | Next: %s", shaShort, nextStr))
		} else {
			s.notifier.CDSetTopic(fmt.Sprintf("exed: 💤 CD idle | Next: %s", nextStr))
		}
		return
	}

	// Within window, between deploys.
	if s.lastDeploy != nil {
		shaShort := s.lastDeploy.SHA
		if len(shaShort) > 12 {
			shaShort = shaShort[:12]
		}
		nextStr := s.nextAt.UTC().Format("15:04 UTC")
		s.notifier.CDSetTopic(fmt.Sprintf("exed: 🟢 %s | Next: %s", shaShort, nextStr))
	} else {
		nextStr := s.nextAt.UTC().Format("15:04 UTC")
		s.notifier.CDSetTopic(fmt.Sprintf("exed: 🟢 CD active | Next: %s", nextStr))
	}
}

// nextDeployTime computes the next time a deploy should run, given the
// current time. Returns the next :00 or :30 boundary within the deploy
// window on a business day (Mon-Fri, non-holiday).
func (s *Scheduler) nextDeployTime(now time.Time) time.Time {
	et, _ := time.LoadLocation("America/New_York")
	pt, _ := time.LoadLocation("America/Los_Angeles")

	// Start with the next 30-minute boundary from now.
	t := now.Truncate(30 * time.Minute).Add(30 * time.Minute)

	for {
		// Check if this time is within the deploy window.
		if s.isDeployableTime(t, et, pt) {
			return t
		}
		// Move to next 30-minute boundary.
		t = t.Add(30 * time.Minute)
	}
}

// isDeployableTime returns true if t is within the CD deploy window:
// Mon-Fri, not a holiday, between 9am ET and 6pm PT.
func (s *Scheduler) isDeployableTime(t time.Time, et, pt *time.Location) bool {
	// Check day of week: Mon-Fri only.
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}

	// Check if it's a US federal holiday.
	if IsUSFederalHoliday(t) {
		return false
	}

	// Check time window: >= 9:00 AM ET and <= 6:00 PM PT.
	// Convert to both timezones and check.
	etTime := t.In(et)
	ptTime := t.In(pt)

	// Must be >= 9:00 AM in ET.
	if etTime.Hour() < windowStartHour {
		return false
	}
	if etTime.Hour() == windowStartHour && etTime.Minute() < windowStartMinute {
		return false
	}

	// Must be <= 6:00 PM in PT (last deploy AT 6pm).
	if ptTime.Hour() > windowEndHour {
		return false
	}
	if ptTime.Hour() == windowEndHour && ptTime.Minute() > windowEndMinute {
		return false
	}

	return true
}

// isWindowOpen returns true if the given time is within the deploy window.
func (s *Scheduler) isWindowOpen(now time.Time) bool {
	et, _ := time.LoadLocation("America/New_York")
	pt, _ := time.LoadLocation("America/Los_Angeles")
	return s.isDeployableTime(now, et, pt)
}
