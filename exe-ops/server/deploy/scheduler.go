package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Scheduler orchestrates continuous deployment of exed on a fixed schedule.
// Deploys happen every 30 minutes during business hours (Mon-Fri 9am ET to 5pm PT),
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
	channel   string   // slack channel ("ship" for prod, "boat" for staging)
	services  []string // processes managed by CD (e.g. ["exed"])

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wakeC     chan struct{} // signals the Run loop to re-evaluate immediately
	stateFile string        // path to persist enabled/disabled state

	// announcedDate tracks which business day we've already sent
	// the first/last deploy announcements for, to avoid duplicates.
	announcedFirstDate string // "2006-01-02"
	announcedLastDate  string // "2006-01-02"

	lastTopic string // last topic string sent to Slack, to avoid redundant updates

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

// GitSHAProvider provides the HEAD SHA of the main branch and commit
// range counting.
type GitSHAProvider interface {
	HeadSHA() string
	// CommitCount returns the number of commits in (fromSHA, toSHA].
	// Returns -1 on error.
	CommitCount(fromSHA, toSHA string) int
}

// CDNotifier handles CD-specific Slack notifications.
type CDNotifier interface {
	CDSetTopic(channel, topic string)
	CDPostMessage(channel, text string)
}

// InventoryProvider provides host information for exed.
type InventoryProvider interface {
	// ExedHost returns the deploy target info for exed (host, dnsName, stage, role, deployedSHA).
	ExedHost() (host, dnsName, stage, role, deployedSHA string, ok bool)
}

const (
	deployInterval = 30 * time.Minute

	// Time window: first deploy at 9:00 AM ET, last at 5:00 PM PT.
	// We use America/New_York and America/Los_Angeles to handle DST properly.
	windowStartHour   = 9 // 9 AM in America/New_York
	windowStartMinute = 0
	windowEndHour     = 17 // 5 PM in America/Los_Angeles
	windowEndMinute   = 0

	// maxCommitsPerDeploy is the threshold above which CD auto-disables.
	// A deploy with more than this many commits is too risky for automated
	// rollout and requires human review.
	maxCommitsPerDeploy = 20
)

// cdState is the JSON structure persisted to disk.
type cdState struct {
	Enabled            bool   `json:"enabled"`
	DisabledReason     string `json:"disabled_reason,omitempty"`
	AnnouncedFirstDate string `json:"announced_first_date,omitempty"`
	AnnouncedLastDate  string `json:"announced_last_date,omitempty"`
	LastTopic          string `json:"last_topic,omitempty"`
}

// NewScheduler creates a new CD scheduler. stateFile is the path to
// persist enabled/disabled state across restarts. If empty, state is
// not persisted (resets to disabled on restart).
func NewScheduler(
	manager *Manager,
	gitSHA GitSHAProvider,
	notifier CDNotifier,
	inventory InventoryProvider,
	log *slog.Logger,
	environment string,
	stateFile string,
) *Scheduler {
	channel := "ship"
	if environment == "staging" {
		channel = "boat"
	}
	s := &Scheduler{
		manager:   manager,
		gitSHA:    gitSHA,
		notifier:  notifier,
		inventory: inventory,
		log:       log,
		channel:   channel,
		services:  []string{"exed"},
		nowFunc:   time.Now,
		wakeC:     make(chan struct{}, 1),
		stateFile: stateFile,
	}
	s.loadState()
	return s
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
	s.announcedFirstDate = ""
	s.announcedLastDate = ""
	s.lastTopic = ""
	s.saveStateLocked()
	s.log.Info("CD scheduler enabled")
	if s.notifier != nil {
		now := s.nowFunc()
		next := s.nextDeployTime(now)
		s.notifier.CDPostMessage(s.channel, fmt.Sprintf("🟢 exed CD enabled — first deploy at %s", formatTime(next)))
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
	s.saveStateLocked()
	s.log.Info("CD scheduler disabled")
	if s.notifier != nil {
		s.notifier.CDPostMessage(s.channel, "🔴 exed CD disabled")
		s.setTopicLocked("exed: 🔴 CD disabled")
	}
	// Wake the Run loop.
	select {
	case s.wakeC <- struct{}{}:
	default:
	}
}

// NotifyDeploy is called when any deploy finishes. If it's a successful
// exed deploy not initiated by the CD scheduler itself, we update the
// topic to reflect the newly deployed SHA.
func (s *Scheduler) NotifyDeploy(st Status) {
	// Only care about successful exed deploys from humans (out-of-band).
	if st.Process != "exed" || st.State != "done" || st.InitiatedBy == "exe-ops" {
		return
	}

	s.mu.Lock()
	s.lastDeploy = &ScheduledDeploy{
		SHA:       st.SHA,
		DeployID:  st.ID,
		StartedAt: st.StartedAt,
		State:     "success",
	}
	enabled := s.enabled
	s.mu.Unlock()

	s.log.Info("CD scheduler: recorded out-of-band exed deploy", "sha", st.SHA[:12], "by", st.InitiatedBy)

	if enabled && s.notifier != nil {
		s.updateTopic()
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

	// Seed lastDeploy from inventory so the topic shows the current SHA
	// even after a restart (when in-memory state is lost).
	s.seedLastDeploy()

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
		prevNext := s.nextAt
		s.nextAt = next
		s.mu.Unlock()

		// If the next deploy is on a different day and we haven't already
		// announced it, explain why (holiday, weekend, end of day).
		if s.notifier != nil && !next.IsZero() && (prevNext.IsZero() || prevNext.YearDay() != next.YearDay() || prevNext.Year() != next.Year()) {
			if reason := s.skipReason(now); reason != "" {
				s.notifier.CDPostMessage(s.channel, fmt.Sprintf("💤 exed CD paused — %s. Resumes %s", reason, formatTimeWithDay(next)))
			}
		}

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

		// Announce first/last deploy of the day before running.
		if s.notifier != nil {
			s.announceFirstLast()
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

	// Get exed host info from inventory.
	host, dnsName, stage, role, deployedSHA, ok := s.inventory.ExedHost()
	if !ok {
		s.log.Warn("CD deploy skipped: exed host not found in inventory")
		return
	}

	// Skip if the host is already running this SHA.
	if deployedSHA == sha {
		s.log.Info("CD deploy skipped: host already running this SHA", "sha", sha[:12])
		return
	}

	// Safety valve: if the deploy would ship too many commits, disable CD
	// and alert the team. Large changesets are risky for automated rollout.
	if deployedSHA != "" && len(deployedSHA) == 40 {
		count := s.gitSHA.CommitCount(deployedSHA, sha)
		if count > maxCommitsPerDeploy {
			s.log.Warn("CD disabled: too many commits", "count", count, "from", deployedSHA[:12], "to", sha[:12])
			s.mu.Lock()
			s.enabled = false
			s.disabledReason = fmt.Sprintf("%d commits in next release (max %d)", count, maxCommitsPerDeploy)
			s.saveStateLocked()
			s.mu.Unlock()
			if s.notifier != nil {
				s.notifier.CDPostMessage(s.channel, fmt.Sprintf(
					"<!here> 🔴 exed CD disabled — %d commits in the next release (%s → %s). Manual deploy required.",
					count, shaLink(deployedSHA), shaLink(sha)))
				s.mu.Lock()
				s.setTopicLocked("exed: 🔴 CD disabled")
				s.mu.Unlock()
			}
			return
		}
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
		InitiatedBy: "exe-ops",
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

// seedLastDeploy populates lastDeploy from inventory if it's nil.
// This ensures the topic shows the current deployed SHA after a restart.
func (s *Scheduler) seedLastDeploy() {
	s.mu.Lock()
	if s.lastDeploy != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	_, _, _, _, deployedSHA, ok := s.inventory.ExedHost()
	if !ok || deployedSHA == "" || len(deployedSHA) != 40 {
		return
	}

	s.mu.Lock()
	s.lastDeploy = &ScheduledDeploy{
		SHA:   deployedSHA,
		State: "success",
	}
	s.mu.Unlock()
	s.log.Info("CD scheduler seeded lastDeploy from inventory", "sha", deployedSHA[:12])
}

// announceFirstLast posts a message when the first or last deploy of
// the business day is about to run. Each announcement fires at most once
// per calendar day.
func (s *Scheduler) announceFirstLast() {
	now := s.nowFunc()
	et, _ := time.LoadLocation("America/New_York")
	today := now.In(et).Format("2006-01-02")

	// First deploy of the day.
	s.mu.Lock()
	alreadyFirst := s.announcedFirstDate == today
	s.mu.Unlock()
	if !alreadyFirst {
		s.mu.Lock()
		s.announcedFirstDate = today
		s.saveStateLocked()
		s.mu.Unlock()
		s.notifier.CDPostMessage(s.channel, "☀️ First CD deploy of the day coming up. Active services:"+s.serviceList())
	}

	// Last deploy of the day: next deploy after this one is on a different day.
	nextAfter := s.nextDeployTime(now)
	nextAfterDate := nextAfter.In(et).Format("2006-01-02")

	s.mu.Lock()
	alreadyLast := s.announcedLastDate == today
	s.mu.Unlock()
	if !alreadyLast && nextAfterDate != today {
		s.mu.Lock()
		s.announcedLastDate = today
		s.saveStateLocked()
		s.mu.Unlock()
		s.notifier.CDPostMessage(s.channel, fmt.Sprintf(
			"🌙 Last CD deploy of the day — back at it %s. Active services:", formatTimeWithDay(nextAfter))+s.serviceList())
	}
}

// disableOnFailure disables CD and posts a failure message to Slack.
func (s *Scheduler) disableOnFailure(reason string) {
	s.mu.Lock()
	s.enabled = false
	s.disabledReason = reason
	s.saveStateLocked()
	s.mu.Unlock()

	s.log.Warn("CD scheduler auto-disabled", "reason", reason)

	if s.notifier != nil {
		s.notifier.CDPostMessage(s.channel, fmt.Sprintf("🔴 exed CD disabled — %s", reason))
		s.mu.Lock()
		s.setTopicLocked("exed: 🔴 CD disabled")
		s.mu.Unlock()
	}
}

// updateTopic sets the Slack channel topic based on current CD state.
func (s *Scheduler) updateTopic() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled {
		s.setTopicLocked("exed: 🔴 CD disabled")
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
		nextStr := formatTimeWithDay(next)
		if s.lastDeploy != nil {
			s.setTopicLocked(fmt.Sprintf("exed: 💤 CD idle | Last: %s | Next: %s", shaLink(s.lastDeploy.SHA), nextStr))
		} else {
			s.setTopicLocked(fmt.Sprintf("exed: 💤 CD idle | Next: %s", nextStr))
		}
		return
	}

	// Within window, between deploys.
	nextStr := formatTime(s.nextAt)
	if s.lastDeploy != nil {
		s.setTopicLocked(fmt.Sprintf("exed: 🟢 %s | Next: %s", shaLink(s.lastDeploy.SHA), nextStr))
	} else {
		s.setTopicLocked(fmt.Sprintf("exed: 🟢 CD active | Next: %s", nextStr))
	}
}

// setTopicLocked sends the topic to Slack only if it differs from the last
// one we set. Caller must hold s.mu.
func (s *Scheduler) setTopicLocked(topic string) {
	if topic == s.lastTopic {
		return
	}
	s.lastTopic = topic
	s.saveStateLocked()
	s.notifier.CDSetTopic(s.channel, topic)
}

// loadState reads the persisted CD state from disk.
func (s *Scheduler) loadState() {
	if s.stateFile == "" {
		return
	}
	data, err := os.ReadFile(s.stateFile)
	if err != nil {
		return // file doesn't exist yet, start disabled
	}
	var st cdState
	if err := json.Unmarshal(data, &st); err != nil {
		s.log.Warn("CD state file corrupt, starting disabled", "error", err)
		return
	}
	s.enabled = st.Enabled
	s.disabledReason = st.DisabledReason
	s.announcedFirstDate = st.AnnouncedFirstDate
	s.announcedLastDate = st.AnnouncedLastDate
	s.lastTopic = st.LastTopic
	if s.enabled {
		s.log.Info("CD scheduler restored to enabled from state file")
	} else if st.DisabledReason != "" {
		s.log.Info("CD scheduler restored to disabled", "reason", st.DisabledReason)
	}
}

// saveStateLocked writes the current CD state to disk. Caller must hold mu.
func (s *Scheduler) saveStateLocked() {
	if s.stateFile == "" {
		return
	}
	st := cdState{
		Enabled:            s.enabled,
		DisabledReason:     s.disabledReason,
		AnnouncedFirstDate: s.announcedFirstDate,
		AnnouncedLastDate:  s.announcedLastDate,
		LastTopic:          s.lastTopic,
	}
	data, err := json.Marshal(st)
	if err != nil {
		s.log.Warn("CD state marshal failed", "error", err)
		return
	}
	if err := os.WriteFile(s.stateFile, data, 0o644); err != nil {
		s.log.Warn("CD state write failed", "error", err)
	}
}

// skipReason returns a human-readable explanation for why CD is not deploying
// right now (e.g. holiday, weekend, outside hours). Returns "" if now is
// within the deploy window.
func (s *Scheduler) skipReason(now time.Time) string {
	if s.isWindowOpen(now) {
		return ""
	}

	et, _ := time.LoadLocation("America/New_York")
	nowET := now.In(et)

	if name := USFederalHolidayName(now); name != "" {
		return name
	}
	switch nowET.Weekday() {
	case time.Saturday, time.Sunday:
		return "weekend"
	}
	return "outside deploy window"
}

// formatTime renders t as "15:04 UTC | 11:04 ET | 08:04 PT" for Slack.
func formatTime(t time.Time) string {
	et, _ := time.LoadLocation("America/New_York")
	pt, _ := time.LoadLocation("America/Los_Angeles")
	return fmt.Sprintf("%s | %s ET | %s PT",
		t.UTC().Format("15:04 UTC"),
		t.In(et).Format("15:04"),
		t.In(pt).Format("15:04"))
}

// formatTimeWithDay is like formatTime but includes the weekday.
func formatTimeWithDay(t time.Time) string {
	et, _ := time.LoadLocation("America/New_York")
	pt, _ := time.LoadLocation("America/Los_Angeles")
	return fmt.Sprintf("%s | %s ET | %s PT",
		t.UTC().Format("Mon 15:04 UTC"),
		t.In(et).Format("Mon 15:04"),
		t.In(pt).Format("Mon 15:04"))
}

// serviceList formats the scheduler's services as a Slack bullet list.
func (s *Scheduler) serviceList() string {
	var b strings.Builder
	for _, svc := range s.services {
		b.WriteString("\n• ")
		b.WriteString(svc)
	}
	return b.String()
}

// shaLink returns a Slack mrkdwn link to the GitHub commit.
func shaLink(sha string) string {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("<%s%s|%s>", githubCommitURL, sha, short)
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
// Mon-Fri, not a holiday, between 9am ET and 5pm PT.
func (s *Scheduler) isDeployableTime(t time.Time, et, pt *time.Location) bool {
	// Convert to both timezones first — all checks use local wall-clock
	// values so the server's own timezone doesn't matter.
	etTime := t.In(et)
	ptTime := t.In(pt)

	// Check day of week in ET: Mon-Fri only.
	// Using ET (not UTC) prevents Sunday evening from being treated as
	// Monday when the server runs in UTC.
	if etTime.Weekday() == time.Saturday || etTime.Weekday() == time.Sunday {
		return false
	}

	// Check if it's a US federal holiday.
	if IsUSFederalHoliday(t) {
		return false
	}

	// Must be >= 9:00 AM in ET.
	if etTime.Hour() < windowStartHour {
		return false
	}
	if etTime.Hour() == windowStartHour && etTime.Minute() < windowStartMinute {
		return false
	}

	// Must be <= 5:00 PM in PT (last deploy AT 5pm).
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
