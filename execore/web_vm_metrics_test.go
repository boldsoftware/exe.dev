package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVMMetricsRouting(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	t.Run("unauthenticated_returns_401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/vm/test-vm/compute-usage/live", nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("POST_returns_405", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/vm/test-vm/compute-usage/live", nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("authenticated_nonexistent_vm_returns_404", func(t *testing.T) {
		user, err := server.createUser(t.Context(), testSSHPubKey, "metrics-test@example.com", "", AllQualityChecks)
		if err != nil {
			t.Fatalf("createUser: %v", err)
		}
		cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
		if err != nil {
			t.Fatalf("createAuthCookie: %v", err)
		}

		req := httptest.NewRequest("GET", "/api/vm/nonexistent-vm/compute-usage/live", nil)
		req.Host = server.env.WebHost
		req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
			if resp["error"] == "" {
				t.Errorf("expected error in response, got %q", w.Body.String())
			}
		}
	})

	t.Run("empty_name_not_routed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/vm//compute-usage/live", nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Empty name should not be routed as VM metrics
		if w.Code == http.StatusOK {
			var resp vmMetricsResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil && resp.Name != "" {
				t.Errorf("empty VM name should not return metrics, got %+v", resp)
			}
		}
	})
}
