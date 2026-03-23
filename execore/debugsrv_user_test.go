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

	"exe.dev/billing"
	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestDebugUserBillingAccountsNoAccounts(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-no-accounts@example.com")

	body := debugUserPageBody(t, s, userID)

	if !strings.Contains(body, "<h2>Billing Accounts</h2>") {
		t.Fatalf("expected Billing Accounts section")
	}
	if !strings.Contains(body, "No billing accounts.") {
		t.Fatalf("expected empty billing accounts message")
	}
}

func TestDebugUserBillingAccountsOneAccount(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-one-account@example.com")
	accountID := "exe_debug_single_account"

	err := withTx1(s, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        accountID,
		CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
	}

	_, err = withTxRes1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: accountID,
		EventType: "active",
		EventAt:   time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("InsertBillingEvent(active): %v", err)
	}

	body := debugUserPageBody(t, s, userID)

	requireAccountRow(t, body, accountID, "active")
	dashboardURL := billing.MakeCustomerDashboardURL(accountID)
	if !strings.Contains(body, dashboardURL) {
		t.Fatalf("expected dashboard URL for %q", accountID)
	}
}

func TestDebugUserBillingAccountWithMixedEvents(t *testing.T) {
	// Tests that a single account with multiple billing events (active -> canceled -> active)
	// shows the correct latest status on the debug user page.
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-mixed-events@example.com")
	accountID := "exe_debug_mixed_events"

	err := withTx1(s, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        accountID,
		CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
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
		_, err := withTxRes1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: accountID,
			EventType: ev.eventType,
			EventAt:   ev.at,
		})
		if err != nil {
			t.Fatalf("InsertBillingEvent(%s): %v", ev.eventType, err)
		}
	}

	body := debugUserPageBody(t, s, userID)
	requireAccountRow(t, body, accountID, "active")
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
// (billing_exemption='free') has most entitlements granted.
func TestDebugBillingEntitlementTableFriendUser(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-friend-ent@example.com")

	err := s.db.Tx(context.Background(), func(_ context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE users SET billing_exemption = 'free' WHERE user_id = ?`, userID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to set billing_exemption: %v", err)
	}

	body := debugBillingPageBody(t, s, userID)

	requireEntitlementRow(t, body, "Use LLM Gateway", true)
	requireEntitlementRow(t, body, "Create VMs", true)
	requireEntitlementRow(t, body, "Connect to VMs", true)
	requireEntitlementRow(t, body, "Run VMs", true)
	requireEntitlementRow(t, body, "Purchase Credits", false)
}

// TestDebugBillingEntitlementTableIndividualUser verifies an Individual plan user
// (active billing) has all entitlements granted on the billing page.
// The user has an account + active billing event, which makes GetUserBilling
// return has_billing → Individual plan → all entitlements granted.
func TestDebugBillingEntitlementTableIndividualUser(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-individual-ent@example.com")
	accountID := "exe_debug_individual_ent"

	err := withTx1(s, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        accountID,
		CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
	}
	_, err = withTxRes1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: accountID,
		EventType: "active",
		EventAt:   time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
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
		`<td>` + regexp.QuoteMeta(accountID) + `</td>\s*<td>` + regexp.QuoteMeta(status) + `</td>`,
	)
	if !row.MatchString(body) {
		t.Fatalf("expected billing row account=%q status=%q", accountID, status)
	}
}
