package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func runImport(ctx context.Context, dbPath string, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	maxPages := fs.Int("max-pages", 2000, "max pages of conversations to scrape (50/page)")
	incremental := fs.Bool("incremental", true, "stop when we hit conversations older than the newest already-imported")
	sharedLabel := fs.String("shared-label", "", "optional shared-label id to filter conversations (otherwise scrape 'all')")
	teamInbox := fs.String("team-inbox", "", "optional team id: fetch that team's inbox + closed mailboxes instead of 'all'")
	rescanComments := fs.Bool("rescan-comments", false, "re-fetch comments for every conversation (default: only for new or newly-updated)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, ok := resolveMissive()
	if !ok {
		return fmt.Errorf("Missive not configured: set EXE_MISSIVE_API_KEY, or attach the 'missive' exe.dev integration")
	}
	slog.InfoContext(ctx, "missive", "base", cfg.Base, "direct_token", cfg.Token != "")

	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	client := newMissiveClient(cfg)
	imp := &importer{db: db, client: client, rescanComments: *rescanComments, incremental: *incremental}

	// record a scrape_runs row
	res, err := db.ExecContext(ctx, `INSERT INTO scrape_runs (started_at) VALUES (?)`, time.Now().Unix())
	if err != nil {
		return err
	}
	runID, _ := res.LastInsertId()

	var scrapeErr error
	defer func() {
		errStr := ""
		if scrapeErr != nil {
			errStr = scrapeErr.Error()
		}
		_, _ = db.Exec(`UPDATE scrape_runs SET finished_at=?, convs_seen=?, msgs_seen=?, comments_seen=?, error=? WHERE id=?`,
			time.Now().Unix(), imp.convsSeen, imp.msgsSeen, imp.commentsSeen, errStr, runID)
	}()

	mailboxes := []url.Values{}
	switch {
	case *sharedLabel != "":
		v := url.Values{}
		v.Set("shared_label", *sharedLabel)
		mailboxes = append(mailboxes, v)
	case *teamInbox != "":
		v1 := url.Values{}
		v1.Set("team_inbox", *teamInbox)
		v2 := url.Values{}
		v2.Set("team_closed", *teamInbox)
		mailboxes = append(mailboxes, v1, v2)
	default:
		v := url.Values{}
		v.Set("all", "true")
		mailboxes = append(mailboxes, v)
	}

	for _, base := range mailboxes {
		if err := imp.scrapeMailbox(ctx, base, *maxPages); err != nil {
			scrapeErr = err
			return err
		}
	}
	slog.InfoContext(ctx, "import done", "conversations", imp.convsSeen, "messages", imp.msgsSeen, "comments", imp.commentsSeen)
	fmt.Printf("imported %d conversations, %d messages, %d comments\n", imp.convsSeen, imp.msgsSeen, imp.commentsSeen)
	return nil
}

type importer struct {
	db             *sql.DB
	client         *missiveClient
	rescanComments bool
	incremental    bool
	convsSeen      int
	msgsSeen       int
	commentsSeen   int
	// NewInboundConvs is filled with the ids of conversations that gained an
	// inbound (non-staff) message during this scrape. Used by the poll loop to
	// auto-run the agent.
	NewInboundConvs []string
}

// isStaffAddr returns true if a from-address looks like someone on the exe.dev
// team (so we shouldn't auto-run the agent on their outbound replies).
func isStaffAddr(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	for _, d := range []string{"@exe.xyz", "@exe.dev", "@sketch.dev"} {
		if strings.HasSuffix(addr, d) {
			return true
		}
	}
	// Known founder personal addresses (they show up as the From on some
	// support replies sent before proper aliasing).
	for _, a := range []string{"philip.zeyliger@gmail.com", "david.crawshaw@gmail.com"} {
		if addr == a {
			return true
		}
	}
	return false
}

func (imp *importer) scrapeMailbox(ctx context.Context, base url.Values, maxPages int) error {
	var until string
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		params := url.Values{}
		for k, vs := range base {
			for _, v := range vs {
				params.Add(k, v)
			}
		}
		params.Set("limit", "50")
		if until != "" {
			params.Set("until", until)
		}
		convs, raws, err := imp.client.listConversations(ctx, params)
		if err != nil {
			return fmt.Errorf("list conversations (page %d): %w", page, err)
		}
		if len(convs) == 0 {
			return nil
		}
		var minActivity float64
		anyChanged := false
		for i, conv := range convs {
			prevActivity, existed, err := imp.getKnownActivity(ctx, conv.ID)
			if err != nil {
				return err
			}
			changed := !existed || int64(conv.LastActivityAt) != prevActivity
			if _, err := imp.upsertConversation(ctx, conv, raws[i]); err != nil {
				return err
			}
			if changed || imp.rescanComments {
				anyChanged = true
				newInbound, err := imp.scrapeMessages(ctx, conv.ID)
				if err != nil {
					if errors.Is(err, errRateLimited) {
						return err
					}
					slog.WarnContext(ctx, "scrape messages", "conv", conv.ID, "error", err)
				}
				if newInbound {
					imp.NewInboundConvs = append(imp.NewInboundConvs, conv.ID)
				}
				if err := imp.scrapeComments(ctx, conv.ID); err != nil {
					if errors.Is(err, errRateLimited) {
						return err
					}
					slog.WarnContext(ctx, "scrape comments", "conv", conv.ID, "error", err)
				}
			}
			if conv.LastActivityAt > 0 && (minActivity == 0 || conv.LastActivityAt < minActivity) {
				minActivity = conv.LastActivityAt
			}
			imp.convsSeen++
		}
		slog.InfoContext(ctx, "page", "page", page, "count", len(convs), "any_changed", anyChanged, "until", until)
		if imp.incremental && !anyChanged {
			// Nothing on this page changed since our last scan, so nothing
			// earlier in the list could have changed either. Stop.
			return nil
		}
		if minActivity == 0 {
			return nil
		}
		nextUntil := strconv.FormatFloat(minActivity, 'f', -1, 64)
		if nextUntil == until {
			return nil
		}
		until = nextUntil
		if len(convs) < 50 {
			return nil
		}
	}
	return nil
}

// getKnownActivity returns the last_activity_at we already have stored for
// convID, and whether we have a row for it at all.
func (imp *importer) getKnownActivity(ctx context.Context, convID string) (int64, bool, error) {
	var ts int64
	err := imp.db.QueryRowContext(ctx, `SELECT COALESCE(last_activity_at,0) FROM conversations WHERE id=?`, convID).Scan(&ts)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return ts, true, nil
}

func (imp *importer) upsertConversation(ctx context.Context, conv missiveConversation, raw json.RawMessage) (bool, error) {
	now := time.Now().Unix()
	subject := conv.Subject
	if subject == "" {
		subject = conv.LatestSubject
	}
	teamName := ""
	if conv.Team != nil {
		teamName = conv.Team.Name
	}
	assignees, _ := json.Marshal(conv.Assignees)
	labels, _ := json.Marshal(conv.SharedLabels)

	// Detect if new: check if a row exists.
	var existing string
	err := imp.db.QueryRowContext(ctx, `SELECT id FROM conversations WHERE id=?`, conv.ID).Scan(&existing)
	isNew := err == sql.ErrNoRows
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	_, err = imp.db.ExecContext(ctx, `
INSERT INTO conversations
  (id, subject, created_at, last_activity_at, team_name, assignees_json, labels_json, raw_json, first_seen, last_seen)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  subject=excluded.subject,
  last_activity_at=excluded.last_activity_at,
  team_name=excluded.team_name,
  assignees_json=excluded.assignees_json,
  labels_json=excluded.labels_json,
  raw_json=excluded.raw_json,
  last_seen=excluded.last_seen
`, conv.ID, subject, int64(conv.CreatedAt), int64(conv.LastActivityAt), teamName,
		string(assignees), string(labels), string(raw), now, now)
	if err != nil {
		return false, err
	}
	return isNew, nil
}

// scrapeMessages pulls messages for a conversation and returns true if any
// NEW inbound (non-staff) message was observed.
func (imp *importer) scrapeMessages(ctx context.Context, convID string) (bool, error) {
	var until string
	newInbound := false
	for page := 0; page < 100; page++ {
		metas, raws, err := imp.client.listMessages(ctx, convID, until)
		if err != nil {
			return newInbound, err
		}
		if len(metas) == 0 {
			return newInbound, nil
		}
		var minDelivered float64
		seenAny := false
		for i, m := range metas {
			wasNew, err := imp.upsertMessage(ctx, convID, m, raws[i])
			if err != nil {
				return newInbound, err
			}
			imp.msgsSeen++
			if wasNew && m.FromField != nil && !isStaffAddr(m.FromField.Address) {
				newInbound = true
			}
			if m.DeliveredAt > 0 && (minDelivered == 0 || m.DeliveredAt < minDelivered) {
				minDelivered = m.DeliveredAt
			}
			seenAny = true
		}
		if !seenAny || len(metas) < 10 {
			return newInbound, nil
		}
		if minDelivered == 0 {
			return newInbound, nil
		}
		nextUntil := strconv.FormatFloat(minDelivered, 'f', -1, 64)
		if nextUntil == until {
			return newInbound, nil
		}
		until = nextUntil
	}
	return newInbound, nil
}

// upsertMessage returns (wasNew, err). wasNew==true when this message id was
// not previously in the DB.
func (imp *importer) upsertMessage(ctx context.Context, convID string, m missiveMessageMeta, raw json.RawMessage) (bool, error) {
	var existing string
	err := imp.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE id=?`, m.ID).Scan(&existing)
	wasNew := err == sql.ErrNoRows
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	fromAddr, fromName := "", ""
	if m.FromField != nil {
		fromAddr = m.FromField.Address
		fromName = m.FromField.Name
	}
	toJSON, _ := json.Marshal(m.ToFields)
	bodyHTML := m.Body
	if bodyHTML == "" {
		bodyHTML = m.Preview
	}
	bodyText := htmlToText(bodyHTML)
	now := time.Now().Unix()
	_, err = imp.db.ExecContext(ctx, `
INSERT INTO messages (id, conversation_id, subject, delivered_at, from_address, from_name, to_json, body_text, body_html, raw_json, first_seen)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  subject=excluded.subject,
  delivered_at=excluded.delivered_at,
  from_address=excluded.from_address,
  from_name=excluded.from_name,
  to_json=excluded.to_json,
  body_text=excluded.body_text,
  body_html=excluded.body_html,
  raw_json=excluded.raw_json
`, m.ID, convID, m.Subject, int64(m.DeliveredAt), fromAddr, fromName, string(toJSON),
		bodyText, bodyHTML, string(raw), now)
	return wasNew, err
}

func (imp *importer) scrapeComments(ctx context.Context, convID string) error {
	comments, raws, err := imp.client.listComments(ctx, convID)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for i, c := range comments {
		authorName, authorEmail := "", ""
		if c.Author != nil {
			authorName = c.Author.Name
			authorEmail = c.Author.Email
		}
		_, err := imp.db.ExecContext(ctx, `
INSERT INTO comments (id, conversation_id, author_name, author_email, body, created_at, raw_json, first_seen)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  author_name=excluded.author_name,
  author_email=excluded.author_email,
  body=excluded.body,
  created_at=excluded.created_at,
  raw_json=excluded.raw_json
`, c.ID, convID, authorName, authorEmail, c.Body, int64(c.CreatedAt), string(raws[i]), now)
		if err != nil {
			return err
		}
		imp.commentsSeen++
	}
	return nil
}
