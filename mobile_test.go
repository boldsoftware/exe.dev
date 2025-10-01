package exe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMobileHomeRoute(t *testing.T) {
	server := NewTestServer(t)

	// Test GET /m
	req := httptest.NewRequest("GET", "/m", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "exe.dev") {
		t.Error("Expected exe.dev title in response")
	}
	if !strings.Contains(body, "Create") {
		t.Error("Expected Create button in response")
	}
}

func TestMobileHostnameCheck(t *testing.T) {
	server := NewTestServer(t)

	// Test hostname availability check
	reqBody := `{"hostname": "test-hostname"}`
	req := httptest.NewRequest("POST", "/m/check-hostname", strings.NewReader(reqBody))
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

func TestMobileCreateVMFlow(t *testing.T) {
	server := NewTestServer(t)

	// Test VM creation form submission
	form := url.Values{}
	form.Add("hostname", "test-vm")
	form.Add("description", "A test VM")

	req := httptest.NewRequest("POST", "/m/create-vm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "test-vm.exe.dev") {
		t.Error("Expected VM hostname in response")
	}
	if !strings.Contains(body, "A test VM") {
		t.Error("Expected VM description in response")
	}
	if !strings.Contains(body, "Enter your email to continue") {
		t.Error("Expected email prompt in response")
	}
}

func TestMobileEmailAuth(t *testing.T) {
	server := NewTestServer(t)

	// Test email authentication
	form := url.Values{}
	form.Add("email", "test@example.com")

	req := httptest.NewRequest("POST", "/m/email-auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Check Your Email") {
		t.Errorf("expected email check message in response, got:\n%s", body)
	}
	if !strings.Contains(body, "test@example.com") {
		t.Errorf("expected email address in response, got:\n%s", body)
	}
}

func TestMobileVMListUnauthorized(t *testing.T) {
	server := NewTestServer(t)

	// Test VM list without authentication
	req := httptest.NewRequest("GET", "/m/home", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to /m
	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("Expected status 307, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	if location != "/m" {
		t.Errorf("Expected redirect to /m, got %s", location)
	}
}

func TestMobileInvalidHostname(t *testing.T) {
	server := NewTestServer(t)

	// Test invalid hostname check
	reqBody := `{"hostname": "a"}`
	req := httptest.NewRequest("POST", "/m/check-hostname", strings.NewReader(reqBody))
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
	server := NewTestServer(t)

	// Test invalid email
	form := url.Values{}
	form.Add("email", "invalid-email")

	req := httptest.NewRequest("POST", "/m/email-auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
