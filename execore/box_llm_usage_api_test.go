package execore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exe.dev/exedb"
)

func TestAPILLMUsage_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/llm-usage", nil)
	req.Host = s.env.WebHost
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPILLMUsage_DateParam(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "llmusage-params@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookie, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	tests := []struct {
		name string
		url  string
		code int
	}{
		{"no date (current period)", "/api/llm-usage", http.StatusOK},
		{"valid date", "/api/llm-usage?date=2026-03-15", http.StatusOK},
		{"invalid date format", "/api/llm-usage?date=not-a-date", http.StatusBadRequest},
		{"wrong date format", "/api/llm-usage?date=2026-3-1", http.StatusBadRequest},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		req.Host = s.env.WebHost
		req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Errorf("%s: expected %d, got %d", tc.name, tc.code, w.Code)
		}
	}
}

func TestAPILLMUsage_WithData(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "llmusage-data@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookie, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		box1ID, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost: "test-host", Name: "llm-box-1", Status: "running",
			Image: "test-image", CreatedByUserID: user.UserID, Region: "pdx",
		})
		if err != nil {
			return err
		}
		box2ID, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost: "test-host", Name: "llm-box-2", Status: "running",
			Image: "test-image", CreatedByUserID: user.UserID, Region: "pdx",
		})
		if err != nil {
			return err
		}
		if err := q.RecordBoxLLMUsage(ctx, exedb.RecordBoxLLMUsageParams{
			BoxID: int(box1ID), UserID: user.UserID, Provider: "anthropic", Model: "claude-sonnet-4-20250514",
			CostMicrocents: 500_000,
		}); err != nil {
			return err
		}
		if err := q.RecordBoxLLMUsage(ctx, exedb.RecordBoxLLMUsageParams{
			BoxID: int(box2ID), UserID: user.UserID, Provider: "openai", Model: "gpt-4.1-nano",
			CostMicrocents: 10_000,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Current period (no date param) should include today's data.
	req := httptest.NewRequest(http.MethodGet, "/api/llm-usage", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp llmUsageResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.PeriodStart == "" || resp.PeriodEnd == "" {
		t.Fatal("response missing periodStart/periodEnd")
	}

	// Both inserts happen in the same CURRENT_TIMESTAMP call → same day → 1 day group.
	if len(resp.Days) != 1 {
		t.Fatalf("expected 1 day group, got %d", len(resp.Days))
	}

	day := resp.Days[0]
	if len(day.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(day.Entries))
	}

	// Entries ordered by cost descending.
	if day.Entries[0].Box != "llm-box-1" {
		t.Errorf("first entry box = %q, want llm-box-1", day.Entries[0].Box)
	}
	if day.Entries[0].Model != "claude-sonnet-4-20250514" {
		t.Errorf("first entry model = %q, want claude-sonnet-4-20250514", day.Entries[0].Model)
	}
	if day.Entries[1].Box != "llm-box-2" {
		t.Errorf("second entry box = %q, want llm-box-2", day.Entries[1].Box)
	}

	if resp.TotalCount != 2 {
		t.Errorf("totalCount = %d, want 2", resp.TotalCount)
	}
	if resp.TotalCost != "$0.51" {
		t.Errorf("totalCost = %q, want $0.51", resp.TotalCost)
	}
}

func TestAPILLMUsage_Empty(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "llmusage-empty@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookie, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/llm-usage", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp llmUsageResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Days) != 0 {
		t.Errorf("expected 0 days, got %d", len(resp.Days))
	}
	if resp.TotalCount != 0 {
		t.Errorf("totalCount = %d, want 0", resp.TotalCount)
	}
}

func TestFormatMicrocents(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "$0.00"},
		{1_000_000, "$1.00"},
		{500_000, "$0.50"},
		{10_000, "$0.01"},
		{1_500_000, "$1.50"},
		{123_456_789, "$123.46"},
		{100, "$0.00"},
		{5_000, "$0.01"}, // rounds up
		{4_999, "$0.00"}, // rounds down
		{510_000, "$0.51"},
		{800_000, "$0.80"},
		{810_000, "$0.81"},
		{65_958, "$0.07"},    // sub-cent rounds up
		{1_481_958, "$1.48"}, // sub-cent rounds down
	}
	for _, tc := range tests {
		got := formatMicrocents(tc.input)
		if got != tc.want {
			t.Errorf("formatMicrocents(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAPIBoxLLMUsage_EmptyForMissingVM(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "llmusage-missingvm@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookie, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vm/ghost/llm-usage", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp boxLLMUsageResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(resp.Models))
	}
	if resp.TotalCost != "$0.00" {
		t.Fatalf("expected $0.00 totalCost, got %q", resp.TotalCost)
	}
	if resp.PeriodStart == "" || resp.PeriodEnd == "" {
		t.Fatal("response missing periodStart/periodEnd")
	}
}

func TestAPIBoxLLMUsage_WithDataIncludesPeriod(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "llmusage-boxdata@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookie, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		boxID, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost: "test-host", Name: "my-vm", Status: "running",
			Image: "test-image", CreatedByUserID: user.UserID, Region: "pdx",
		})
		if err != nil {
			return err
		}
		return q.RecordBoxLLMUsage(ctx, exedb.RecordBoxLLMUsageParams{
			BoxID: int(boxID), UserID: user.UserID, Provider: "anthropic", Model: "claude-sonnet-4-20250514",
			CostMicrocents: 500_000,
		})
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vm/my-vm/llm-usage", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp boxLLMUsageResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.Models))
	}
	if resp.PeriodStart == "" || resp.PeriodEnd == "" {
		t.Fatal("response missing periodStart/periodEnd")
	}

	start, err := time.Parse(time.RFC3339, resp.PeriodStart)
	if err != nil {
		t.Fatalf("parse periodStart: %v", err)
	}
	end, err := time.Parse(time.RFC3339, resp.PeriodEnd)
	if err != nil {
		t.Fatalf("parse periodEnd: %v", err)
	}
	wantStart, wantEnd := calendarMonthPeriod(time.Now().UTC())
	if !start.Equal(wantStart) {
		t.Fatalf("periodStart = %s, want %s", start, wantStart)
	}
	if !end.Equal(wantEnd) {
		t.Fatalf("periodEnd = %s, want %s", end, wantEnd)
	}
}

func TestAPIBoxLLMUsage_LookupErrorReturns500(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "llmusage-boxlookup@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/vm/my-vm/llm-usage", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	s.handleAPIBoxLLMUsage(w, req, user.UserID, "my-vm")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
