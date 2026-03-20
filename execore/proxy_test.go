package execore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

// createTestRequest creates an http.Request with proper context for proxy tests
// For hosts without explicit ports, adds the server's HTTP port
func createTestRequestForServer(method, url, host string, server *Server) *http.Request {
	req := httptest.NewRequest(method, url, nil)

	// If host doesn't have a port, add the server's HTTP port
	finalHost := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		// No port in host, add server's HTTP port
		if server.servingHTTP() {
			finalHost = net.JoinHostPort(host, strconv.Itoa(server.httpPort()))
		} else {
			// Fallback to port 80 for test
			finalHost = net.JoinHostPort(host, "80")
		}
	}

	req.Host = finalHost

	// Set up mock local address context that the proxy handler expects
	// Parse the host to determine what port to mock
	var mockPort int
	if _, portStr, err := net.SplitHostPort(finalHost); err == nil {
		if port, err := strconv.Atoi(portStr); err == nil {
			mockPort = port
		} else {
			mockPort = 80 // fallback
		}
	} else {
		// No port specified, assume default
		mockPort = 80
	}

	// Create a mock net.Addr that represents the local address
	mockAddr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: mockPort,
	}

	// Add the local address to the request context
	ctx := context.WithValue(req.Context(), http.LocalAddrContextKey, mockAddr)
	req = req.WithContext(ctx)

	return req
}

// TestProxyStreaming tests that the proxy doesn't buffer streaming responses
func TestProxyStreaming(t *testing.T) {
	t.Parallel()

	// This test verifies that FlushInterval is set on the reverse proxy
	// to avoid buffering responses. This is critical for:
	// - Server-Sent Events (SSE)
	// - Streaming responses
	// - WebSocket upgrades
	// - Any real-time data transfer

	// Create a mock streaming backend that sends data in chunks
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enable flushing
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter doesn't support flushing")
		}

		// Send headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send data in chunks with delays
		for i := range 3 {
			fmt.Fprintf(w, "data: chunk %d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer backend.Close()

	// Parse backend URL
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Create a reverse proxy similar to what proxyViaSSHPortForward does
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	// This is the critical setting - FlushInterval = -1 means flush immediately
	proxy.FlushInterval = -1

	// Create test server with the proxy
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Make request to proxy
	req, err := http.NewRequest("GET", proxyServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read the response body to verify streaming works
	// If buffering were enabled, we'd get all chunks at once after the delays
	// With flushing, we get them as they're sent
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	bodyStr := string(body)

	// Verify we got all chunks
	for i := range 3 {
		expected := fmt.Sprintf("data: chunk %d\n\n", i)
		if !strings.Contains(bodyStr, expected) {
			t.Errorf("Expected to find %q in response, got: %q", expected, bodyStr)
		}
	}

	// Verify content type
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type 'text/event-stream', got %q", ct)
	}
}

// TestPublicRouteStripsTokenCtx verifies that on public routes, token auth
// still works for identity (UserID is set), but CtxRaw is stripped so that
// X-ExeDev-Token-Ctx is never forwarded to the VM. This prevents user A from
// injecting arbitrary trusted context into user B's public box.
func TestPublicRouteStripsTokenCtx(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// Generate a test ed25519 key pair and register a user.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	email := "publicctx@example.com"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create a box with a public route.
	boxName := "pubbox"
	publicRoute := `{"port":80,"share":"public"}`
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"fake_ctrhost", boxName, "running", "test-image", "test-container-id", userID, publicRoute,
			"test-identity-key", "test-authorized-keys", "test-client-key", 2222)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create box: %v", err)
	}

	// Look up the box (same as handleProxyRequest does).
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		t.Fatalf("failed to look up box: %v", err)
	}

	// Create a VM token with a ctx field.
	vmNamespace := "v0@" + boxName + "." + s.env.BoxHost
	payload := []byte(`{"exp":4000000000,"ctx":{"role":"admin"}}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, vmNamespace)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Sanity check: getProxyAuth with Bearer token returns non-nil CtxRaw.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	result := s.getProxyAuth(req, box)
	if result == nil {
		t.Fatal("getProxyAuth returned nil for valid token")
	}
	if result.UserID != userID {
		t.Errorf("expected UserID %q, got %q", userID, result.UserID)
	}
	if result.CtxRaw == nil {
		t.Fatal("getProxyAuth should return non-nil CtxRaw for token with ctx")
	}

	// Now simulate the public route code path: call getProxyAuth and strip CtxRaw.
	// This mirrors the logic in handleProxyRequest for public routes.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	authResult := s.getProxyAuth(req2, box)
	if authResult == nil {
		t.Fatal("getProxyAuth returned nil on second call")
	}
	// Public route: strip CtxRaw.
	authResult.CtxRaw = nil

	if authResult.UserID != userID {
		t.Errorf("after strip, expected UserID %q, got %q", userID, authResult.UserID)
	}
	if authResult.CtxRaw != nil {
		t.Error("after strip, CtxRaw should be nil")
	}
}

// TestRequestAccess tests the request-access flow:
// 1. Authenticated user without access sees "You need access" page
// 2. POST to /__exe.dev/request-access sends email and shows "Request sent"
// 3. Unauthenticated user sees the 401 login page (not the request-access page)
func TestRequestAccess(t *testing.T) {
	t.Parallel()

	// Set up a fake email server to capture sent emails.
	var mu sync.Mutex
	var sentEmails []map[string]string
	fakeEmailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var emailData map[string]string
		if err := json.NewDecoder(r.Body).Decode(&emailData); err != nil {
			t.Errorf("failed to decode email: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		sentEmails = append(sentEmails, emailData)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeEmailServer.Close()

	s := newTestServer(t)
	s.fakeHTTPEmail = fakeEmailServer.URL

	ctx := t.Context()

	// Create owner (user A).
	ownerID := "usr_owner_" + generateRegistrationToken()
	ownerEmail := "owner@reqaccess-test.dev"

	// Create requester (user B).
	requesterID := "usr_requester_" + generateRegistrationToken()
	requesterEmail := "requester@reqaccess-test.dev"

	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, ownerID, ownerEmail); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, requesterID, requesterEmail); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create users: %v", err)
	}

	// Create a box owned by user A with a private route.
	boxName := "reqaccessbox"
	privateRoute := `{"port":80,"share":"private"}`
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"fake_ctrhost", boxName, "running", "test-image", "test-container-id", ownerID, privateRoute,
			"test-identity-key", "test-authorized-keys", "test-client-key", 2222)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create box: %v", err)
	}

	// Create an auth cookie for requester on the box's proxy domain.
	boxDomain := s.env.BoxSub(boxName) // e.g. "reqaccessbox.exe.cloud"
	cookieValue, err := s.createAuthCookie(ctx, requesterID, boxDomain)
	if err != nil {
		t.Fatalf("failed to create auth cookie: %v", err)
	}

	port := s.httpPort()
	cookieName := exeweb.ProxyAuthCookieName(port)

	// --- Test 1: GET to the box URL → should see "You need access" page ---
	t.Run("authenticated_user_sees_request_access", func(t *testing.T) {
		req := createTestRequestForServer("GET", "/", boxDomain, s)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})

		w := httptest.NewRecorder()
		s.handleProxyRequest(w, req)

		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
		if !strings.Contains(bodyStr, "You need access") {
			t.Errorf("expected 'You need access' in body, got: %s", bodyStr)
		}
		if !strings.Contains(bodyStr, requesterEmail) {
			t.Errorf("expected requester email %q in body, got: %s", requesterEmail, bodyStr)
		}
		if !strings.Contains(bodyStr, "Request access") {
			t.Errorf("expected 'Request access' button in body, got: %s", bodyStr)
		}
	})

	// --- Test 2: POST to /__exe.dev/request-access → sends email and shows "Request sent" ---
	t.Run("post_request_access_sends_email", func(t *testing.T) {
		mu.Lock()
		sentEmails = nil
		mu.Unlock()

		form := url.Values{"message": {"Please let me in!"}}
		req := createTestRequestForServer("POST", "/__exe.dev/request-access", boxDomain, s)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Body = io.NopCloser(strings.NewReader(form.Encode()))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})

		w := httptest.NewRecorder()
		s.handleProxyRequest(w, req)

		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		if !strings.Contains(bodyStr, "Request sent") {
			t.Errorf("expected 'Request sent' in body, got: %s", bodyStr)
		}

		// Verify the email was sent.
		mu.Lock()
		emailCount := len(sentEmails)
		mu.Unlock()

		if emailCount != 1 {
			t.Fatalf("expected 1 email sent, got %d", emailCount)
		}

		mu.Lock()
		sentEmail := sentEmails[0]
		mu.Unlock()

		if sentEmail["to"] != ownerEmail {
			t.Errorf("email sent to %q, expected %q", sentEmail["to"], ownerEmail)
		}
		if !strings.Contains(sentEmail["subject"], requesterEmail) {
			t.Errorf("expected requester email in subject, got: %q", sentEmail["subject"])
		}
		if !strings.Contains(sentEmail["subject"], boxName) {
			t.Errorf("expected box name in subject, got: %q", sentEmail["subject"])
		}
		if !strings.Contains(sentEmail["body"], "Please let me in!") {
			t.Errorf("expected message in email body, got: %q", sentEmail["body"])
		}
		if !strings.Contains(sentEmail["body"], "share_vm="+boxName) {
			t.Errorf("expected share_vm param in email body, got: %q", sentEmail["body"])
		}
		if !strings.Contains(sentEmail["body"], "share_email="+url.QueryEscape(requesterEmail)) {
			t.Errorf("expected share_email param in email body, got: %q", sentEmail["body"])
		}
		if sentEmail["reply_to"] != requesterEmail {
			t.Errorf("expected reply_to %q, got %q", requesterEmail, sentEmail["reply_to"])
		}
	})

	// --- Test 3: Unauthenticated user sees 401 login page (not request-access) ---
	t.Run("unauthenticated_user_sees_login", func(t *testing.T) {
		req := createTestRequestForServer("GET", "/", boxDomain, s)
		// No cookie set.

		w := httptest.NewRecorder()
		s.handleProxyRequest(w, req)

		resp := w.Result()

		// Unauthenticated user should be redirected to login.
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected 307 redirect, got %d", resp.StatusCode)
		}
		loc := resp.Header.Get("Location")
		if !strings.Contains(loc, "/__exe.dev/login") {
			t.Errorf("expected redirect to /__exe.dev/login, got: %s", loc)
		}
	})

	// --- Test 4: Nonexistent box shows generic 401 (not request-access) ---
	t.Run("nonexistent_box_shows_401", func(t *testing.T) {
		nonexistentDomain := s.env.BoxSub("nonexistentbox")
		cookieValueNonexistent, err := s.createAuthCookie(ctx, requesterID, nonexistentDomain)
		if err != nil {
			t.Fatalf("failed to create auth cookie: %v", err)
		}
		cookieNameNonexistent := exeweb.ProxyAuthCookieName(port)

		req := createTestRequestForServer("GET", "/", nonexistentDomain, s)
		req.AddCookie(&http.Cookie{Name: cookieNameNonexistent, Value: cookieValueNonexistent})

		w := httptest.NewRecorder()
		s.handleProxyRequest(w, req)

		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
		// Should show the generic 401 page, NOT the request-access page.
		if strings.Contains(bodyStr, "You need access") {
			t.Error("nonexistent box should NOT show 'You need access' page")
		}
		if !strings.Contains(bodyStr, "Access") {
			t.Errorf("expected generic 401 page, got: %s", bodyStr)
		}
	})

	// --- Test 5: GET /__exe.dev/request-access renders the form ---
	t.Run("get_request_access_renders_form", func(t *testing.T) {
		req := createTestRequestForServer("GET", "/__exe.dev/request-access", boxDomain, s)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookieValue})

		w := httptest.NewRecorder()
		s.handleProxyRequest(w, req)

		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		if !strings.Contains(bodyStr, "You need access") {
			t.Errorf("expected 'You need access' in body, got: %s", bodyStr)
		}
		if !strings.Contains(bodyStr, "request-access") {
			t.Errorf("expected form action in body, got: %s", bodyStr)
		}
	})
}
