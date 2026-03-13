package execore

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exe.dev/sqlite"
)

var flagScreenshot = flag.Bool("scripts.screenshot", false, "take screenshots during screen flow tests")

var screenshotRunID = randText()[:6]

func randText() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func takeScreenshot(t testing.TB, name, html string) {
	t.Helper()
	takeScreenshotWithSize(t, name, html, 1200, 900)
}

func takeScreenshotWithSize(t testing.TB, name, html string, width, height int) {
	t.Helper()

	dir := filepath.Join(os.TempDir(), "screenshots", screenshotRunID, t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("screenshot: failed to create dir: %v", err)
		return
	}

	htmlPath := filepath.Join(dir, name+".html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Logf("screenshot: failed to write html: %v", err)
		return
	}

	pngPath := filepath.Join(dir, name+".png")
	cmd := exec.Command(
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"--headless",
		"--screenshot="+pngPath,
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--force-device-scale-factor=2",
		"file://"+htmlPath,
	)
	if err := cmd.Run(); err != nil {
		t.Logf("screenshot: chrome failed: %v", err)
		return
	}

	t.Logf("screenshot: file://%s", pngPath)
}

// TestScreenFlow tests the web UI by making HTTP requests and optionally taking screenshots.
// Run with -scripts.screenshot to capture screenshots of each page.
func TestScreenFlow(t *testing.T) {
	server := newTestServer(t)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirects automatically
		},
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", server.httpPort())

	// Helper to make GET requests and capture response
	get := func(path string) (int, string) {
		resp, err := client.Get(baseURL + path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Helper to make POST requests
	post := func(path string, data url.Values) (int, string, http.Header) {
		resp, err := client.PostForm(baseURL+path, data)
		if err != nil {
			t.Fatalf("POST %s failed: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}

	var screenshotCount int
	screenshot := func(name, body string) {
		if *flagScreenshot {
			screenshotCount++
			safeName := fmt.Sprintf("%02d_%s", screenshotCount, name)
			takeScreenshot(t, safeName, body)
		}
	}

	screenshotMobile := func(name, body string) {
		if *flagScreenshot {
			screenshotCount++
			safeName := fmt.Sprintf("%02d_%s_mobile", screenshotCount, name)
			takeScreenshotWithSize(t, safeName, body, 375, 667) // iPhone SE size
		}
	}

	// Test 0: Index page
	t.Run("index", func(t *testing.T) {
		status, body := get("/")
		if status != 200 {
			t.Errorf("GET /: expected 200, got %d", status)
		}
		if !strings.Contains(body, "ssh") {
			t.Errorf("GET /: expected 'ssh' in body")
		}
		screenshot("index", body)
		screenshotMobile("index", body)
	})

	// Test 1: Auth form page
	t.Run("auth_form", func(t *testing.T) {
		status, body := get("/auth")
		if status != 200 {
			t.Errorf("GET /auth: expected 200, got %d", status)
		}
		if !strings.Contains(body, "Login (or create an account)") {
			t.Errorf("GET /auth: expected 'Login (or create an account)' in body")
		}
		screenshot("auth_form", body)
	})

	// Test 2: Submit email for auth
	email := "screentest@example.com"
	t.Run("auth_submit", func(t *testing.T) {
		status, body, _ := post("/auth", url.Values{"email": {email}})
		if status != 200 {
			t.Errorf("POST /auth: expected 200, got %d", status)
		}
		if !strings.Contains(body, "Check Your Email") {
			t.Errorf("POST /auth: expected 'Check Your Email' in body")
		}
		screenshot("email_sent", body)
	})

	// Test 3: Create a verification token and test the verification form
	t.Run("email_verification_form", func(t *testing.T) {
		// Get the user ID
		user, err := server.GetUserByEmail(t.Context(), email)
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}

		// Create verification token
		token := "test-screen-token-" + randText()[:8]
		expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO email_verifications (token, email, user_id, expires_at, verification_code)
				VALUES (?, ?, ?, ?, ?)`,
				token, email, user.UserID, expires, "123456")
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create verification token: %v", err)
		}

		// GET verification form
		status, body := get("/verify-email?token=" + token)
		if status != 200 {
			t.Errorf("GET /verify-email: expected 200, got %d", status)
		}
		if !strings.Contains(body, "CONFIRM LOGIN") {
			t.Errorf("GET /verify-email: expected 'CONFIRM LOGIN' in body")
		}
		screenshot("email_verification_form", body)

		// POST to complete verification
		status, body, _ = post("/verify-email", url.Values{"token": {token}})
		if status != 200 {
			t.Errorf("POST /verify-email: expected 200, got %d", status)
		}
		if !strings.Contains(body, "Email Verified") {
			t.Errorf("POST /verify-email: expected 'Email Verified' in body")
		}
		screenshot("email_verified", body)
	})

	// Test 4: Test device verification page structure (without actual device)
	t.Run("device_verification_invalid", func(t *testing.T) {
		status, _ := get("/verify-device?token=invalid")
		if status != 404 {
			t.Errorf("GET /verify-device with invalid token: expected 404, got %d", status)
		}
	})

	// Test 5: Proxy logged out page
	t.Run("proxy_logged_out", func(t *testing.T) {
		// Use httptest to test the template directly
		rec := httptest.NewRecorder()

		data := struct {
			WebHost string
		}{
			WebHost: server.env.WebHost,
		}
		server.renderTemplate(context.Background(), rec, "proxy-logged-out.html", data)

		if rec.Code != 200 {
			t.Errorf("proxy-logged-out template: expected 200, got %d", rec.Code)
		}
		screenshot("proxy_logged_out", rec.Body.String())
	})

	// Test 6: Proxy unreachable page
	t.Run("proxy_unreachable", func(t *testing.T) {
		rec := httptest.NewRecorder()

		data := struct {
			WebHost         string
			BoxName         string
			BoxDest         func(string) string
			Port            int
			IsShelleyPort   bool
			ShowWelcomeStep bool
			SSHCommand      string
			TerminalURL     string
			TraceID         string
		}{
			WebHost:         server.env.WebHost,
			BoxName:         "testbox",
			BoxDest:         server.env.BoxDest,
			Port:            8080,
			IsShelleyPort:   false,
			ShowWelcomeStep: true,
			SSHCommand:      "ssh testbox@" + server.env.ReplHost,
			TerminalURL:     "https://testbox.term." + server.env.WebHost,
			TraceID:         "test-trace-id",
		}
		server.renderTemplate(context.Background(), rec, "proxy-unreachable.html", data)

		if rec.Code != 200 {
			t.Errorf("proxy-unreachable template: expected 200, got %d", rec.Code)
		}
		screenshot("proxy_unreachable", rec.Body.String())
	})

	// Test 7: Terminal access denied page
	t.Run("terminal_access_denied", func(t *testing.T) {
		rec := httptest.NewRecorder()

		data := struct {
			BoxName      string
			DashboardURL string
			TraceID      string
		}{
			BoxName:      "testbox",
			DashboardURL: "/",
			TraceID:      "test-trace-id",
		}
		server.renderTemplate(context.Background(), rec, "terminal-access-denied.html", data)

		if rec.Code != 200 {
			t.Errorf("terminal-access-denied template: expected 200, got %d", rec.Code)
		}
		screenshot("terminal_access_denied", rec.Body.String())
	})

	// Test 8: Login confirmation page
	t.Run("login_confirmation", func(t *testing.T) {
		rec := httptest.NewRecorder()

		data := struct {
			WebHost    string
			UserEmail  string
			SiteDomain string
			CancelURL  string
			ConfirmURL string
		}{
			WebHost:    server.env.WebHost,
			UserEmail:  email,
			SiteDomain: "example.com",
			CancelURL:  "/cancel",
			ConfirmURL: "/confirm",
		}
		server.renderTemplate(context.Background(), rec, "login-confirmation.html", data)

		if rec.Code != 200 {
			t.Errorf("login-confirmation template: expected 200, got %d", rec.Code)
		}
		screenshot("login_confirmation", rec.Body.String())
	})
}
