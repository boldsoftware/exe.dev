package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIVMsLive_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/vms/live", nil)
	req.Host = s.env.WebHost
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIVMsLive_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "vms-live-method@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/vms/live", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestAPIVMsLive_EmptyResponse(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "vms-live-empty@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vms/live", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp vmsLiveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// User has no VMs, so vms should be empty array (not null).
	if resp.VMs == nil {
		t.Error("expected vms to be empty array, got null")
	}
	if len(resp.VMs) != 0 {
		t.Errorf("expected 0 vms, got %d", len(resp.VMs))
	}

	// No plan → pool limits should be 0 (unlimited).
	if resp.Pool.CPUMax != 0 {
		t.Errorf("expected cpu_max 0 (no plan), got %d", resp.Pool.CPUMax)
	}
	if resp.Pool.MemMaxBytes != 0 {
		t.Errorf("expected mem_max_bytes 0 (no plan), got %d", resp.Pool.MemMaxBytes)
	}
	if resp.Pool.CPUUsed != 0 {
		t.Errorf("expected cpu_used 0, got %f", resp.Pool.CPUUsed)
	}
	if resp.Pool.MemUsedBytes != 0 {
		t.Errorf("expected mem_used_bytes 0, got %d", resp.Pool.MemUsedBytes)
	}
}
