package e1e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

// fakeOIDCServer implements a minimal OIDC identity provider for testing.
// It serves the authorization, token, and userinfo endpoints.
type fakeOIDCServer struct {
	server *httptest.Server
	email  string // email to return in claims
	sub    string // subject ID to return in claims
}

func newFakeOIDCServer(email, sub string) *fakeOIDCServer {
	f := &fakeOIDCServer{email: email, sub: sub}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("/authorize", f.handleAuthorize)
	mux.HandleFunc("/token", f.handleToken)
	mux.HandleFunc("/userinfo", f.handleUserinfo)
	f.server = httptest.NewServer(mux)
	return f
}

func (f *fakeOIDCServer) Close() {
	f.server.Close()
}

func (f *fakeOIDCServer) URL() string {
	return f.server.URL
}

func (f *fakeOIDCServer) AuthURL() string {
	return f.server.URL + "/authorize"
}

func (f *fakeOIDCServer) TokenURL() string {
	return f.server.URL + "/token"
}

func (f *fakeOIDCServer) UserinfoURL() string {
	return f.server.URL + "/userinfo"
}

// handleDiscovery serves the OIDC discovery document.
func (f *fakeOIDCServer) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"issuer":                 f.server.URL,
		"authorization_endpoint": f.server.URL + "/authorize",
		"token_endpoint":         f.server.URL + "/token",
		"userinfo_endpoint":      f.server.URL + "/userinfo",
	})
}

// handleAuthorize simulates the IdP login page.
// In a real IdP, the user would authenticate here.
// For testing, we immediately redirect back with an authorization code.
func (f *fakeOIDCServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	redirectURI := r.URL.Query().Get("redirect_uri")
	if state == "" || redirectURI == "" {
		http.Error(w, "missing state or redirect_uri", http.StatusBadRequest)
		return
	}
	callback := fmt.Sprintf("%s?code=fake-auth-code&state=%s", redirectURI, url.QueryEscape(state))
	http.Redirect(w, r, callback, http.StatusFound)
}

// handleToken exchanges an authorization code for a token.
func (f *fakeOIDCServer) handleToken(w http.ResponseWriter, r *http.Request) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := map[string]any{
		"iss":            f.server.URL,
		"sub":            f.sub,
		"email":          f.email,
		"email_verified": true,
		"aud":            "test-client-id",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
	}
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	idToken := header + "." + payload + ".fakesig"

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token": "fake-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

// handleUserinfo returns user info (fallback if id_token parsing fails).
func (f *fakeOIDCServer) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sub":            f.sub,
		"email":          f.email,
		"email_verified": true,
	})
}

// followOIDCRedirectChain follows the redirect chain from the OIDC auth URL
// through the fake IdP and back to the exed callback.
// Returns the final response (typically a redirect to / or the SSH success page).
func followOIDCRedirectChain(t *testing.T, authURL string) *http.Response {
	t.Helper()
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// Step 1: GET the auth URL → fake OIDC server redirects to exed callback.
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("GET auth URL failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 from OIDC server, got %d", resp.StatusCode)
	}
	callbackURL := resp.Header.Get("Location")
	if !strings.Contains(callbackURL, "/oauth/oidc/callback") {
		t.Fatalf("expected redirect to /oauth/oidc/callback, got %q", callbackURL)
	}
	t.Logf("callback URL: %s", callbackURL)

	// Step 2: GET the callback URL → exed exchanges code, completes auth.
	resp, err = client.Get(callbackURL)
	if err != nil {
		t.Fatalf("GET callback URL failed: %v", err)
	}
	resp.Body.Close()
	return resp
}

// getTeamInviteToken waits for the team invite email and extracts the team_invite token.
func getTeamInviteToken(t *testing.T, memberEmail string) string {
	t.Helper()
	msg, err := Env.servers.Email.WaitForEmail(memberEmail)
	if err != nil {
		t.Fatalf("waiting for team invite email: %v", err)
	}
	re := regexp.MustCompile(`team_invite=([a-zA-Z0-9_-]+)`)
	m := re.FindStringSubmatch(msg.Body)
	if len(m) < 2 {
		t.Fatalf("could not find team_invite token in email body: %s", msg.Body)
	}
	return m[1]
}

// TestTeamOIDCWebLogin tests that a new user invited to a team with OIDC auth
// can register via the web flow through the OIDC provider.
func TestTeamOIDCWebLogin(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register the team owner (uses normal email flow).
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-oidc-web.example")
	ownerPTY.Disconnect()

	// Create team.
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_oidcweb_e2e", "OIDCWebTeam", ownerEmail)

	memberEmail := "member@test-team-oidc-web.example"

	// Start fake OIDC server that will authenticate as the member.
	oidcServer := newFakeOIDCServer(memberEmail, "oidc-sub-webmember-1")
	t.Cleanup(oidcServer.Close)
	testinfra.AddCanonicalization(oidcServer.URL(), "OIDC_SERVER")

	// Configure team OIDC via the SSH `team auth set oidc` command.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile,
		"team", "auth", "set", "oidc",
		"--issuer-url="+oidcServer.URL(),
		"--client-id=test-client-id",
		"--client-secret=test-client-secret",
		"--display-name=TestOIDC",
	)
	if err != nil {
		t.Fatalf("team auth set oidc failed: %v\noutput: %s", err, out)
	}
	t.Logf("team auth set oidc output: %s", out)

	// Invite member (creates pending invite with auth_provider=oidc).
	addTeamMember(t, ownerKeyFile, memberEmail)

	// Wait for the invite email and extract the team_invite token.
	inviteToken := getTeamInviteToken(t, memberEmail)
	t.Logf("team invite token: %s", inviteToken)

	base := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())

	// Submit email to web login with the team invite token.
	// Exed should detect OIDC and redirect to the fake IdP.
	jar, _ := cookiejar.New(nil)
	client := noRedirectClient(jar)

	formData := url.Values{
		"email":       {memberEmail},
		"team_invite": {inviteToken},
	}
	resp, err := client.PostForm(base+"/auth", formData)
	if err != nil {
		t.Fatalf("POST /auth failed: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	for resp.StatusCode == http.StatusTemporaryRedirect {
		location, err := resp.Location()
		if err != nil {
			t.Fatalf("missing Location from auth redirect: %v", err)
		}
		resp, err = client.PostForm(location.String(), formData)
		if err != nil {
			t.Fatalf("failed to follow auth redirection to %q: %v", location, err)
		}
		resp.Body.Close()
	}

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect to OIDC, got %d, body: %s", resp.StatusCode, respBody)
	}
	oidcAuthURL := resp.Header.Get("Location")
	if !strings.Contains(oidcAuthURL, "/authorize") {
		t.Fatalf("expected redirect to OIDC /authorize, got %q", oidcAuthURL)
	}
	t.Logf("OIDC auth URL: %s", oidcAuthURL)

	// Follow OIDC auth URL → fake server redirects to exed callback.
	resp, err = client.Get(oidcAuthURL)
	if err != nil {
		t.Fatalf("GET OIDC auth URL failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 from OIDC, got %d", resp.StatusCode)
	}
	callbackURL := resp.Header.Get("Location")
	t.Logf("callback URL: %s", callbackURL)

	// Follow callback → exed processes OIDC callback, sets cookie, redirects.
	resp, err = client.Get(callbackURL)
	if err != nil {
		t.Fatalf("GET callback URL failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect after callback, got %d", resp.StatusCode)
	}

	// Verify the session cookie was set.
	u, _ := url.Parse(base)
	cookies := jar.Cookies(u)
	hasAuthCookie := false
	for _, c := range cookies {
		if c.Name == "exe-auth" {
			hasAuthCookie = true
			break
		}
	}
	if !hasAuthCookie {
		t.Fatal("no exe-auth cookie set after OIDC login")
	}

	// Verify the session is valid: the user should be redirected to the authenticated shell page.
	authClient := noRedirectClient(jar)
	resp, err = authClient.Get(base + "/shell")
	if err != nil {
		t.Fatalf("GET /shell failed: %v", err)
	}
	resp, err = followRedirects(t, authClient, resp)
	resp.Body.Close()
	// An authenticated user gets 200; unauthenticated gets a redirect to /auth.
	if resp.StatusCode != http.StatusOK {
		loc := resp.Header.Get("Location")
		t.Fatalf("GET /shell: expected 200 (authenticated), got %d (Location: %s)", resp.StatusCode, loc)
	}
}

// TestTeamOIDCSSHNewDevice tests that an existing OIDC user adding a new SSH key
// is prompted to verify via the OIDC provider (not email).
func TestTeamOIDCSSHNewDevice(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register the team owner (uses normal email flow).
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-oidc-ssh.example")
	ownerPTY.Disconnect()

	// Create team.
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_oidcssh_e2e", "OIDCSSHTeam", ownerEmail)

	memberEmail := "member@test-team-oidc-ssh.example"

	// Start fake OIDC server.
	oidcServer := newFakeOIDCServer(memberEmail, "oidc-sub-sshmember-1")
	t.Cleanup(oidcServer.Close)
	testinfra.AddCanonicalization(oidcServer.URL(), "OIDC_SERVER")

	// Configure team OIDC via the SSH `team auth set oidc` command.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile,
		"team", "auth", "set", "oidc",
		"--issuer-url="+oidcServer.URL(),
		"--client-id=test-client-id",
		"--client-secret=test-client-secret",
		"--display-name=TestOIDC",
	)
	if err != nil {
		t.Fatalf("team auth set oidc failed: %v\noutput: %s", err, out)
	}
	t.Logf("team auth set oidc output: %s", out)

	// Invite member.
	addTeamMember(t, ownerKeyFile, memberEmail)

	// Register the member via web OIDC flow first (so user exists with auth_provider=oidc).
	inviteToken := getTeamInviteToken(t, memberEmail)
	base := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())
	jar, _ := cookiejar.New(nil)
	client := noRedirectClient(jar)

	formData := url.Values{
		"email":       {memberEmail},
		"team_invite": {inviteToken},
	}
	resp, err := client.PostForm(base+"/auth", formData)
	if err != nil {
		t.Fatalf("POST /auth failed: %v", err)
	}
	resp.Body.Close()

	for resp.StatusCode == http.StatusTemporaryRedirect {
		location, err := resp.Location()
		if err != nil {
			t.Fatalf("missing Location from auth redirect: %v", err)
		}
		resp, err = client.PostForm(location.String(), formData)
		if err != nil {
			t.Fatalf("failed to follow auth redirection to %q: %v", location, err)
		}
		resp.Body.Close()
	}

	oidcAuthURL := resp.Header.Get("Location")

	resp, err = client.Get(oidcAuthURL)
	if err != nil {
		t.Fatalf("GET OIDC auth failed: %v", err)
	}
	resp.Body.Close()
	callbackURL := resp.Header.Get("Location")

	resp, err = client.Get(callbackURL)
	if err != nil {
		t.Fatalf("GET callback failed: %v", err)
	}
	resp.Body.Close()

	// Now SSH in with a NEW key — should trigger OIDC verification for the existing user.
	newKeyFile, newPublicKey := genSSHKey(t)
	pty := sshToExeDev(t, newKeyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(memberEmail)

	// Should show OIDC verification URL (not email verification).
	pty.Want("Verify with")
	ptyOut := pty.WantREMatch(`Waiting for verification`)

	// Strip ANSI escape codes and extract the URL.
	ansiRE := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	clean := ansiRE.ReplaceAllString(ptyOut, "")
	urlRE := regexp.MustCompile(`https?://\S+`)
	authURLStr := strings.TrimSpace(urlRE.FindString(clean))
	if authURLStr == "" {
		t.Fatalf("could not extract OIDC auth URL from output: %q", clean)
	}
	t.Logf("OIDC auth URL: %s", authURLStr)

	// Follow the OIDC redirect chain (fake IdP → exed callback).
	followOIDCRedirectChain(t, authURLStr)

	// SSH session should complete — user already exists, just adding a new device.
	pty.Want("Email verified")
	pty.Want("Registration complete")
	pty.Want("new ssh key")
	pty.WantPrompt()

	// Verify user identity with the new key.
	pty.SendLine("whoami")
	pty.Want(memberEmail)
	pty.Want(newPublicKey)
	pty.WantPrompt()
	pty.Disconnect()
}
