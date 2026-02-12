package email

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mrz1836/postmark"
)

const (
	// bouncesPageSize is the maximum number of bounces to fetch per API call.
	// Postmark allows up to 500 per request.
	bouncesPageSize = 500

	// bouncesLookbackDefault is how far back to look when there's no last poll time.
	// We use 30 days since Postmark only stores bounce dumps for 30 days.
	bouncesLookbackDefault = 30 * 24 * time.Hour
)

// BounceRecord represents a bounced email that should be stored.
type BounceRecord struct {
	Email     string
	Reason    string
	BouncedAt time.Time
}

// BounceStore provides the interface for storing bounces and tracking poll state.
type BounceStore interface {
	// GetLastBouncesPollTime returns the last time bounces were polled.
	// Returns zero time if never polled.
	GetLastBouncesPollTime(ctx context.Context) (time.Time, error)
	// SetLastBouncesPollTime updates the last poll time.
	SetLastBouncesPollTime(ctx context.Context, t time.Time) error
	// StoreBounce stores a bounce record.
	StoreBounce(ctx context.Context, bounce BounceRecord) error
}

// PostmarkBouncePoller polls Postmark for bounces and stores them via the provided store.
type PostmarkBouncePoller struct {
	client   *postmark.Client
	store    BounceStore
	logger   *slog.Logger
	interval time.Duration
	stopOnce sync.Once
	stop     chan struct{}
}

// NewPostmarkBouncePoller creates a new Postmark bounce poller.
// pollInterval controls how often to poll (typically 10 minutes).
func NewPostmarkBouncePoller(apiKey string, store BounceStore, logger *slog.Logger, pollInterval time.Duration) *PostmarkBouncePoller {
	client := postmark.NewClient(apiKey, "")
	return &PostmarkBouncePoller{
		client:   client,
		store:    store,
		logger:   logger,
		interval: pollInterval,
		stop:     make(chan struct{}),
	}
}

// Start begins the polling loop.
func (p *PostmarkBouncePoller) Start() {
	// Poll immediately on start
	p.pollOnce()

	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.pollOnce()
			case <-p.stop:
				return
			}
		}
	}()
}

// Stop stops the poller.
func (p *PostmarkBouncePoller) Stop() {
	p.stopOnce.Do(func() {
		close(p.stop)
	})
}

// pollOnce performs a single poll of Postmark for bounces.
func (p *PostmarkBouncePoller) pollOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Get the last poll time
	fromDate, err := p.store.GetLastBouncesPollTime(ctx)
	if err != nil {
		p.logger.ErrorContext(ctx, "failed to get last bounces poll time", "error", err)
		return
	}
	if fromDate.IsZero() {
		// First poll ever - look back 30 days
		fromDate = time.Now().Add(-bouncesLookbackDefault)
	}

	// Fetch bounces from Postmark using the fromdate filter.
	// Postmark uses Eastern Time for date filtering.
	// We subtract 1 hour as a buffer for clock skew.
	fromDateWithBuffer := fromDate.Add(-1 * time.Hour)
	options := map[string]any{
		"fromdate": fromDateWithBuffer.Format("2006-01-02T15:04:05"),
	}

	var totalProcessed int

	// Paginate through all bounces
	offset := int64(0)
	for {
		bounces, totalCount, err := p.client.GetBounces(ctx, bouncesPageSize, offset, options)
		if err != nil {
			p.logger.ErrorContext(ctx, "failed to fetch bounces from Postmark", "error", err, "offset", offset)
			return
		}

		if len(bounces) == 0 {
			break
		}

		// Process each bounce
		for _, bounce := range bounces {
			// Only process bounces that Postmark has marked as inactive.
			// This means hard bounces and other permanent failures.
			if !bounce.Inactive {
				continue
			}

			// Skip if we've already processed this bounce (based on timestamp).
			// This handles the overlap from our 1-hour buffer.
			if !bounce.BouncedAt.After(fromDate) {
				continue
			}

			// Store the bounce
			record := BounceRecord{
				Email:     bounce.Email,
				Reason:    bounce.Type + ": " + bounce.Description,
				BouncedAt: bounce.BouncedAt,
			}
			if err := p.store.StoreBounce(ctx, record); err != nil {
				p.logger.ErrorContext(ctx, "failed to store email bounce",
					"email", bounce.Email, "error", err)
				continue
			}

			totalProcessed++
			p.logger.InfoContext(ctx, "recorded email bounce",
				"email", bounce.Email, "type", bounce.Type, "bounced_at", bounce.BouncedAt)
		}

		// Check if we've fetched all bounces
		offset += int64(len(bounces))
		if offset >= totalCount {
			break
		}
	}

	// Update the last poll time.
	// Use current time rather than newest bounce time to avoid re-polling
	// the same range if there were no new bounces.
	now := time.Now()
	if err := p.store.SetLastBouncesPollTime(ctx, now); err != nil {
		p.logger.ErrorContext(ctx, "failed to update last bounces poll time", "error", err)
		return
	}

	if totalProcessed > 0 {
		p.logger.InfoContext(ctx, "bounces poll complete", "processed", totalProcessed)
	}
}
