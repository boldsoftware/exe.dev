package execore

import (
	"context"
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

	dryRun, err := s.runStalePDXBatch(ctx, "dry_run")
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

	apply, err := s.runStalePDXBatch(ctx, "apply")
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
	if !strings.Contains(body, "mode=dry_run") || !strings.Contains(body, "dry_run_planned") {
		t.Fatalf("unexpected body:\n%s", body)
	}
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
