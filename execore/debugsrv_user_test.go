package execore

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
)

func TestDebugUserBillingAccountsOneAccount(t *testing.T) {
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

	requireAccountRow(t, body, account.ID, "active")
	dashboardURL := billing.MakeCustomerDashboardURL(account.ID)
	if !strings.Contains(body, dashboardURL) {
		t.Fatalf("expected dashboard URL for %q", account.ID)
	}
}

func TestDebugUserBillingAccountWithMixedEvents(t *testing.T) {
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
	requireAccountRow(t, body, account.ID, "active")
}

// TestDebugBillingEntitlementTablePresent verifies the entitlement table section
// appears on the debug billing page with one row per entitlement.
func TestDebugBillingEntitlementTablePresent(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-entitlements@example.com")

	body := debugBillingPageBody(t, s, userID)

	if !strings.Contains(body, "<h2>Entitlements</h2>") {
		t.Fatal("expected Entitlements section header")
	}

	// Every concrete entitlement should appear in the table.
	for _, ent := range entitlement.AllEntitlements() {
		if !strings.Contains(body, ent.DisplayName) {
			t.Errorf("entitlement table missing %q", ent.DisplayName)
		}
		if !strings.Contains(body, ent.ID) {
			t.Errorf("entitlement table missing ID %q", ent.ID)
		}
	}
}

// TestDebugBillingEntitlementTableBasicUser verifies a Basic plan user has most
// entitlements denied. Basic grants only llm:use and vm:connect.
func TestDebugBillingEntitlementTableBasicUser(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-basic-ent@example.com")

	body := debugBillingPageBody(t, s, userID)

	requireEntitlementRow(t, body, "Use LLM Gateway", true)
	requireEntitlementRow(t, body, "Connect to VMs", true)
	requireEntitlementRow(t, body, "Create VMs", false)
	requireEntitlementRow(t, body, "Purchase Credits", false)
	requireEntitlementRow(t, body, "Run VMs", false)
}

// TestDebugBillingEntitlementTableFriendUser verifies a Friend plan user
// has most entitlements granted.
func TestDebugBillingEntitlementTableFriendUser(t *testing.T) {
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
		PlanID:    string(entitlement.CategoryFriend),
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan(friend): %v", err)
	}

	body := debugBillingPageBody(t, s, userID)

	requireEntitlementRow(t, body, "Use LLM Gateway", true)
	requireEntitlementRow(t, body, "Create VMs", true)
	requireEntitlementRow(t, body, "Connect to VMs", true)
	requireEntitlementRow(t, body, "Run VMs", true)
	requireEntitlementRow(t, body, "Purchase Credits", false)
}

// TestDebugBillingEntitlementTableIndividualUser verifies an Individual plan user
// has all entitlements granted on the billing page.
func TestDebugBillingEntitlementTableIndividualUser(t *testing.T) {
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
		PlanID:    string(entitlement.CategoryIndividual),
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

	for _, ent := range entitlement.AllEntitlements() {
		requireEntitlementRow(t, body, ent.DisplayName, true)
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
	// Match: <td>DisplayName</td><td><code>id</code></td><td>Status</td> within the same row.
	pattern := `<td>` + regexp.QuoteMeta(displayName) + `</td>\s*<td><code>[^<]+</code></td>\s*<td>` + regexp.QuoteMeta(status) + `</td>`
	re := regexp.MustCompile(pattern)
	if !re.MatchString(body) {
		t.Errorf("entitlement row %q: expected %s, pattern not found", displayName, status)
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

func requireAccountRow(t *testing.T, body, accountID, status string) {
	t.Helper()
	row := regexp.MustCompile(
		`<td>` + regexp.QuoteMeta(accountID) + `</td>\s*<td><code>[^<]*</code></td>\s*<td>` + regexp.QuoteMeta(status) + `</td>`,
	)
	if !row.MatchString(body) {
		t.Fatalf("expected billing row account=%q status=%q", accountID, status)
	}
}
