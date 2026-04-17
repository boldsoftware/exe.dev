package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIVMComputeUsage(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a user and VM once for the subtests that need auth.
	user, err := server.createUser(t.Context(), generateTestSSHKey(t), "history-test@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}
	_, err = server.preCreateBox(t.Context(), preCreateBoxOptions{
		userID:        user.UserID,
		name:          "test-vm-history",
		image:         "ubuntu:latest",
		region:        "pdx",
		ctrhost:       "",
		noShard:       false,
		allocatedCPUs: 0,
	})
	if err != nil {
		t.Fatalf("preCreateBox: %v", err)
	}

	t.Run("unauthenticated_returns_401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/vm/test-vm-history/compute-usage", nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("POST_returns_405", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/vm/test-vm-history/compute-usage", nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("authenticated_nonexistent_vm_returns_404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/vm/nonexistent-vm/compute-usage", nil)
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

	t.Run("metrics_unavailable_returns_503", func(t *testing.T) {
		// Test server has metricsdURL == "", so should return 503.
		req := httptest.NewRequest("GET", "/api/vm/test-vm-history/compute-usage", nil)
		req.Host = server.env.WebHost
		req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
			if resp["error"] == "" {
				t.Errorf("expected error in response, got %q", w.Body.String())
			}
		}
	})

	t.Run("invalid_hours_returns_400", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			hours string
		}{
			{"negative", "-1"},
			{"zero", "0"},
			{"too_large", "1000"},
			{"non_numeric", "abc"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest("GET", "/api/vm/test-vm-history/compute-usage?hours="+tc.hours, nil)
				req.Host = server.env.WebHost
				req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
				w := httptest.NewRecorder()
				server.ServeHTTP(w, req)

				if w.Code != http.StatusBadRequest {
					t.Errorf("expected 400 for hours=%s, got %d: %s", tc.hours, w.Code, w.Body.String())
				}
			})
		}
	})

	t.Run("empty_name_not_routed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/vm//compute-usage", nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Empty name is not routed by routeAPIVM, so falls through.
		if w.Code == http.StatusOK {
			t.Errorf("empty VM name should not return 200, got %d", w.Code)
		}
	})
}
