package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/sqlite"
)

func TestValidateCallbackURI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		uri     string
		wantErr bool
	}{
		{"exedev-app://auth", false},
		{"myapp://callback", false},
		{"com.example.app://auth", false},
		{"https://evil.com/steal", true},
		{"http://localhost:8080/callback", true},
		{"javascript:alert(1)", true},
		{"data:text/html,<script>alert(1)</script>", true},
		{"blob:http://evil.com/uuid", true},
		{"vbscript:msgbox", true},
		{"file:///etc/passwd", true},
		{"", true},
		{"://noscheme", true},
	}
	for _, tt := range tests {
		err := validateCallbackURI(tt.uri)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateCallbackURI(%q) error = %v, wantErr %v", tt.uri, err, tt.wantErr)
		}
	}
}

// createTestUserWithCookie creates a user via the email verification flow and returns the auth cookie.
func createTestUserWithCookie(t *testing.T, s *Server, email string) string {
	t.Helper()
	port := s.httpPort()

	form := url.Values{}
	form.Set("email", email)
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", port),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var token string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT token FROM email_verifications WHERE email = ?`, email).Scan(&token)
	})
	if err != nil {
		t.Fatal("no verification token:", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/verify-email?token=%s", port, token), nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	for _, c := range resp.Cookies() {
		if c.Name == "exe-auth" {
			return c.Value
		}
	}
	t.Fatal("no exe-auth cookie")
	return ""
}

// createTestUserWithAppToken creates a user via the app_token flow and returns the app token.
func createTestUserWithAppToken(t *testing.T, s *Server, email string) string {
	t.Helper()
	port := s.httpPort()

	form := url.Values{}
	form.Set("email", email)
	form.Set("response_mode", "app_token")
	form.Set("callback_uri", "exedev-app://auth")
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", port),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Should show the code entry page.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Enter Code") && !strings.Contains(bodyStr, "verify-code") {
		t.Fatalf("expected code entry page, got: %s", bodyStr[:min(200, len(bodyStr))])
	}

	// Retrieve the verification code from DB.
	var code string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT verification_code FROM email_verifications WHERE email = ?`, email).Scan(&code)
	})
	if err != nil {
		t.Fatal("no verification record:", err)
	}
	if code == "" {
		t.Fatal("verification code not set")
	}

	// Submit the code.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form2 := url.Values{}
	form2.Set("email", email)
	form2.Set("code", code)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/auth/verify-code", port), strings.NewReader(form2.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	// The app token flow renders the success/passkey page.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// An auth cookie should be set (for passkey registration on the page).
	hasCookie := false
	for _, c := range resp.Cookies() {
		if c.Name == "exe-auth" {
			hasCookie = true
		}
	}
	if !hasCookie {
		t.Fatal("exe-auth cookie should be set for passkey registration")
	}

	// The page should contain the callback URL with the app token.
	bodyStr = string(body)
	if !strings.Contains(bodyStr, "exedev-app") || !strings.Contains(bodyStr, "exeapp_") {
		t.Fatal("page should contain callback URL with app token")
	}

	// Extract the app token from the database.
	var userID string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID)
	})
	if err != nil {
		t.Fatal("no user:", err)
	}
	tokens, err := withRxRes1(s, context.Background(), (*exedb.Queries).GetAppTokensByUserID, userID)
	if err != nil || len(tokens) == 0 {
		t.Fatal("no app token in DB")
	}
	return tokens[0].Token
}

func TestAppTokenFlowEmailVerification(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "apptest@example.com")

	if !strings.HasPrefix(appToken, exeweb.AppTokenPrefix) {
		t.Fatalf("token should start with %s, got %q", exeweb.AppTokenPrefix, appToken)
	}

	at, err := withRxRes1(s, context.Background(), (*exedb.Queries).GetAppTokenInfo, appToken)
	if err != nil {
		t.Fatal("app token not found in DB:", err)
	}
	if at.Name != "iOS" {
		t.Fatalf("expected name 'iOS', got %q", at.Name)
	}
}

func TestAppTokenAsBearer(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "bearer@example.com")

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", s.httpURL()+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /user with app token, got %d", resp.StatusCode)
	}
}

func TestAppTokenOnExecEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "exectest@example.com")

	req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 from /exec, got %d, body: %s", resp.StatusCode, body)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal("failed to decode response:", err)
	}
	email, _ := result["email"].(string)
	if email != "exectest@example.com" {
		t.Fatalf("expected email exectest@example.com, got %q", email)
	}
}

func TestAppTokenFlowRejectsHTTPCallback(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", s.httpURL()+"/auth?response_mode=app_token&callback_uri="+url.QueryEscape("https://evil.com/steal"), nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for https callback_uri, got %d", resp.StatusCode)
	}
}

func TestAppTokenFlowAlreadyAuthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	cookieValue := createTestUserWithCookie(t, s, "existing@example.com")

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", s.httpURL()+"/auth?response_mode=app_token&callback_uri="+url.QueryEscape("exedev-app://auth"), nil)
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Now renders a passkey prompt page instead of redirecting.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "exedev-app") || !strings.Contains(bodyStr, "exeapp_") {
		t.Fatal("page should contain callback URL with app token")
	}
}

func TestCookieNotAcceptedAsBearer(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	cookieValue := createTestUserWithCookie(t, s, "cookie@example.com")

	// Try to use the cookie value as a Bearer token on /exec.
	req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
	req.Header.Set("Authorization", "Bearer "+cookieValue)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 when using cookie as bearer token, got %d", resp.StatusCode)
	}
}

func TestAppTokenBypassesCmdRestrictions(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "cmdtest@example.com")

	// "doc" is NOT in DefaultTokenCmds, so an SSH-signed token would be rejected.
	// App tokens should bypass cmd restrictions entirely.
	req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("doc"))
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("app token should not be subject to cmd restrictions, but got 403")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 from /exec with doc command, got %d: %s", resp.StatusCode, body)
	}
}

func TestAppTokenCapRevokesOldest(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// Create a user via the normal cookie flow so we have a user ID.
	cookieValue := createTestUserWithCookie(t, s, "cap@example.com")

	// Look up the user ID from the cookie.
	var userID string
	err := s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM auth_cookies WHERE cookie_value = ?`, cookieValue).Scan(&userID)
	})
	if err != nil {
		t.Fatal("no user for cookie:", err)
	}

	// Create maxAppTokensPerUser + 3 tokens.
	var tokens []string
	for i := 0; i < maxAppTokensPerUser+3; i++ {
		tok, err := s.createAppToken(context.Background(), userID)
		if err != nil {
			t.Fatalf("createAppToken #%d: %v", i, err)
		}
		tokens = append(tokens, tok)
	}

	// Verify only maxAppTokensPerUser tokens remain.
	remaining, err := withRxRes1(s, context.Background(), (*exedb.Queries).GetAppTokensByUserID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != maxAppTokensPerUser {
		t.Fatalf("expected %d tokens, got %d", maxAppTokensPerUser, len(remaining))
	}

	// The newest token should still be valid.
	newest := tokens[len(tokens)-1]
	if _, err := s.validateAppToken(context.Background(), newest); err != nil {
		t.Fatalf("newest token should be valid: %v", err)
	}

	// The oldest token should have been revoked.
	oldest := tokens[0]
	if _, err := s.validateAppToken(context.Background(), oldest); err == nil {
		t.Fatal("oldest token should have been revoked")
	}
}

func TestAppTokenFlowStoresParamsInEmailVerification(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	form := url.Values{}
	form.Set("email", "params@example.com")
	form.Set("response_mode", "app_token")
	form.Set("callback_uri", "exedev-app://auth")
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", s.httpPort()),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var rm, cb, vc *string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT response_mode, callback_uri, verification_code FROM email_verifications WHERE email = ?`, "params@example.com").Scan(&rm, &cb, &vc)
	})
	if err != nil {
		t.Fatal(err)
	}
	if rm == nil || *rm != "app_token" {
		t.Fatalf("expected response_mode=app_token, got %v", rm)
	}
	if cb == nil || *cb != "exedev-app://auth" {
		t.Fatalf("expected callback_uri=exedev-app://auth, got %v", cb)
	}
	if vc == nil || len(*vc) != 8 {
		t.Fatalf("expected 8-char hex verification_code, got %v", vc)
	}
}

func TestAppTokenVerifyCodeWrongCode(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	port := s.httpPort()
	const testEmail = "wrongcode@example.com"

	// Start the app_token email flow.
	form := url.Values{}
	form.Set("email", testEmail)
	form.Set("response_mode", "app_token")
	form.Set("callback_uri", "exedev-app://auth")
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", port),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Submit a wrong code — should show error but allow retry.
	submitCode := func(code string) (int, string) {
		f := url.Values{}
		f.Set("email", testEmail)
		f.Set("code", code)
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/auth/verify-code", port), strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// First wrong attempt — should show remaining count.
	status, body := submitCode("00000000")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !strings.Contains(body, "Incorrect code") || !strings.Contains(body, "4 attempts remaining") {
		t.Fatalf("expected error with 4 remaining, got: %s", body[:min(300, len(body))])
	}

	// Verification record should NOT be consumed (can retry).
	var count int
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT count(*) FROM email_verifications WHERE email = ?`, testEmail).Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("verification should not be consumed on wrong code")
	}

	// Exhaust remaining attempts (4 more = 5 total).
	for i := 0; i < 3; i++ {
		submitCode("00000000")
	}
	// 5th attempt should lock it out.
	status, body = submitCode("00000000")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if !strings.Contains(body, "Too many attempts") {
		t.Fatalf("expected lockout message, got: %s", body[:min(300, len(body))])
	}

	// Verification record should be deleted.
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT count(*) FROM email_verifications WHERE email = ?`, testEmail).Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("verification should be deleted after too many attempts")
	}
}

func TestAppTokenCodeEntryDoesNotExposeToken(t *testing.T) {
	t.Parallel()
	// The code entry page must not contain the verification token.
	// The token is a secret that proves email ownership in the link-based flow;
	// exposing it would let someone skip the code check via /verify-email.
	s := newTestServer(t)
	port := s.httpPort()

	// Start the app_token email flow.
	form := url.Values{}
	form.Set("email", "notoken@example.com")
	form.Set("response_mode", "app_token")
	form.Set("callback_uri", "exedev-app://auth")
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", port),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Get the token from DB — it must NOT appear in the page.
	var verifyToken string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT token FROM email_verifications WHERE email = ?`, "notoken@example.com").Scan(&verifyToken)
	})
	if err != nil {
		t.Fatal("no verification token:", err)
	}

	if strings.Contains(string(body), verifyToken) {
		t.Fatal("code entry page must not contain the verification token")
	}
}

func TestAppTokenInProxyCookie(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "proxycookie@example.com")

	// Look up user ID.
	var userID string
	err := s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "proxycookie@example.com").Scan(&userID)
	})
	if err != nil {
		t.Fatal("no user:", err)
	}

	// Create a box owned by this user.
	boxName := "appcookiebox"
	privateRoute := `{"port":80,"share":"private"}`
	err = s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"fake_ctrhost", boxName, "running", "test-image", "test-container-id", userID, privateRoute,
			[]byte("test-identity-key"), "test-authorized-keys", []byte("test-client-key"), 2222)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create box: %v", err)
	}

	box, err := withRxRes1(s, context.Background(), (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		t.Fatalf("failed to look up box: %v", err)
	}

	port := s.httpPort()
	cookieName := exeweb.ProxyAuthCookieName(port)

	// Test: app token in proxy cookie authenticates.
	t.Run("app_token_cookie_authenticates", func(t *testing.T) {
		req := createTestRequestForServer("GET", "/", s.env.BoxSub(boxName), s)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: appToken})

		result := s.getProxyAuth(req, box)
		if result == nil {
			t.Fatal("expected auth result from app token in cookie, got nil")
		}
		if result.UserID != userID {
			t.Fatalf("expected userID %q, got %q", userID, result.UserID)
		}
	})

	// Test: invalid app token in cookie is rejected.
	t.Run("invalid_app_token_cookie_rejected", func(t *testing.T) {
		req := createTestRequestForServer("GET", "/", s.env.BoxSub(boxName), s)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: exeweb.AppTokenPrefix + "invalid_token"})

		result := s.getProxyAuth(req, box)
		if result != nil {
			t.Fatalf("expected nil for invalid app token in cookie, got userID=%q", result.UserID)
		}
	})

	// Test: regular cookie value is not treated as app token.
	t.Run("regular_cookie_not_app_token", func(t *testing.T) {
		req := createTestRequestForServer("GET", "/", s.env.BoxSub(boxName), s)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "not-an-app-token"})

		result := s.getProxyAuth(req, box)
		if result != nil {
			t.Fatalf("expected nil for regular cookie, got userID=%q", result.UserID)
		}
	})
}

func TestAppTokenInCookieInfo(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "cookieinfo@example.com")

	var userID string
	err := s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "cookieinfo@example.com").Scan(&userID)
	})
	if err != nil {
		t.Fatal("no user:", err)
	}

	pd := &proxyData{s: s}

	// Valid app token returns cookie data.
	t.Run("valid_app_token", func(t *testing.T) {
		cd, exists, err := pd.CookieInfo(context.Background(), appToken, "exe.dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Fatal("expected exists=true for valid app token")
		}
		if cd.UserID != userID {
			t.Fatalf("expected userID %q, got %q", userID, cd.UserID)
		}
		if cd.Domain != "exe.dev" {
			t.Fatalf("expected domain exe.dev, got %q", cd.Domain)
		}
	})

	// Invalid app token returns exists=false.
	t.Run("invalid_app_token", func(t *testing.T) {
		_, exists, err := pd.CookieInfo(context.Background(), exeweb.AppTokenPrefix+"bogus", "exe.dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Fatal("expected exists=false for invalid app token")
		}
	})

	// Regular cookie value is unaffected.
	t.Run("regular_cookie_miss", func(t *testing.T) {
		_, exists, err := pd.CookieInfo(context.Background(), "not-a-token", "exe.dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Fatal("expected exists=false for unknown cookie")
		}
	})
}

func TestAppTokenInExeAuthCookie(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "exeauth@example.com")

	// Use the app token as the exe-auth cookie on the main site.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", s.httpURL()+"/user", nil)
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: appToken})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /user with app token in exe-auth cookie, got %d", resp.StatusCode)
	}
}
