package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"
)

// pollLoop runs the importer on a 15s cadence. On 429 it exponentially
// backs off up to 5 minutes. Any conversation that gained a new inbound
// message since the last scrape is enqueued for the auto-agent worker.
func pollLoop(ctx context.Context, db *sql.DB, queue chan<- string) {
	const (
		baseInterval = 15 * time.Second
		maxBackoff   = 5 * time.Minute
	)
	cfg, ok := resolveMissive()
	if !ok {
		slog.WarnContext(ctx, "poll loop disabled: no Missive config")
		return
	}
	slog.InfoContext(ctx, "poll loop: missive", "base", cfg.Base, "direct_token", cfg.Token != "")
	client := newMissiveClient(cfg)
	backoff := baseInterval

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		imp := &importer{db: db, client: client, incremental: true}
		runID, _ := recordScrapeStart(ctx, db)
		params := url.Values{}
		params.Set("all", "true")
		err := imp.scrapeMailbox(ctx, params, 20)
		recordScrapeEnd(ctx, db, runID, imp, err)

		if err != nil {
			if errors.Is(err, errRateLimited) {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				slog.WarnContext(ctx, "poll: rate limited", "sleep", backoff)
			} else if ctx.Err() == nil {
				// Non-429 errors: log and sleep a normal interval. Don't let
				// one bad API call escalate into a full backoff.
				slog.WarnContext(ctx, "poll: scrape error", "err", err)
				backoff = baseInterval
			}
		} else {
			backoff = baseInterval
		}

		// Enqueue new-inbound conversations for the agent worker.
		for _, cid := range imp.NewInboundConvs {
			select {
			case queue <- cid:
				slog.InfoContext(ctx, "poll: queued for agent", "conv", cid)
			default:
				slog.WarnContext(ctx, "poll: agent queue full, dropping", "conv", cid)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func recordScrapeStart(ctx context.Context, db *sql.DB) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO scrape_runs (started_at) VALUES (?)`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func recordScrapeEnd(ctx context.Context, db *sql.DB, runID int64, imp *importer, scrapeErr error) {
	errStr := ""
	if scrapeErr != nil {
		errStr = scrapeErr.Error()
	}
	_, _ = db.ExecContext(ctx, `UPDATE scrape_runs SET finished_at=?, convs_seen=?, msgs_seen=?, comments_seen=?, error=? WHERE id=?`,
		time.Now().Unix(), imp.convsSeen, imp.msgsSeen, imp.commentsSeen, errStr, runID)
}

// autoAgentWorker pulls conversation ids off the queue and runs the agent on
// each one, sequentially. De-dupes against the results table so a given
// last_activity_at is only ever processed once.
func autoAgentWorker(ctx context.Context, db *sql.DB, queue <-chan string) {
	// inflight tracks the last_activity_at we have already dispatched for a
	// given conv, so bursts of enqueues for the same conv only fire once.
	var mu sync.Mutex
	inflight := map[string]int64{}

	for {
		var convID string
		select {
		case <-ctx.Done():
			return
		case convID = <-queue:
		}
		var lastActivity int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(last_activity_at,0) FROM conversations WHERE id=?`, convID).Scan(&lastActivity); err != nil {
			slog.WarnContext(ctx, "auto-agent: lookup conv", "conv", convID, "err", err)
			continue
		}
		mu.Lock()
		if inflight[convID] >= lastActivity && lastActivity != 0 {
			mu.Unlock()
			continue
		}
		inflight[convID] = lastActivity
		mu.Unlock()

		// Skip if we already have a result newer than the last activity.
		var latestResult int64
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_at),0) FROM results WHERE conversation_id=?`, convID).Scan(&latestResult)
		if latestResult >= lastActivity && lastActivity != 0 {
			slog.InfoContext(ctx, "auto-agent: already have result", "conv", convID)
			continue
		}

		slog.InfoContext(ctx, "auto-agent: running", "conv", convID)
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		prompt := "New inbound support message. Triage this conversation and produce a short internal comment for the on-call engineer."
		res, err := runAgent(runCtx, db, convID, prompt, nil)
		cancel()
		if err != nil {
			slog.WarnContext(ctx, "auto-agent: run failed", "conv", convID, "err", err)
			continue
		}
		slog.InfoContext(ctx, "auto-agent: done", "conv", convID, "result", res.ResultID, "cost_usd", fmt.Sprintf("%.4f", res.CostUSD))
	}
}

// startAutoLoops wires the poll loop and agent worker together. Returns after
// both goroutines are launched.
func startAutoLoops(ctx context.Context, db *sql.DB) {
	if _, ok := resolveMissive(); !ok {
		slog.WarnContext(ctx, "auto loops disabled: no Missive config")
		return
	}
	queue := make(chan string, 128)
	go pollLoop(ctx, db, queue)
	go autoAgentWorker(ctx, db, queue)
}

// Ensure encoding/json keeps linking even though we may remove the direct
// reference later (defensive; also doubles as a compile-time sanity check
// that the package is imported).
var _ = json.Marshal
