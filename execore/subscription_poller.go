package execore

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"exe.dev/billing"
)

// SubscriptionPoller polls Stripe for subscription events and updates
// the database accordingly.
type SubscriptionPoller struct {
	billing *billing.Manager
	log     *slog.Logger

	ctx  context.Context
	stop func()
	done chan struct{}
}

// StartSubscriptionPoller creates a new subscription poller.
func StartSubscriptionPoller(billing *billing.Manager, logger *slog.Logger) *SubscriptionPoller {
	ctx, stop := context.WithCancel(context.Background())
	p := &SubscriptionPoller{
		billing: billing,
		log:     logger,
		ctx:     ctx,
		stop:    stop,
		done:    make(chan struct{}),
	}
	go p.poll()
	return p
}

func (p *SubscriptionPoller) poll() {
	defer close(p.done)

	// Look back an arbitrary 60 days to catch any missed events.
	// This covers us in the unlikely event that the poller is down
	// for a period of up to 24 hours.
	since := time.Now().Add(-60 * 24 * time.Hour)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Trial expiry runs less frequently — every 30 seconds is plenty.
	trialTicker := time.NewTicker(30 * time.Second)
	defer trialTicker.Stop()

	for {
		nextSince, err := p.billing.SyncSubscriptions(p.ctx, since)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			p.log.ErrorContext(p.ctx, "failed to sync subscription events",
				"since", since,
				"error", err)
		} else {
			since = nextSince
		}

		// Check for expired trial plans on the trial ticker.
		select {
		case <-trialTicker.C:
			if n, err := p.billing.ExpireTrialPlans(p.ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					p.log.ErrorContext(p.ctx, "failed to expire trial plans", "error", err)
				}
			} else if n > 0 {
				p.log.InfoContext(p.ctx, "expired trial plans", "count", n)
			}
		default:
		}

		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Stop stops the poller. It blocks until the polling goroutine has exited.
func (p *SubscriptionPoller) Stop() {
	p.stop()
	<-p.done
}
