package execore

import (
	"context"
	"log/slog"
	"time"

	"exe.dev/billing"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// SubscriptionPoller polls Stripe for subscription events and updates
// the database accordingly.
type SubscriptionPoller struct {
	billing *billing.Manager
	db      *sqlite.DB
	log     *slog.Logger

	ctx  context.Context
	stop func()
	done chan struct{}
}

// StartSubscriptionPoller creates a new subscription poller.
func StartSubscriptionPoller(billing *billing.Manager, db *sqlite.DB, logger *slog.Logger) *SubscriptionPoller {
	ctx, stop := context.WithCancel(context.Background())
	p := &SubscriptionPoller{
		billing: billing,
		db:      db,
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

	for e := range p.billing.SubscriptionEvents(p.ctx, since) {
		// Normalize the Stripe event timestamp for consistent storage
		eventAt := sqlite.NormalizeTime(e.EventAt)
		var inserted bool
		err := p.db.Tx(p.ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			q := exedb.New(tx.Conn())
			res, err := q.InsertBillingEvent(p.ctx, exedb.InsertBillingEventParams{
				AccountID: e.AccountID,
				EventType: e.EventType,
				EventAt:   eventAt,
			})
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			inserted = n > 0
			return nil
		})
		if err != nil {
			p.log.ErrorContext(p.ctx, "failed to record subscription event",
				"account_id", e.AccountID,
				"event_type", e.EventType,
				"event_at", e.EventAt,
				"error", err)
			continue
		}
		if inserted {
			p.log.InfoContext(p.ctx, "subscription event recorded",
				"account_id", e.AccountID,
				"event_type", e.EventType,
				"event_at", e.EventAt)
		}
	}
}

// Stop stops the poller. It blocks until the polling goroutine has exited.
func (p *SubscriptionPoller) Stop() {
	p.stop()
}
