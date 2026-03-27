package execore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
)

func TestDebugBillingHealth(t *testing.T) {
	s := newTestServer(t)

	// Create a couple users (each gets an account via createTestUser).
	userID1 := createTestUser(t, s, "health1@example.com")
	createTestUser(t, s, "health2@example.com")

	// Give user1 an active billing event.
	account1, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, userID1)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	if err = withTx1(s, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: account1.ID,
		EventType: "active",
		EventAt:   time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("InsertBillingEvent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/debug/billing-health", nil)
	w := httptest.NewRecorder()
	s.handleDebugBillingHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}

	body := w.Body.String()

	// Should contain the page title.
	if !strings.Contains(body, "Billing Health") {
		t.Fatal("missing page title")
	}

	// Should show total accounts (at least 2).
	if !strings.Contains(body, "Total accounts") {
		t.Fatal("missing Total accounts label")
	}

	// Should show the Active Plans section.
	if !strings.Contains(body, "Active Plans") {
		t.Fatal("missing Active Plans section")
	}

	// Should show the Orphaned section.
	if !strings.Contains(body, "Orphaned") {
		t.Fatal("missing Orphaned section")
	}
}
