package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// runJudgeBacktest runs the judge over the N most-recently-active
// conversations and writes a TSV report (so we can eyeball whether the
// classifier is doing the right thing before we flip it on for real).
func runJudgeBacktest(ctx context.Context, dbPath string, args []string) error {
	fs := flag.NewFlagSet("judge-backtest", flag.ContinueOnError)
	n := fs.Int("n", 100, "number of most-recent conversations to judge")
	out := fs.String("out", "judge-backtest.tsv", "output TSV path ('-' for stdout)")
	parallel := fs.Int("parallel", 6, "concurrent judge calls")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(subject,''), COALESCE(last_activity_at,0)
FROM conversations ORDER BY last_activity_at DESC LIMIT ?`, *n)
	if err != nil {
		return err
	}
	type convRef struct {
		id           string
		subject      string
		lastActivity int64
	}
	var convs []convRef
	for rows.Next() {
		var c convRef
		if err := rows.Scan(&c.id, &c.subject, &c.lastActivity); err == nil {
			convs = append(convs, c)
		}
	}
	rows.Close()
	slog.InfoContext(ctx, "backtest", "conversations", len(convs))

	type resultRow struct {
		ID, Subject  string
		LastActivity int64
		Verdict      judgeVerdict
		Err          string
	}
	results := make([]resultRow, len(convs))

	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	start := time.Now()
	for i, c := range convs {
		i, c := i, c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			v, err := judgeConversation(ctx, db, c.id)
			rr := resultRow{ID: c.id, Subject: c.subject, LastActivity: c.lastActivity, Verdict: v}
			if err != nil {
				rr.Err = err.Error()
			}
			results[i] = rr
		}()
	}
	wg.Wait()
	slog.InfoContext(ctx, "backtest done", "elapsed", time.Since(start).Round(time.Millisecond))

	var w *csv.Writer
	var fh *os.File
	if *out == "-" {
		w = csv.NewWriter(os.Stdout)
	} else {
		fh, err = os.Create(*out)
		if err != nil {
			return err
		}
		defer fh.Close()
		w = csv.NewWriter(fh)
	}
	w.Comma = '\t'
	_ = w.Write([]string{"id", "last_activity", "category", "is_support", "prompt_injection", "confidence_pct", "cost_usd", "reason", "subject", "error"})
	var totalCost float64
	counts := map[string]int{}
	skipped := 0
	for _, r := range results {
		ts := time.Unix(r.LastActivity, 0).UTC().Format("2006-01-02T15:04:05Z")
		_ = w.Write([]string{
			r.ID, ts, r.Verdict.Category,
			fmt.Sprintf("%v", r.Verdict.IsSupport),
			fmt.Sprintf("%v", r.Verdict.PromptInjection),
			fmt.Sprintf("%d", r.Verdict.ConfidencePct),
			fmt.Sprintf("%.4f", r.Verdict.CostUSD),
			strings.ReplaceAll(r.Verdict.Reason, "\n", " "),
			strings.ReplaceAll(truncate(r.Subject, 120), "\n", " "),
			strings.ReplaceAll(truncate(r.Err, 200), "\n", " "),
		})
		totalCost += r.Verdict.CostUSD
		counts[r.Verdict.Category]++
		if r.Verdict.Skip() {
			skipped++
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "judged %d conversations, total cost $%.4f, would skip %d\n", len(results), totalCost, skipped)
	cb, _ := json.Marshal(counts)
	fmt.Fprintf(os.Stderr, "categories: %s\n", cb)
	if fh != nil {
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	}
	return nil
}
