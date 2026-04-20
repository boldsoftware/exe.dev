package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/tracing"
)

const (
	defaultTrialExpiryRateLimit = 10 * time.Minute
	trialExpiryIdlePollInterval = 6 * time.Hour
)

// startTrialExpiryEnforcer runs a background loop that stops VMs belonging to
// users whose trial plans have expired. It queries the next trial expiry time
// and sleeps until then, falling back to polling every 6 hours when there are
// no active trials. The debug page can wake it immediately.
//
// A rate limiter controls how frequently users are enforced: each pass either
// enforces one expired user immediately or sleeps until the next allowed time.
func (s *Server) startTrialExpiryEnforcer(ctx context.Context) {
	timer := time.NewTimer(defaultTrialExpiryRateLimit)
	defer timer.Stop()

	for {
		nextRun := s.runTrialExpiryPass(ctx)
		delay := min(nextRun, trialExpiryIdlePollInterval)

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

// runTrialExpiryPass checks if there is any user with an expired trial
// and expires the trial for them. It does exactly one user at a time,
// if the enforcement is disabled or rate limited it returns the time
// to wait before checking again.
func (s *Server) runTrialExpiryPass(ctx context.Context) time.Duration {
	enabled, err := s.isTrialExpiryEnforcerEnabled(ctx)
	if err != nil {
		s.slog().ErrorContext(ctx, "trial expiry: failed to check enabled state", "error", err)
		return trialExpiryIdlePollInterval
	}
	if !enabled {
		return trialExpiryIdlePollInterval
	}
	// Reset the rate limit in case it changed.
	s.trialExpiryLimiter.SetLimit(s.trialExpiryRate(ctx))

	userID, ok := s.nextExpiredTrialUser(ctx)
	if !ok {
		return s.nextTrialExpiryPassDelay(ctx)
	}

	r := s.trialExpiryLimiter.Reserve()
	if d := r.Delay(); d > 0 {
		r.Cancel() // Can't do anything, so just return it.
		return d
	}

	s.enforceExpiredTrialForUser(ctx, userID)
	return 0
}

// nextTrialExpiryPassDelay returns how long the enabled enforcer should sleep
// before the next pass. Uses the earliest upcoming trial expiry, falling back
// to the idle poll interval.
func (s *Server) nextTrialExpiryPassDelay(ctx context.Context) time.Duration {
	if nextExpiry := s.nextTrialExpiry(ctx); nextExpiry != nil {
		if d := time.Until(*nextExpiry); d > 0 {
			return d
		}
		return 0
	}
	return trialExpiryIdlePollInterval
}

// wakeTrialExpiryEnforcer pokes the enforcer to re-check immediately.
// Non-blocking; only called from the debug page.
func (s *Server) wakeTrialExpiryEnforcer() {
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

func (s *Server) nextExpiredTrialUser(ctx context.Context) (string, bool) {
	userID, err := withRxRes0(s, ctx, (*exedb.Queries).NextExpiredTrialUser)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "trial expiry: failed to find candidate", "error", err)
		return "", false
	}
	return userID, true
}

// errSkipUser is returned inside a transaction to indicate the user should be
// skipped (e.g. they acquired entitlements between the candidate query and the
// enforcement transaction). It is not a real error — callers unwrap it.
var errSkipUser = errors.New("skip user")

// enforceExpiredTrialForUser checks a single user and transitions their plan
// to basic if their trial has truly expired. Stops any running VMs.
func (s *Server) enforceExpiredTrialForUser(ctx context.Context, userID string) {
	ctx = tracing.ContextWithTraceID(ctx, tracing.GenerateTraceID())
	// Resolve plan, fetch account, and transition plan all in one write tx
	// so that a concurrent Stripe webhook can't sneak in between the check
	// and the plan replacement.
	var (
		account  exedb.Account
		category plan.Category
	)
	now := time.Now()
	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		var err error
		category, err = plan.ForUser(ctx, q, userID)
		if err != nil {
			return fmt.Errorf("resolving plan: %w", err)
		}
		p, ok := plan.Get(category)
		if !ok || p.Entitlements[plan.VMRun] {
			return errSkipUser
		}
		account, err = q.GetAccountByUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("getting account: %w", err)
		}
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID: account.ID,
			PlanID:    plan.ID(plan.CategoryBasic),
			At:        now,
			ChangedBy: "system:trial_expired",
		})
	}); err != nil {
		s.slog().ErrorContext(ctx, "trial expiry: failed to enforce",
			"user_id", userID,
			"error", err,
		)
		return
	}
	s.slog().InfoContext(ctx, "trial expiry: transitioned plan to basic",
		"user_id", userID,
		"account_id", account.ID,
		"previous_category", category,
	)

	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "trial expiry: failed to list running boxes",
			"user_id", userID,
			"error", err,
		)
		// Plan was already transitioned; user won't appear in
		// NextExpiredTrialUser again so these VMs won't be retried
		// automatically. The error log is sufficient for operator follow-up.
		return
	}

	for _, box := range boxes {
		s.slog().InfoContext(ctx, "trial expiry: stopping VM",
			"user_id", userID,
			"box_name", box.Name,
			"box_id", box.ID,
			"plan_category", plan.CategoryBasic,
		)
		if err := s.stopBox(ctx, box); err != nil {
			s.slog().ErrorContext(ctx, "trial expiry: failed to stop VM",
				"user_id", userID,
				"box_name", box.Name,
				"error", err,
			)
		}
	}
}
