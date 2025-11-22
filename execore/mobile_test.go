package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMobileHostnameCheck(t *testing.T) {
	server := newTestServer(t)

	// Test hostname availability check
	reqBody := `{"hostname": "test-hostname"}`
	req := httptest.NewRequest("POST", "/m/check-hostname", strings.NewReader(reqBody))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}

	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if !response.Valid {
		t.Error("Expected hostname to be valid")
	}
	if !response.Available {
		t.Error("Expected hostname to be available")
	}
	if response.Message != "" {
		t.Errorf("Expected empty message for available hostname, got %q", response.Message)
	}
}

func TestMobileVMListUnauthorized(t *testing.T) {
	server := newTestServer(t)

	// Test VM list without authentication
	req := httptest.NewRequest("GET", "/m/home", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to /m
	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status 303, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	if location != "/m" {
		t.Errorf("Expected redirect to /m, got %s", location)
	}
}

func TestMobileInvalidHostname(t *testing.T) {
	server := newTestServer(t)

	// Test invalid hostname check
	reqBody := `{"hostname": "a"}`
	req := httptest.NewRequest("POST", "/m/check-hostname", strings.NewReader(reqBody))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}

	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if response.Valid {
		t.Error("Expected hostname to be invalid")
	}
	if !strings.Contains(response.Message, "Invalid") {
		t.Error("Expected invalid hostname message")
	}
}

func TestMobileInvalidEmail(t *testing.T) {
	server := newTestServer(t)

	// Test invalid email
	form := url.Values{}
	form.Add("email", "invalid-email")

	req := httptest.NewRequest("POST", "/m/email-auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
