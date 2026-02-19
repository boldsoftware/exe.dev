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
	"exe.dev/exedb"
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

func TestDebugUserBillingAccountsManyMixedStatuses(t *testing.T) {
	s := newTestServer(t)
	userID := createTestUser(t, s, "debug-mixed-accounts@example.com")

	canceledAccountID := "exe_debug_a_canceled"
	pendingAccountID := "exe_debug_b_pending"
	activeAccountID := "exe_debug_c_active"

	for _, accountID := range []string{
		activeAccountID,
		canceledAccountID,
		pendingAccountID,
	} {
		err := withTx1(s, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		})
		if err != nil {
			t.Fatalf("InsertAccount(%q): %v", accountID, err)
		}
	}

	_, err := withTxRes1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: canceledAccountID,
		EventType: "active",
		EventAt:   time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("InsertBillingEvent(active): %v", err)
	}
	_, err = withTxRes1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: canceledAccountID,
		EventType: "canceled",
		EventAt:   time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("InsertBillingEvent(canceled): %v", err)
	}
	_, err = withTxRes1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: activeAccountID,
		EventType: "active",
		EventAt:   time.Date(2026, 1, 12, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("InsertBillingEvent(active): %v", err)
	}

	body := debugUserPageBody(t, s, userID)

	requireAccountRow(t, body, canceledAccountID, "canceled")
	requireAccountRow(t, body, pendingAccountID, "pending")
	requireAccountRow(t, body, activeAccountID, "active")

	canceledIdx := strings.Index(body, canceledAccountID)
	pendingIdx := strings.Index(body, pendingAccountID)
	activeIdx := strings.Index(body, activeAccountID)
	if canceledIdx == -1 || pendingIdx == -1 || activeIdx == -1 {
		t.Fatalf("missing account IDs in rendered billing accounts table")
	}
	if !(canceledIdx < pendingIdx && pendingIdx < activeIdx) {
		t.Fatalf(
			"expected deterministic account ordering by account ID, got indexes canceled=%d pending=%d active=%d",
			canceledIdx,
			pendingIdx,
			activeIdx,
		)
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

func requireAccountRow(t *testing.T, body, accountID, status string) {
	t.Helper()
	row := regexp.MustCompile(
		`<td>` + regexp.QuoteMeta(accountID) + `</td>\s*<td>` + regexp.QuoteMeta(status) + `</td>`,
	)
	if !row.MatchString(body) {
		t.Fatalf("expected billing row account=%q status=%q", accountID, status)
	}
}
