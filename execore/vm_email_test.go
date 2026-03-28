package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/sqlite"
)

func TestVMEmailSend_Success(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create user and box
	email := "owner@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create a box for this user
	boxName := "test-email-box"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Send email request
	reqBody := exeweb.VMEmailRequest{
		To:      email,
		Subject: "Test Subject",
		Body:    "Test body content",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp exeweb.VMEmailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if !resp.Success {
		t.Errorf("Expected success=true, got error: %s", resp.Error)
	}
}

func TestVMEmailSend_MissingBoxHeader(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	reqBody := exeweb.VMEmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Exedev-Box header
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

func TestVMEmailSend_RejectNonTailscaleIP(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.env.GatewayDev = false // Production mode - requires Tailscale IP

	reqBody := exeweb.VMEmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", "any-box")
	// Default httptest.NewRequest uses 192.0.2.1:1234 as RemoteAddr (non-Tailscale)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for non-Tailscale IP, got %d: %s", w.Code, w.Body.String())
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "unauthorized" {
		t.Errorf("Expected error 'unauthorized', got %q", resp.Error)
	}
}

func TestVMEmailSend_BoxNotFound(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	reqBody := exeweb.VMEmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", "nonexistent-box")
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestVMEmailSend_NonOwnerRecipient(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create user and box
	email := "owner@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	boxName := "test-nonowner-box"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Try to send to a different email address
	reqBody := exeweb.VMEmailRequest{
		To:      "other@example.com",
		Subject: "Test",
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected status 403, got %d", w.Code)
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "can only send email to VM owner" {
		t.Errorf("Expected 'can only send email to VM owner' error, got: %s", resp.Error)
	}
}

func TestVMEmailSend_MissingFields(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	tests := []struct {
		name      string
		req       exeweb.VMEmailRequest
		wantError string
	}{
		{
			name:      "missing to",
			req:       exeweb.VMEmailRequest{Subject: "Test", Body: "Test"},
			wantError: "missing required field: to",
		},
		{
			name:      "missing subject",
			req:       exeweb.VMEmailRequest{To: "test@example.com", Body: "Test"},
			wantError: "missing required field: subject",
		},
		{
			name:      "missing body",
			req:       exeweb.VMEmailRequest{To: "test@example.com", Subject: "Test"},
			wantError: "missing required field: body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.req)
			req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Exedev-Box", "any-box")
			w := httptest.NewRecorder()

			server.handleVMEmailSend(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("Expected status 400, got %d", w.Code)
			}

			var resp exeweb.VMEmailResponse
			json.Unmarshal(w.Body.Bytes(), &resp)
			if resp.Error != tt.wantError {
				t.Errorf("Expected error %q, got %q", tt.wantError, resp.Error)
			}
		})
	}
}

func TestVMEmailSend_SubjectTooLong(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a subject that's too long
	longSubject := make([]byte, exeweb.VMEmailMaxSubjectLen+1)
	for i := range longSubject {
		longSubject[i] = 'a'
	}

	reqBody := exeweb.VMEmailRequest{
		To:      "test@example.com",
		Subject: string(longSubject),
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", "any-box")
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	wantError := "subject exceeds maximum length of 200 characters"
	if resp.Error != wantError {
		t.Errorf("Expected error %q, got %q", wantError, resp.Error)
	}
}

func TestVMEmailSend_SubjectWithCRLF(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	tests := []struct {
		name    string
		subject string
	}{
		{"newline", "Test\nInjected"},
		{"carriage_return", "Test\rInjected"},
		{"crlf", "Test\r\nBcc: attacker@evil.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := exeweb.VMEmailRequest{
				To:      "test@example.com",
				Subject: tt.subject,
				Body:    "Test body",
			}
			body, _ := json.Marshal(reqBody)

			req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Exedev-Box", "any-box")
			w := httptest.NewRecorder()

			server.handleVMEmailSend(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("Expected status 400, got %d", w.Code)
			}

			var resp exeweb.VMEmailResponse
			json.Unmarshal(w.Body.Bytes(), &resp)
			if resp.Error != "subject contains invalid characters" {
				t.Errorf("Expected error %q, got %q", "subject contains invalid characters", resp.Error)
			}
		})
	}
}

func TestVMEmailSend_BodyTooLong(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a body that's too long
	longBody := make([]byte, exeweb.VMEmailMaxBodyLen+1)
	for i := range longBody {
		longBody[i] = 'a'
	}

	reqBody := exeweb.VMEmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    string(longBody),
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", "any-box")
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	wantError := "body exceeds maximum length of 65536 bytes"
	if resp.Error != wantError {
		t.Errorf("Expected error %q, got %q", wantError, resp.Error)
	}
}

func TestVMEmailSend_RequestBodyTooLarge(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a request body that exceeds exeweb.VMEmailMaxRequestSize (128KB)
	// We need to create valid JSON that's larger than 128KB
	longBody := make([]byte, exeweb.VMEmailMaxRequestSize+1000)
	for i := range longBody {
		longBody[i] = 'a'
	}

	reqBody := exeweb.VMEmailRequest{
		To:      "test@example.com",
		Subject: "Test",
		Body:    string(longBody),
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", "any-box")
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Expected status 413, got %d: %s", w.Code, w.Body.String())
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "request body too large" {
		t.Errorf("Expected error %q, got %q", "request body too large", resp.Error)
	}
}

func TestVMEmailSend_SubjectAtLimit(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create user and box
	email := "owner-atlimit@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	boxName := "test-subject-atlimit-box"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Create a subject that's exactly at the limit
	maxSubject := make([]byte, exeweb.VMEmailMaxSubjectLen)
	for i := range maxSubject {
		maxSubject[i] = 'a'
	}

	reqBody := exeweb.VMEmailRequest{
		To:      email,
		Subject: string(maxSubject),
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for subject at limit, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVMEmailSend_BodyAtLimit(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create user and box
	email := "owner-bodyatlimit@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	boxName := "test-body-atlimit-box"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Create a body that's exactly at the limit
	maxBody := make([]byte, exeweb.VMEmailMaxBodyLen)
	for i := range maxBody {
		maxBody[i] = 'a'
	}

	reqBody := exeweb.VMEmailRequest{
		To:      email,
		Subject: "Test",
		Body:    string(maxBody),
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for body at limit, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVMEmailSend_InvalidJSON(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader([]byte("not valid json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", "any-box")
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "invalid JSON" {
		t.Errorf("Expected 'invalid JSON' error, got: %s", resp.Error)
	}
}

func TestVMEmailSend_RateLimiting(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create user and box
	email := "ratelimit@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	boxName := "test-ratelimit-box"
	var boxID int64
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		id, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		boxID = id
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Set up the box with zero credit
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.CreateBoxEmailCredit(ctx, exedb.CreateBoxEmailCreditParams{
			BoxID:           boxID,
			AvailableCredit: 0.0, // No credit
			LastRefreshAt:   time.Now(),
		})
	})
	if err != nil {
		t.Fatalf("Failed to set up email credit: %v", err)
	}

	reqBody := exeweb.VMEmailRequest{
		To:      email,
		Subject: "Test",
		Body:    "Test body",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected status 429, got %d: %s", w.Code, w.Body.String())
	}

	var resp exeweb.VMEmailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	wantError := "rate limit exceeded; emails refill at 10/day"
	if resp.Error != wantError {
		t.Errorf("Expected error %q, got %q", wantError, resp.Error)
	}
}

func TestCalculateRefreshedVMEmailCredit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		available     float64
		max           float64
		refreshPerDay float64
		elapsed       time.Duration
		want          float64
	}{
		{
			name:          "no time elapsed",
			available:     10.0,
			max:           50.0,
			refreshPerDay: 10.0,
			elapsed:       0,
			want:          10.0,
		},
		{
			name:          "one day elapsed",
			available:     10.0,
			max:           50.0,
			refreshPerDay: 10.0,
			elapsed:       24 * time.Hour,
			want:          20.0,
		},
		{
			name:          "half day elapsed",
			available:     10.0,
			max:           50.0,
			refreshPerDay: 10.0,
			elapsed:       12 * time.Hour,
			want:          15.0,
		},
		{
			name:          "caps at max",
			available:     45.0,
			max:           50.0,
			refreshPerDay: 10.0,
			elapsed:       24 * time.Hour,
			want:          50.0,
		},
		{
			name:          "already at max",
			available:     50.0,
			max:           50.0,
			refreshPerDay: 10.0,
			elapsed:       24 * time.Hour,
			want:          50.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastRefresh := time.Now().Add(-tt.elapsed)
			now := time.Now()
			got := calculateRefreshedVMEmailCredit(tt.available, tt.max, tt.refreshPerDay, lastRefresh, now)
			// Allow small floating point error
			if got < tt.want-0.01 || got > tt.want+0.01 {
				t.Errorf("calculateRefreshedVMEmailCredit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVMEmailSend_CaseInsensitiveEmailMatch(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create user with lowercase email
	email := "owner@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	boxName := "test-case-box"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Try to send with uppercase email - should work
	reqBody := exeweb.VMEmailRequest{
		To:      "OWNER@EXAMPLE.COM",
		Subject: "Test Subject",
		Body:    "Test body content",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	server.handleVMEmailSend(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (case-insensitive match), got %d: %s", w.Code, w.Body.String())
	}
}
