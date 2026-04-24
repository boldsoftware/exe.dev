package execore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"golang.org/x/time/rate"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/tracing"
)

const (
	defaultTrialExpiryRateLimit   = 10 * time.Minute
	trialExpiryIdlePollInterval   = 6 * time.Hour
	trialExpiryDeferredRetryDelay = 1 * time.Hour
	trialExpiryQueryErrorRetry    = 5 * time.Minute
)

type queue[T any] []T

func (q queue[T]) empty() bool {
	return len(q) == 0
}

func (q queue[T]) peak() T {
	return q[0]
}

func (q *queue[T]) reset(items []T) {
	*q = append((*q)[:0], items...)
}

func (q *queue[T]) clear() {
	*q = (*q)[:0]
}

func (q *queue[T]) pop() {
	if len(*q) == 0 {
		return
	}
	var zero T
	(*q)[0] = zero
	*q = (*q)[1:]
}

// startTrialExpiryEnforcer runs a background loop that stops VMs belonging to
// users whose trial plans have expired. It queries the next trial expiry time
// and sleeps until then, falling back to polling every 6 hours when there are
// no active trials. The debug page can wake it immediately.
//
// A rate limiter controls how frequently users are enforced. The outer loop
// keeps an in-memory queue of expired candidates so each pass can continue from
// where the previous one left off, instead of re-fetching the whole backlog.
func (s *Server) startTrialExpiryEnforcer(ctx context.Context) {
	timer := time.NewTimer(defaultTrialExpiryRateLimit)
	defer timer.Stop()
	var candidateQueue queue[exedb.ExpiredTrialCandidatesRow]

	for {
		nextRun := s.runTrialExpiryPass(ctx, &candidateQueue)
		delay := nextRun

		at := time.Now().Add(delay)
		s.trialExpiryNextWake.Store(&at)
		timer.Reset(delay)

		select {
		case <-ctx.Done():
			s.trialExpiryNextWake.Store(nil)
			return
		case <-timer.C:
		case <-s.trialExpiryWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		s.trialExpiryNextWake.Store(nil)
	}
}

// runTrialExpiryPass scans expired stripeless-trial candidates in expiry order
// and enforces at most one user. The passed queue is owned by the caller, so
// subsequent passes can continue from the remaining candidates without
// fetching them again. If enforcement is disabled, rate limited, or every
// expired candidate fails/skips, it returns how long to wait before checking
// again.
func (s *Server) runTrialExpiryPass(ctx context.Context, candidateQueue *queue[exedb.ExpiredTrialCandidatesRow]) time.Duration {
	enabled, err := s.isTrialExpiryEnforcerEnabled(ctx)
	if err != nil {
		s.slog().ErrorContext(ctx, "trial expiry: failed to check enabled state", "error", err)
		return trialExpiryIdlePollInterval
	}
	if !enabled {
		candidateQueue.clear()
		s.trialExpiryRefreshRequested.Store(false)
		return trialExpiryIdlePollInterval
	}
	if s.trialExpiryRefreshRequested.Swap(false) {
		candidateQueue.clear()
	}
	// Reset the rate limit in case it changed.
	s.trialExpiryLimiter.SetLimit(s.trialExpiryRate(ctx))

	if candidateQueue.empty() {
		candidates, err := withRxRes0(s, ctx, (*exedb.Queries).ExpiredTrialCandidates)
		if err != nil {
			s.slog().ErrorContext(ctx, "trial expiry: failed to list candidates", "error", err)
			return trialExpiryQueryErrorRetry
		}
		candidateQueue.reset(candidates)
		if candidateQueue.empty() {
			return s.nextTrialExpiryPassDelay(ctx)
		}
	}

	for !candidateQueue.empty() {
		candidate := candidateQueue.peak()
		if s.trialExpiryShouldSkipCandidate(candidate.AccountID) {
			candidateQueue.pop()
			continue
		}
		enforced, delay := s.enforceExpiredTrialForCandidate(ctx, candidate)
		if delay > 0 {
			return delay
		}
		candidateQueue.pop()
		if enforced {
			return 0
		}
	}

	return s.nextTrialExpiryPassDelay(ctx)
}

// nextTrialExpiryPassDelay returns how long the enabled enforcer should sleep
// before the next pass. Uses the earliest upcoming trial expiry, falling back
// to the idle poll interval.
func (s *Server) nextTrialExpiryPassDelay(ctx context.Context) time.Duration {
	var (
		delay time.Duration
		found bool
	)

	if nextExpiry := s.nextTrialExpiry(ctx); nextExpiry != nil {
		if d := time.Until(*nextExpiry); d > 0 {
			delay = d
			found = true
		} else {
			return 0
		}
	}
	if nextRetry := s.nextTrialExpirySkipRetry(); nextRetry != nil {
		if d := time.Until(*nextRetry); d > 0 {
			if !found || d < delay {
				delay = d
				found = true
			}
		} else {
			return 0
		}
	}

	if found {
		return delay
	}
	return trialExpiryIdlePollInterval
}

// wakeTrialExpiryEnforcer pokes the enforcer to re-check immediately.
// Non-blocking; only called from the debug page.
func (s *Server) wakeTrialExpiryEnforcer() {
	s.trialExpiryRefreshRequested.Store(true)
	select {
	case s.trialExpiryWake <- struct{}{}:
	default:
	}
}

// isTrialExpiryEnforcerEnabled reports whether the enforcer is enabled in
// server_meta. Defaults to false (disabled) when the key is not set.
// Returns an error only on DB failures so the caller can distinguish
// "disabled by config" from "couldn't read config".
func (s *Server) isTrialExpiryEnforcerEnabled(ctx context.Context) (bool, error) {
	val, err := withRxRes0(s, ctx, (*exedb.Queries).GetTrialExpiryEnforcerEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // not set → disabled
	}
	if err != nil {
		return false, err
	}
	return val == "true", nil
}

// trialExpiryRate returns the rate.Limit for the enforcer from server_meta.
func (s *Server) trialExpiryRate(ctx context.Context) rate.Limit {
	return rate.Limit(1.0 / s.trialExpiryRateLimit(ctx).Seconds())
}

// trialExpiryRateLimit returns the configured minimum interval between
// successive user enforcements. Defaults to defaultTrialExpiryRateLimit.
func (s *Server) trialExpiryRateLimit(ctx context.Context) time.Duration {
	val, err := withRxRes0(s, ctx, (*exedb.Queries).GetTrialExpiryRateLimit)
	if err != nil {
		return defaultTrialExpiryRateLimit
	}
	d, err := time.ParseDuration(val)
	if err != nil || d <= 0 {
		return defaultTrialExpiryRateLimit
	}
	return d
}

func (s *Server) nextTrialExpiry(ctx context.Context) *time.Time {
	t, err := withRxRes0(s, ctx, (*exedb.Queries).NextTrialExpiry)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.slog().ErrorContext(ctx, "trial expiry: failed to query next expiry", "error", err)
		}
		return nil
	}
	return t
}

// errSkipUser is returned inside a transaction to indicate the user should be
// skipped (e.g. they acquired entitlements between the candidate query and the
// enforcement transaction). It is not a real error — callers unwrap it.
var errSkipUser = errors.New("skip user")

type trialExpirySkipAccount struct {
	AccountID     string
	UserID        string
	Kind          string
	Detail        string
	AttemptCount  int
	LastAttemptAt time.Time
	RetryAt       time.Time
}

type errTrialExpiryRateLimited struct {
	delay time.Duration
}

func (e errTrialExpiryRateLimited) Error() string {
	return "trial expiry rate limited"
}

func trialExpiryCandidateStillEligible(activePlan exedb.AccountPlan, now time.Time) bool {
	if plan.Base(activePlan.PlanID) != plan.CategoryTrial {
		return false
	}
	if activePlan.ChangedBy == nil || *activePlan.ChangedBy != "system:stripeless_trial" {
		return false
	}
	if activePlan.TrialExpiresAt == nil {
		return false
	}
	return !activePlan.TrialExpiresAt.After(now)
}

// enforceExpiredTrialForCandidate checks one expired-trial candidate and
// transitions their plan to basic if their trial has truly expired. Returns
// whether a user was enforced and, when rate limited, how long the caller
// should wait before retrying.
func (s *Server) enforceExpiredTrialForCandidate(ctx context.Context, candidate exedb.ExpiredTrialCandidatesRow) (bool, time.Duration) {
	ctx = tracing.ContextWithTraceID(ctx, tracing.GenerateTraceID())
	// Resolve plan and transition it in one write tx so a concurrent Stripe
	// webhook can't sneak in between the check and the plan replacement.
	var category plan.Category
	now := time.Now()
	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		var err error
		category, err = plan.ForUser(ctx, q, candidate.UserID)
		if err != nil {
			return fmt.Errorf("resolving plan: %w", err)
		}
		p, ok := plan.Get(category)
		if !ok || p.Entitlements[plan.VMRun] {
			return errSkipUser
		}

		activePlan, err := q.GetActiveAccountPlan(ctx, candidate.AccountID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return errSkipUser
		case err != nil:
			return fmt.Errorf("get active account plan: %w", err)
		case !trialExpiryCandidateStillEligible(activePlan, now):
			return errSkipUser
		}

		r := s.trialExpiryLimiter.Reserve()
		if d := r.Delay(); d > 0 {
			r.Cancel()
			return errTrialExpiryRateLimited{delay: d}
		}

		if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID: candidate.AccountID,
			PlanID:    plan.ID(plan.CategoryBasic),
			At:        now,
			ChangedBy: "system:trial_expired",
		}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		var rateLimited errTrialExpiryRateLimited
		switch {
		case errors.As(err, &rateLimited):
			return false, rateLimited.delay
		case errors.Is(err, errSkipUser):
			s.deferTrialExpiryCandidate(candidate, "skip", trialExpirySkipDetail(category))
		case !errors.Is(err, errSkipUser):
			s.slog().ErrorContext(ctx, "trial expiry: failed to enforce",
				"user_id", candidate.UserID,
				"account_id", candidate.AccountID,
				"error", err,
			)
			s.deferTrialExpiryCandidate(candidate, "error", err.Error())
		}
		return false, 0
	}
	s.clearTrialExpirySkipAccount(candidate.AccountID)
	s.slog().InfoContext(ctx, "trial expiry: transitioned plan to basic",
		"user_id", candidate.UserID,
		"account_id", candidate.AccountID,
		"previous_category", category,
	)

	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, candidate.UserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "trial expiry: failed to list running boxes",
			"user_id", candidate.UserID,
			"account_id", candidate.AccountID,
			"error", err,
		)
		// Plan was already transitioned, so this user won't be returned as an
		// expired-trial candidate again. The error log is sufficient for
		// operator follow-up.
		return true, 0
	}

	for _, box := range boxes {
		s.slog().InfoContext(ctx, "trial expiry: stopping VM",
			"user_id", candidate.UserID,
			"box_name", box.Name,
			"box_id", box.ID,
			"plan_category", plan.CategoryBasic,
		)
		if err := s.stopBox(ctx, box); err != nil {
			s.slog().ErrorContext(ctx, "trial expiry: failed to stop VM",
				"user_id", candidate.UserID,
				"box_name", box.Name,
				"error", err,
			)
		}
	}
	return true, 0
}

func (s *Server) deferTrialExpiryCandidate(candidate exedb.ExpiredTrialCandidatesRow, kind, detail string) {
	now := time.Now()
	s.trialExpirySkipMu.Lock()
	defer s.trialExpirySkipMu.Unlock()

	s.pruneTrialExpirySkipAccountsLocked(now)
	entry := s.trialExpirySkipAccounts[candidate.AccountID]
	entry.AccountID = candidate.AccountID
	entry.UserID = candidate.UserID
	entry.Kind = kind
	entry.Detail = detail
	entry.AttemptCount++
	entry.LastAttemptAt = now
	entry.RetryAt = now.Add(trialExpiryDeferredRetryDelay)
	s.trialExpirySkipAccounts[candidate.AccountID] = entry
}

func (s *Server) clearTrialExpirySkipAccount(accountID string) {
	s.trialExpirySkipMu.Lock()
	defer s.trialExpirySkipMu.Unlock()
	delete(s.trialExpirySkipAccounts, accountID)
}

func (s *Server) trialExpiryShouldSkipCandidate(accountID string) bool {
	now := time.Now()
	s.trialExpirySkipMu.Lock()
	defer s.trialExpirySkipMu.Unlock()

	entry, ok := s.trialExpirySkipAccounts[accountID]
	if !ok {
		return false
	}
	if now.After(entry.RetryAt) || now.Equal(entry.RetryAt) {
		delete(s.trialExpirySkipAccounts, accountID)
		return false
	}
	return true
}

func (s *Server) nextTrialExpirySkipRetry() *time.Time {
	now := time.Now()
	s.trialExpirySkipMu.Lock()
	defer s.trialExpirySkipMu.Unlock()

	s.pruneTrialExpirySkipAccountsLocked(now)

	var (
		next  time.Time
		found bool
	)
	for _, entry := range s.trialExpirySkipAccounts {
		if !found || entry.RetryAt.Before(next) {
			next = entry.RetryAt
			found = true
		}
	}
	if !found {
		return nil
	}
	return &next
}

func (s *Server) trialExpirySkipAccountsSnapshot() []trialExpirySkipAccount {
	now := time.Now()
	s.trialExpirySkipMu.Lock()
	defer s.trialExpirySkipMu.Unlock()

	s.pruneTrialExpirySkipAccountsLocked(now)

	rows := make([]trialExpirySkipAccount, 0, len(s.trialExpirySkipAccounts))
	for _, entry := range s.trialExpirySkipAccounts {
		rows = append(rows, entry)
	}
	slices.SortFunc(rows, func(a, b trialExpirySkipAccount) int {
		if c := a.RetryAt.Compare(b.RetryAt); c != 0 {
			return c
		}
		return cmp.Compare(a.AccountID, b.AccountID)
	})
	return rows
}

func (s *Server) pruneTrialExpirySkipAccountsLocked(now time.Time) {
	for accountID, entry := range s.trialExpirySkipAccounts {
		if now.After(entry.RetryAt) || now.Equal(entry.RetryAt) {
			delete(s.trialExpirySkipAccounts, accountID)
		}
	}
}

func trialExpirySkipDetail(category plan.Category) string {
	if p, ok := plan.Get(category); ok && p.Entitlements[plan.VMRun] {
		return fmt.Sprintf("effective plan %q still grants VMRun", category)
	}
	return fmt.Sprintf("effective plan %q is not enforceable", category)
}
