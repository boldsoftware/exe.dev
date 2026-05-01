package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIUsagePool_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/vms/usage/pool", nil)
	req.Host = s.env.WebHost
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIUsagePool_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "pool-method@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/vms/usage/pool", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestAPIUsagePool_GatedWhenNotEnforced(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.env.EnforcePlanCPUMax = false

	user, err := s.createUser(t.Context(), testSSHPubKey, "pool-gated@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vms/usage/pool", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp usagePoolResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Points) != 0 {
		t.Errorf("expected 0 points, got %d", len(resp.Points))
	}
}

func TestAPIUsageHistory_GatedWhenNotEnforced(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.env.EnforcePlanCPUMax = false

	user, err := s.createUser(t.Context(), testSSHPubKey, "history-gated@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vms/usage/history", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if body != "{}" {
		t.Errorf("expected empty JSON object, got %q", body)
	}
}
