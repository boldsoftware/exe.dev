package execore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"exe.dev/exedb"
)

func insertSignupIPCheckForTest(t *testing.T, s *Server, email, payload string) int64 {
	t.Helper()
	var id int64
	err := s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertSignupIPCheck(ctx, exedb.InsertSignupIPCheckParams{
			Email:            email,
			Ip:               "203.0.113.9",
			Source:           "test",
			IpqsResponseJson: &payload,
		}); err != nil {
			return err
		}
		row, err := q.GetLatestSignupIPCheckByEmail(ctx, email)
		if err != nil {
			return err
		}
		id = row.ID
		return nil
	})
	if err != nil {
		t.Fatalf("insert signup ip check: %v", err)
	}
	return id
}

func TestBuildStalePDXDecisions(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()

	userWithSignal := createTestUser(t, s, "stale-pdx-signal@example.com")
	userFallback := createTestUser(t, s, "stale-pdx-fallback@example.com")
	for _, userID := range []string{userWithSignal, userFallback} {
		if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "pdx"}); err != nil {
			t.Fatalf("set pdx region: %v", err)
		}
	}
	insertSignupIPCheckForTest(t, s, "stale-pdx-signal@example.com", `{"country_code":"GB","latitude":51.5,"longitude":-0.12}`)

	decisions, err := s.buildStalePDXDecisions(ctx)
	if err != nil {
		t.Fatalf("build decisions: %v", err)
	}

	got := map[string]stalePDXDecision{}
	for _, d := range decisions {
		got[d.User.UserID] = d
	}
	if got[userWithSignal].TargetRegion != "lon" {
		t.Fatalf("signal user target = %q, want lon", got[userWithSignal].TargetRegion)
	}
	if got[userWithSignal].DecisionSource != "signup_ip_check" {
		t.Fatalf("signal user source = %q, want signup_ip_check", got[userWithSignal].DecisionSource)
	}
	if got[userFallback].TargetRegion != "lax" {
		t.Fatalf("fallback user target = %q, want lax", got[userFallback].TargetRegion)
	}
	if got[userFallback].DecisionSource != "fallback_lax" {
		t.Fatalf("fallback user source = %q, want fallback_lax", got[userFallback].DecisionSource)
	}
}

func TestRunStalePDXBatchApplyAndRollbackTruthful(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()

	userID := createTestUser(t, s, "stale-pdx-apply@example.com")
	if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "pdx"}); err != nil {
		t.Fatalf("set pdx region: %v", err)
	}
	insertSignupIPCheckForTest(t, s, "stale-pdx-apply@example.com", `{"country_code":"GB","latitude":51.5,"longitude":-0.12}`)

	dryRun, err := s.runStalePDXBatch(ctx, stalePDXBatchOptions{Mode: "dry_run"})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(dryRun.Rows) == 0 || dryRun.Rows[0].Status != "dry_run_planned" {
		t.Fatalf("dry run rows = %+v, want dry_run_planned", dryRun.Rows)
	}
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Region != "pdx" {
		t.Fatalf("dry run changed user region to %q", user.Region)
	}

	apply, err := s.runStalePDXBatch(ctx, stalePDXBatchOptions{Mode: "apply", Limit: 1})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var applied exedb.UserRegionMigration
	for _, row := range apply.Rows {
		if row.UserID == userID {
			applied = row
		}
	}
	if applied.Status != "apply_succeeded" || applied.TargetRegion != "lon" {
		t.Fatalf("applied row = %+v", applied)
	}
	user, err = withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Region != "lon" {
		t.Fatalf("user region after apply = %q, want lon", user.Region)
	}

	if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "fra"}); err != nil {
		t.Fatalf("force region drift: %v", err)
	}
	rollback, err := s.rollbackStalePDXBatch(ctx, apply.BatchID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(rollback.Rows) != 1 {
		t.Fatalf("rollback rows = %d, want 1", len(rollback.Rows))
	}
	if rollback.Rows[0].Status != "rollback_failed" {
		t.Fatalf("rollback status = %q, want rollback_failed", rollback.Rows[0].Status)
	}
	if rollback.Rows[0].Error == nil || !strings.Contains(*rollback.Rows[0].Error, "CAS failed") {
		t.Fatalf("rollback error = %v, want CAS failed", rollback.Rows[0].Error)
	}
	user, err = withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatal(err)
	}
	if user.Region != "fra" {
		t.Fatalf("rollback failure should not change region, got %q", user.Region)
	}
}

func TestHandleDebugStalePDXCleanupRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()
	userID := createTestUser(t, s, "stale-pdx-http@example.com")
	if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "pdx"}); err != nil {
		t.Fatalf("set pdx region: %v", err)
	}
	insertSignupIPCheckForTest(t, s, "stale-pdx-http@example.com", `{"country_code":"US","latitude":34.05,"longitude":-118.24}`)

	form := url.Values{"mode": {"dry_run"}}
	req := httptest.NewRequest(http.MethodPost, "/debug/stale-pdx-cleanup/run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleDebugStalePDXCleanupRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "mode=dry_run") || !strings.Contains(body, "terminal_event=done") || !strings.Contains(body, "status\tuser_id\temail") || !strings.Contains(body, "dry_run_planned") {
		t.Fatalf("unexpected body:\n%s", body)
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
}

func TestHandleDebugStalePDXCleanupRunApplyRequiresExplicitLimit(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	form := url.Values{"mode": {"apply"}}
	req := httptest.NewRequest(http.MethodPost, "/debug/stale-pdx-cleanup/run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleDebugStalePDXCleanupRun(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "apply requires an explicit limit") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRunStalePDXBatchRespectsLimit(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()

	userIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		email := fmt.Sprintf("stale-pdx-batch-%02d@example.com", i)
		userID := createTestUser(t, s, email)
		userIDs = append(userIDs, userID)
		if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "pdx"}); err != nil {
			t.Fatalf("set pdx region: %v", err)
		}
		insertSignupIPCheckForTest(t, s, email, `{"country_code":"GB","latitude":51.5,"longitude":-0.12}`)
	}

	result, err := s.runStalePDXBatch(ctx, stalePDXBatchOptions{Mode: "apply", Limit: 2})
	if err != nil {
		t.Fatalf("apply with limit: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2; rows=%+v", len(result.Rows), result.Rows)
	}
	if result.Applied != 2 {
		t.Fatalf("applied = %d, want 2", result.Applied)
	}
	appliedUserIDs := map[string]bool{}
	for _, row := range result.Rows {
		appliedUserIDs[row.UserID] = true
		if row.Status != "apply_succeeded" {
			t.Fatalf("row status = %q, want apply_succeeded", row.Status)
		}
	}
	for _, userID := range userIDs {
		user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
		if err != nil {
			t.Fatal(err)
		}
		want := "pdx"
		if appliedUserIDs[userID] {
			want = "lon"
		}
		if user.Region != want {
			t.Fatalf("user %s region = %q, want %q", userID, user.Region, want)
		}
	}
}

func TestHandleDebugStalePDXCleanupStreamsProgressAndSummary(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		email := fmt.Sprintf("stale-pdx-stream-%02d@example.com", i)
		userID := createTestUser(t, s, email)
		if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "pdx"}); err != nil {
			t.Fatalf("set pdx region: %v", err)
		}
		insertSignupIPCheckForTest(t, s, email, `{"country_code":"GB","latitude":51.5,"longitude":-0.12}`)
	}

	form := url.Values{"mode": {"dry_run"}, "limit": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/debug/stale-pdx-cleanup/run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleDebugStalePDXCleanupRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, needle := range []string{"stream=stale_pdx_cleanup", "event=start", "batch_id=stale-pdx-", "limit=1", "event=row", "event=done", "selected=1", "terminal_event=done", "status\tuser_id\temail"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("body missing %q:\n%s", needle, body)
		}
	}
	startLine := firstLineWithPrefix(body, "event=start\t")
	doneLine := firstLineWithPrefix(body, "event=done\t")
	startBatchID := extractTabbedField(startLine, "batch_id")
	doneBatchID := extractTabbedField(doneLine, "batch_id")
	if startBatchID == "" || doneBatchID == "" || startBatchID != doneBatchID {
		t.Fatalf("batch_id mismatch: start=%q done=%q\nbody:\n%s", startBatchID, doneBatchID, body)
	}
}

func firstLineWithPrefix(body, prefix string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func extractTabbedField(line, key string) string {
	for _, field := range strings.Split(line, "\t") {
		if strings.HasPrefix(field, key+"=") {
			return strings.TrimPrefix(field, key+"=")
		}
	}
	return ""
}

func TestBuildStalePDXDecisionsUsesRawEmailWhenCanonicalDiffers(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()

	email := "john.smith+demo@gmail.com"
	userID := createTestUser(t, s, email)
	if err := withTx1(s, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{UserID: userID, Region: "pdx"}); err != nil {
		t.Fatalf("set pdx region: %v", err)
	}
	insertSignupIPCheckForTest(t, s, email, `{"country_code":"GB","latitude":51.5,"longitude":-0.12}`)

	decisions, err := s.buildStalePDXDecisions(ctx)
	if err != nil {
		t.Fatalf("build decisions: %v", err)
	}
	for _, d := range decisions {
		if d.User.UserID == userID {
			if d.TargetRegion != "lon" {
				t.Fatalf("target region = %q, want lon", d.TargetRegion)
			}
			if d.DecisionSource != "signup_ip_check" {
				t.Fatalf("decision source = %q, want signup_ip_check", d.DecisionSource)
			}
			return
		}
	}
	t.Fatalf("decision for user %s not found", userID)
}
