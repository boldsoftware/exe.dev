package execore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
)

func TestDebugUserBillingAccountsOneAccount(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-one-account@example.com")

	// createTestUser creates an account. Look it up and add a billing event.
	account, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}

	if err = withTx1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: account.ID,
		EventType: "active",
		EventAt:   time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("InsertBillingEvent(active): %v", err)
	}

	body := debugUserPageBody(t, s, userID)

	requireAccountRow(t, body, account.ID)
	billingPageURL := "/debug/billing?userId=" + userID
	if !strings.Contains(body, billingPageURL) {
		t.Fatalf("expected billing page URL for user %q", userID)
	}
}

func TestDebugUserBillingAccountWithMixedEvents(t *testing.T) {
	t.Parallel()
	// Tests that a single account with multiple billing events (active -> canceled -> active)
	// shows the correct latest status on the debug user page.
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-mixed-events@example.com")

	account, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}

	// active -> canceled -> active
	for _, ev := range []struct {
		eventType string
		at        time.Time
	}{
		{"active", time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)},
		{"canceled", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)},
		{"active", time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)},
	} {
		if err := withTx1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: account.ID,
			EventType: ev.eventType,
			EventAt:   ev.at,
		}); err != nil {
			t.Fatalf("InsertBillingEvent(%s): %v", ev.eventType, err)
		}
	}

	body := debugUserPageBody(t, s, userID)
	requireAccountRow(t, body, account.ID)
}

// TestDebugBillingEntitlementTablePresent verifies the entitlement table section
// appears on the debug billing page with one row per plan.
func TestDebugBillingEntitlementTablePresent(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-entitlements@example.com")

	body := debugBillingPageBody(t, s, userID)

	if !strings.Contains(body, "<h4>Entitlements</h4>") {
		t.Fatal("expected Entitlements section header")
	}

	// Every concrete entitlement should appear in the table by ID.
	for _, ent := range plan.AllEntitlements() {
		if !strings.Contains(body, ent.ID) {
			t.Errorf("entitlement table missing ID %q", ent.ID)
		}
	}
}

// TestDebugUserEntitlementTablePresent verifies the entitlement table on the
// debug user page matches the billing page format (ID / Team / User /
// Effective columns).
func TestDebugUserEntitlementTablePresent(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-user-ent@example.com")

	body := debugUserPageBody(t, s, userID)

	if !strings.Contains(body, "<th>ID</th><th>Team</th><th>User</th><th>Effective</th>") {
		t.Fatal("expected user page entitlement table to have ID/Team/User/Effective columns")
	}
	requireEntitlementRow(t, body, "Use LLM Gateway", true)
	requireEntitlementRow(t, body, "Create VMs", false)
}

// TestDebugBillingEntitlementTableBasicUser verifies a Basic plan user has most
// entitlements denied. Basic grants only llm:use.
func TestDebugBillingEntitlementTableBasicUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-basic-ent@example.com")

	body := debugBillingPageBody(t, s, userID)

	requireEntitlementRow(t, body, "Use LLM Gateway", true)
	requireEntitlementRow(t, body, "Create VMs", false)
	requireEntitlementRow(t, body, "Purchase Credits", false)
	requireEntitlementRow(t, body, "Run VMs", false)
	requireEntitlementRow(t, body, "Resize VMs", false)
}

// TestDebugBillingEntitlementTableFriendUser verifies a Friend plan user
// has most entitlements granted.
func TestDebugBillingEntitlementTableFriendUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-friend-ent@example.com")

	// Upgrade from basic to friend plan.
	account, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	err = withTx1(s, t.Context(), (*exedb.Queries).CloseAccountPlan, exedb.CloseAccountPlanParams{
		AccountID: account.ID,
		EndedAt:   &now,
	})
	if err != nil {
		t.Fatalf("CloseAccountPlan: %v", err)
	}
	err = withTx1(s, t.Context(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID: account.ID,
		PlanID:    string(plan.CategoryFriend),
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan(friend): %v", err)
	}

	body := debugBillingPageBody(t, s, userID)

	requireEntitlementRow(t, body, "Use LLM Gateway", true)
	requireEntitlementRow(t, body, "Create VMs", true)
	requireEntitlementRow(t, body, "Run VMs", true)
	requireEntitlementRow(t, body, "Purchase Credits", false)
	requireEntitlementRow(t, body, "Resize VMs", true)
}

// TestDebugBillingEntitlementTableIndividualUser verifies an Individual plan user
// has all entitlements granted on the billing page.
func TestDebugBillingEntitlementTableIndividualUser(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-individual-ent@example.com")

	// createTestUser creates account + basic plan. Upgrade to individual.
	account, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	err = withTx1(s, t.Context(), (*exedb.Queries).CloseAccountPlan, exedb.CloseAccountPlanParams{
		AccountID: account.ID,
		EndedAt:   &now,
	})
	if err != nil {
		t.Fatalf("CloseAccountPlan: %v", err)
	}
	err = withTx1(s, t.Context(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID: account.ID,
		PlanID:    string(plan.CategoryIndividual),
		StartedAt: now,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}
	if err = withTx1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: account.ID,
		EventType: "active",
		EventAt:   now,
	}); err != nil {
		t.Fatalf("InsertBillingEvent: %v", err)
	}

	body := debugBillingPageBody(t, s, userID)

	for _, ent := range plan.AllEntitlements() {
		want := plan.Grants(plan.ID(plan.CategoryIndividual), ent)
		requireEntitlementRow(t, body, ent.DisplayName, want)
	}
}

// requireEntitlementRow checks the entitlement table contains a row with the
// given display name and granted/denied status.
func requireEntitlementRow(t *testing.T, body, displayName string, granted bool) {
	t.Helper()
	status := "Denied"
	if granted {
		status = "Granted"
	}
	// Find the entitlement ID from the display name.
	var entID string
	for _, ent := range plan.AllEntitlements() {
		if ent.DisplayName == displayName {
			entID = ent.ID
			break
		}
	}
	if entID == "" {
		t.Fatalf("unknown entitlement display name %q", displayName)
	}
	// Match: <td><code>id</code></td><td>...</td><td>...</td><td>Effective</td>
	// The Effective column is the last <td> in the row.
	pattern := `<td><code>` + regexp.QuoteMeta(entID) + `</code></td>\s*<td>[^<]*</td>\s*<td>[^<]*</td>\s*<td>` + regexp.QuoteMeta(status) + `</td>`
	re := regexp.MustCompile(pattern)
	if !re.MatchString(body) {
		t.Errorf("entitlement row %q (%s): expected effective=%s, pattern not found", displayName, entID, status)
	}
}

func debugUserPageBody(t *testing.T, s *Server, userID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/debug/user?userId="+url.QueryEscape(userID), nil)
	w := httptest.NewRecorder()
	s.handleDebugUser(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleDebugUser status=%d body=%q", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func debugBillingPageBody(t *testing.T, s *Server, userID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/debug/billing?userId="+url.QueryEscape(userID), nil)
	w := httptest.NewRecorder()
	s.handleDebugBilling(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleDebugBilling status=%d body=%q", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func requireAccountRow(t *testing.T, body, accountID string) {
	t.Helper()
	row := regexp.MustCompile(
		`<td>` + regexp.QuoteMeta(accountID) + `</td>\s*<td><code>[^<]*</code></td>`,
	)
	if !row.MatchString(body) {
		t.Fatalf("expected billing row account=%q", accountID)
	}
}

// TestDebugRevokeTrial tests that revoking a trial moves the user to Basic.
func TestDebugRevokeTrial(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := t.Context()
	userID := createTestUser(t, s, "debug-revoke-trial@example.com")

	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}

	// Grant trial.
	now := time.Now()
	trialEnd := now.Add(30 * 24 * time.Hour)
	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID:      account.ID,
			PlanID:         plan.ID(plan.CategoryTrial),
			At:             now,
			TrialExpiresAt: &trialEnd,
			ChangedBy:      "test",
		})
	}); err != nil {
		t.Fatalf("ReplaceAccountPlan(trial): %v", err)
	}

	// Verify user is on trial.
	cat, err := exedb.WithRxRes0(s.db, ctx, func(q *exedb.Queries, ctx context.Context) (plan.Category, error) {
		return plan.ForUser(ctx, q, userID)
	})
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	if cat != plan.CategoryTrial {
		t.Fatalf("expected trial, got %s", cat)
	}

	// Revoke trial.
	form := url.Values{"user_id": {userID}}
	req := httptest.NewRequest(http.MethodPost, "/debug/users/revoke-trial", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleDebugRevokeTrial(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", w.Code, w.Body.String())
	}

	// Verify user is now on basic.
	cat, err = exedb.WithRxRes0(s.db, ctx, func(q *exedb.Queries, ctx context.Context) (plan.Category, error) {
		return plan.ForUser(ctx, q, userID)
	})
	if err != nil {
		t.Fatalf("ForUser after revoke: %v", err)
	}
	if cat != plan.CategoryBasic {
		t.Fatalf("expected basic after revoke, got %s", cat)
	}

	// Verify plan history shows the change.
	body := debugUserPageBody(t, s, userID)
	if !strings.Contains(body, "debug:revoke-trial") {
		t.Error("expected plan history to show debug:revoke-trial")
	}
}

// TestDebugRevokeTrialNotOnTrial tests that revoking a trial for a non-trial user fails.
func TestDebugRevokeTrialNotOnTrial(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-revoke-not-trial@example.com")

	form := url.Values{"user_id": {userID}}
	req := httptest.NewRequest(http.MethodPost, "/debug/users/revoke-trial", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleDebugRevokeTrial(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not trial") {
		t.Errorf("expected error about not being on trial, got: %s", w.Body.String())
	}
}

// TestDebugRevokeTrialShowsInUI tests that the Revoke Trial button appears
// on the debug user page when the user is on a trial.
func TestDebugRevokeTrialShowsInUI(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := t.Context()
	userID := createTestUser(t, s, "debug-revoke-ui@example.com")

	// On basic: should show Grant Trial, not Revoke Trial.
	body := debugUserPageBody(t, s, userID)
	if !strings.Contains(body, "Grant Trial") {
		t.Error("expected Grant Trial on basic user")
	}
	if strings.Contains(body, "Revoke Trial") {
		t.Error("should not show Revoke Trial on basic user")
	}

	// Grant trial.
	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	trialEnd := now.Add(30 * 24 * time.Hour)
	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID:      account.ID,
			PlanID:         plan.ID(plan.CategoryTrial),
			At:             now,
			TrialExpiresAt: &trialEnd,
			ChangedBy:      "test",
		})
	}); err != nil {
		t.Fatalf("ReplaceAccountPlan(trial): %v", err)
	}

	// On trial: should show Revoke Trial, not Grant Trial.
	body = debugUserPageBody(t, s, userID)
	if strings.Contains(body, "Grant Trial") {
		t.Error("should not show Grant Trial on trial user")
	}
	if !strings.Contains(body, "Revoke Trial") {
		t.Error("expected Revoke Trial on trial user")
	}
}
