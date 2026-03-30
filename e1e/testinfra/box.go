package testinfra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const (
	// Banner is the banner printed by exed in test mode
	// the first time a user connects.
	Banner = "~~~ EXE.DEV ~~~"

	// ExeDevPrompt is the prompt used by exe.dev running on localhost.
	ExeDevPrompt = "\033[1;36mlocalhost\033[0m \033[37m▶\033[0m "

	// FakeEmailSuffix is the suffix used for test email addresses.
	FakeEmailSuffix = "@e1e.zzz"
)

// sshOpts is the basic set of SSH options for testing.
var sshOpts = [...]string{
	"-F", "/dev/null",
	"-o", "IdentityAgent=none",
	"-o", "StrictHostKeyChecking=no",
	"-o", "UserKnownHostsFile=/dev/null",
	"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
	"-o", "PreferredAuthentications=publickey",
	"-o", "PubkeyAuthentication=yes",
	"-o", "PasswordAuthentication=no",
	"-o", "KbdInteractiveAuthentication=no",
	"-o", "ChallengeResponseAuthentication=no",
	"-o", "IdentitiesOnly=yes",
	"-o", "ConnectTimeout=3", // 3 second connection timeout
	"-o", "ServerAliveInterval=5", // send keepalive every 5 seconds
	"-o", "ServerAliveCountMax=2", // disconnect after 2 failed keepalives (10s total)
}

// SSHOpts returns the basic set of SSH options to use for testing.
func SSHOpts() []string {
	return sshOpts[:]
}

// BaseSSHArgs returns the SSH arguments to connect to the
// test exed with the given user name and private key file.
func (se *ServerEnv) BaseSSHArgs(username, keyFile string) []string {
	at := ""
	if username != "" {
		at = "@"
	}
	return append(SSHOpts(),
		"-p", strconv.Itoa(se.SSHProxy.Port()),
		"-o", "IdentityFile="+keyFile,
		username+at+"localhost",
	)
}

// SSHToExeDev takes a PTY and a private key file,
// and starts an ssh process to the test exed using that key.
func (se *ServerEnv) SSHToExeDev(ctx context.Context, pty *PTY, keyFile string) (*exec.Cmd, error) {
	cmd, err := se.SSHWithUserName(ctx, pty, "", keyFile)
	if err != nil {
		return nil, err
	}
	pty.SetPrompt(ExeDevPrompt)
	return cmd, nil
}

// SSHWithUserName connects to the test exed on the given PTY
// with the given user name and private key file.
func (se *ServerEnv) SSHWithUserName(ctx context.Context, pty *PTY, username, keyFile string) (*exec.Cmd, error) {
	sshArgs := se.BaseSSHArgs(username, keyFile)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	sshCmd.Env = append(sshCmd.Environ(),
		"SSH_AUTH_SOCK=", // disable SSH agent.
	)
	return sshCmd, pty.AttachAndStart(sshCmd)
}

// RunExeDevSSHCommand runs an ssh command on the test exed
// using the given private key file.
// It returns the command output.
func (se *ServerEnv) RunExeDevSSHCommand(ctx context.Context, keyFile string, args ...string) ([]byte, error) {
	return se.RunExeDevSSHCommandWithStdin(ctx, keyFile, nil, args...)
}

// RunExeDevSSHCommandWithStdin runs an ssh command on the test exed
// using the given private key file and stdin data.
// It returns the command output.
func (se *ServerEnv) RunExeDevSSHCommandWithStdin(ctx context.Context, keyFile string, stdin []byte, args ...string) ([]byte, error) {
	sshArgs := se.BaseSSHArgs("", keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	sshCmd.Env = append(sshCmd.Environ(),
		"SSH_AUTH_SOCK=", // disable SSH agent.
	)
	if stdin != nil {
		sshCmd.Stdin = bytes.NewReader(stdin)
	}

	out, err1 := sshCmd.CombinedOutput()

	var err2 error
	if bytes.Contains(out, []byte{'\r'}) {
		err2 = fmt.Errorf("ssh output contains \\r, did REPL formatting sneak through? raw output:\n%q", out)
	}

	var err3 error
	if ansi.Strip(string(out)) != string(out) {
		err3 = fmt.Errorf("ssh output contains ANSI escape codes, did REPL formatting sneak through? raw output:\n%q", out)
	}

	if err2 == nil && err3 == nil {
		return out, err1
	}
	if err1 == nil && err3 == nil {
		return out, err2
	}
	if err1 == nil && err2 == nil {
		return out, err3
	}
	return out, errors.Join(err1, err2, err3)
}

// RunParseExeDevJSON runs an ssh command on the test exed
// with the given arguments and private key file.
// It expects JSON output, and unmarshals it into a value of type T.
func RunParseExeDevJSON[T any](ctx context.Context, se *ServerEnv, keyFile string, args ...string) (T, error) {
	out, err := se.RunExeDevSSHCommand(ctx, keyFile, args...)
	var result T
	if err != nil {
		return result, fmt.Errorf("failed to run command: %v\n%s", err, out)
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return result, fmt.Errorf("failed to parse command output as JSON: %v\n%s", err, out)
	}
	return result, nil
}

// BoxSSHCommand returns an ssh command to run on the given box,
// using the given private key file. This does not run the command.
func (se *ServerEnv) BoxSSHCommand(ctx context.Context, boxName, keyFile string, args ...string) *exec.Cmd {
	sshArgs := se.BaseSSHArgs(boxName, keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	sshCmd.Env = append(sshCmd.Environ(),
		"SSH_AUTH_SOCK=", // disable SSH agent.
	)
	return sshCmd
}

// WaitForBoxSSHServer waits until an ssh command to the given box succeeds.
func (se *ServerEnv) WaitForBoxSSHServer(ctx context.Context, boxName, keyFile string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	start := time.Now()
	var err error
	for {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		err = se.BoxSSHCommand(attemptCtx, boxName, keyFile, "true").Run()
		attemptCancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("box ssh did not come up after %v: %w (last ssh error: %v)", time.Since(start).Round(time.Millisecond), ctx.Err(), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// WaitForEmailAndVerify waits for an email message to an address,
// looks for a verification link in that email, and clicks it.
// It returns HTTP authorization cookies.
func (se *ServerEnv) WaitForEmailAndVerify(to string) ([]*http.Cookie, error) {
	msg, err := se.Email.WaitForEmail(to)
	if err != nil {
		return nil, err
	}
	return se.ClickVerifyLinkInEmail(msg)
}

// hiddenRe matches hidden input fields in HTML forms.
// It handles inputs where name appears before value.
var hiddenRe = regexp.MustCompile(`<input[^>]+name="([^"]+)"[^>]+value="([^"]*)"[^>]*>`)

// hiddenReReverse matches hidden input fields where value appears before name.
var hiddenReReverse = regexp.MustCompile(`<input[^>]+value="([^"]*)"[^>]+name="([^"]+)"`)

// actionRe finds the form action attribute.
var actionRe = regexp.MustCompile(`<form[^>]+action="([^"]+)"`)

// ExtractFormFields extracts all input fields from HTML into url.Values.
// It handles both name-before-value and value-before-name orderings.
// For Vue pages that use window.__PAGE__ JSON, it also extracts fields from the JSON data.
func ExtractFormFields(htmlBody []byte) url.Values {
	formData := url.Values{}
	for _, match := range hiddenRe.FindAllSubmatch(htmlBody, -1) {
		name := string(match[1])
		value := html.UnescapeString(string(match[2]))
		formData.Set(name, value)
	}
	// Also try the reverse order (value before name)
	for _, match := range hiddenReReverse.FindAllSubmatch(htmlBody, -1) {
		name := string(match[2])
		if formData.Get(name) == "" {
			value := html.UnescapeString(string(match[1]))
			formData.Set(name, value)
		}
	}
	// For Vue pages: extract fields from window.__PAGE__ JSON data.
	// JSON field names match form parameter names, so no mapping needed.
	if len(formData) == 0 {
		if pageData := ExtractPageJSON(htmlBody); pageData != nil {
			for k, v := range pageData {
				switch val := v.(type) {
				case string:
					if val != "" {
						formData.Set(k, val)
					}
				case bool:
					if val {
						formData.Set(k, "1")
					}
				}
			}
		}
	}
	return formData
}

// pageJSONRe matches window.__PAGE__={...} in a <script> tag.
var pageJSONRe = regexp.MustCompile(`window\.__PAGE__=({[^<]+})`)

// ExtractPageJSON extracts the JSON data from a Vue page's window.__PAGE__ script tag.
func ExtractPageJSON(htmlBody []byte) map[string]any {
	match := pageJSONRe.FindSubmatch(htmlBody)
	if len(match) < 2 {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(match[1], &data); err != nil {
		return nil
	}
	return data
}

// ExtractFormAction extracts the form action from HTML.
// Returns defaultPath if no action is found.
// For Vue pages, reads the formAction field from window.__PAGE__ JSON data.
func ExtractFormAction(htmlBody []byte, defaultPath string) string {
	actionMatch := actionRe.FindSubmatch(htmlBody)
	if len(actionMatch) >= 2 {
		return string(actionMatch[1])
	}
	if pageData := ExtractPageJSON(htmlBody); pageData != nil {
		if action, ok := pageData["formAction"].(string); ok && action != "" {
			return action
		}
	}
	return defaultPath
}

// ClickVerifyLinkInEmail looks for a verification link in an email message,
// and clicks it. It returns HTTP authorization cookies.
func (se *ServerEnv) ClickVerifyLinkInEmail(emailMsg *EmailMessage) ([]*http.Cookie, error) {
	verifyURL, err := ExtractVerificationToken(emailMsg.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to extract verification URL: %v", err)
	}

	parsedVerifyURL, err := url.Parse(verifyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verification URL %q: %v", verifyURL, err)
	}

	// Step 1: GET the verification page (shows confirmation form)
	getResp, err := http.Get(verifyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to access verification page: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verification page request failed with status: %d", getResp.StatusCode)
	}

	htmlBody, err := io.ReadAll(getResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read verification page body: %v", err)
	}
	getResp.Body.Close()

	// // Extract the pairing code from the verification page
	// codeRe := regexp.MustCompile(`tracking-widest[^>]*>([0-9]{6})<`)
	// if codeMatches := codeRe.FindStringSubmatch(bodyStr); len(codeMatches) >= 2 {
	// 	testinfra.AddCanonicalization(codeMatches[1], "EMAIL_VERIFICATION_CODE")
	// }

	// Extract hidden inputs so we can POST the same form fields back
	formData := ExtractFormFields(htmlBody)

	token := formData.Get("token")
	if token == "" {
		return nil, fmt.Errorf("failed to extract token from HTML form: %s", htmlBody)
	}
	AddCanonicalization(token, "EMAIL_VERIFICATION_TOKEN")

	// Determine form action (defaults to /verify-email if not found)
	actionPath := ExtractFormAction(htmlBody, "/verify-email")
	if !strings.HasPrefix(actionPath, "/") {
		actionPath = "/" + actionPath
	}

	// Create HTTP client with cookie jar to capture authentication cookies
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postURL := fmt.Sprintf("http://localhost:%d%s", se.Exed.HTTPPort, actionPath)
	postResp, err := client.PostForm(postURL, formData)
	if err != nil {
		return nil, fmt.Errorf("failed to submit verification form: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(postResp.Body)
		err = fmt.Errorf("verification form submission returned status: %d, body: %s", postResp.StatusCode, body)
	}
	postResp.Body.Close()

	// Extract cookies from the response
	cookies := jar.Cookies(parsedVerifyURL)
	if len(cookies) == 0 {
		parsedPostURL, _ := url.Parse(postURL)
		cookies = jar.Cookies(parsedPostURL)
	}

	return cookies, err
}

// WebLoginWithEmail performs a web-only login flow (no SSH involved).
// This uses the /auth POST endpoint to trigger email verification.
// Unlike RegisterForExeDevWithEmail, this doesn't create a user via SSH,
// so it exercises the web-only user creation path.
func (se *ServerEnv) WebLoginWithEmail(email string) ([]*http.Cookie, error) {
	// POST to /auth with email to trigger the web login flow
	authURL := fmt.Sprintf("http://localhost:%d/auth", se.Exed.HTTPPort)
	resp, err := http.PostForm(authURL, url.Values{"email": {email}})
	if err != nil {
		return nil, fmt.Errorf("failed to POST to /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /auth failed with status %d: %s", resp.StatusCode, body)
	}

	// Wait for verification email and click verification link
	// (same as SSH flow).
	return se.WaitForEmailAndVerify(email)
}

// WebLoginWithInvite performs a web-only login flow with an invite code.
// This uses the /auth POST endpoint with invite=<code> to apply the invite code.
func (se *ServerEnv) WebLoginWithInvite(email, inviteCode string) ([]*http.Cookie, error) {
	// POST to /auth with email and invite code
	authURL := fmt.Sprintf("http://localhost:%d/auth", se.Exed.HTTPPort)
	resp, err := http.PostForm(authURL, url.Values{
		"email":  {email},
		"invite": {inviteCode},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to POST to /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /auth failed with status %d: %s", resp.StatusCode, body)
	}

	// Wait for verification email and click verification link
	return se.WaitForEmailAndVerify(email)
}

// WebLoginWithExe performs a login flow with login_with_exe=1 set.
// This simulates a user logging in via the proxy auth flow (login-with-exe).
// Users created this way are "basic users" and should only see the profile tab.
func (se *ServerEnv) WebLoginWithExe(email string) ([]*http.Cookie, error) {
	// POST to /auth with email AND login_with_exe=1 to trigger login-with-exe flow
	authURL := fmt.Sprintf("http://localhost:%d/auth", se.Exed.HTTPPort)
	resp, err := http.PostForm(authURL, url.Values{
		"email":          {email},
		"login_with_exe": {"1"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to POST to /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /auth failed with status %d: %s", resp.StatusCode, body)
	}

	// Wait for verification email and verify
	// (same as SSH flow).
	return se.WaitForEmailAndVerify(email)
}

// AddBillingForUser adds a billing account for a user by their user_id.
// This simulates a user completing the Stripe billing flow.
func (se *ServerEnv) AddBillingForUser(userID string) error {
	addBillingURL := fmt.Sprintf("http://localhost:%d/debug/users/add-billing", se.Exed.HTTPPort)
	resp, err := http.PostForm(addBillingURL, url.Values{"user_id": {userID}})
	if err != nil {
		return fmt.Errorf("failed to POST to /debug/users/add-billing: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /debug/users/add-billing failed with status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// AddBillingForEmail adds a billing account for a user by their email address.
// This looks up the user_id from the email and then adds billing.
func (se *ServerEnv) AddBillingForEmail(email string) error {
	// Get user_id from debug endpoint
	usersURL := fmt.Sprintf("http://localhost:%d/debug/users?format=json", se.Exed.HTTPPort)
	resp, err := http.Get(usersURL)
	if err != nil {
		return fmt.Errorf("failed to get users: %v", err)
	}
	defer resp.Body.Close()

	var users []struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return fmt.Errorf("failed to parse users: %v", err)
	}

	var userID string
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			userID = u.UserID
			break
		}
	}
	if userID == "" {
		return fmt.Errorf("user %s not found", email)
	}

	return se.AddBillingForUser(userID)
}

// RegisterForExeDevWithEmail registers with exed over an
// ssh connection on pty.
// Dir is where to put the generated SSH key.
// It returns the HTTP authorization cookies,
// the generated SSH private key file,
// and the ssh command.
func (se *ServerEnv) RegisterForExeDevWithEmail(ctx context.Context, pty *PTY, email, dir string) ([]*http.Cookie, string, *exec.Cmd, error) {
	keyFile, publicKey, err := GenSSHKey(dir)
	if err != nil {
		return nil, "", nil, err
	}
	cmd, err := se.SSHToExeDev(ctx, pty, keyFile)
	if err != nil {
		return nil, "", nil, err
	}
	if err := pty.Want(Banner); err != nil {
		return nil, "", nil, err
	}

	if err := pty.Want("Please enter your email"); err != nil {
		return nil, "", nil, err
	}
	if err := pty.SendLine(email); err != nil {
		return nil, "", nil, err
	}

	if err := pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email)); err != nil {
		return nil, "", nil, err
	}

	// pty.WantRE("Pairing code: .*[0-9]{6}.*")

	cookies, err := se.WaitForEmailAndVerify(email)
	if err != nil {
		return nil, "", nil, err
	}

	if err := pty.Want("Email verified successfully"); err != nil {
		return nil, "", nil, err
	}
	if err := pty.Want("Registration complete"); err != nil {
		return nil, "", nil, err
	}

	// check that we show welcome message for users who
	// haven't created boxes
	if err := pty.Want("Welcome to EXE.DEV!"); err != nil {
		return nil, "", nil, err
	}
	if err := pty.WantPrompt(); err != nil {
		return nil, "", nil, err
	}

	if err := pty.SendLine("whoami"); err != nil {
		return nil, "", nil, err
	}
	if err := pty.Want(email); err != nil {
		return nil, "", nil, err
	}
	if err := pty.Want(publicKey); err != nil {
		return nil, "", nil, err
	}
	if err := pty.WantPrompt(); err != nil {
		return nil, "", nil, err
	}

	return cookies, keyFile, cmd, nil
}

// boxCounter is used to get unique names for boxes.
var boxCounter atomic.Int32

// nonAlphaNumDash is used by boxName.
var nonAlphaNumDash = regexp.MustCompile(`[^a-z0-9-]+`)

// dashes is used by boxName.
var dashes = regexp.MustCompile(`-+`)

// BoxName returns a unique test-specific box name.
// The returned name has an e1e prefix for easy cleanup.
func BoxName(testName, testRunID string) string {
	// Create a unique test-specific box name:
	// "e1e-{runid}-{counter}-{testname}"
	// runid provides cross-process uniqueness.
	// counter covers within-process uniqueness.
	// e1e prefix and testname are for debuggability.
	counter := fmt.Sprintf("%04x", boxCounter.Add(1))
	testName = strings.ToLower(strings.ReplaceAll(testName, "/", "-"))
	// Sanitize to allowed charset [a-z0-9-] to satisfy isValidBoxName
	testName = nonAlphaNumDash.ReplaceAllString(testName, "-")
	// Collapse multiple hyphens and trim
	testName = dashes.ReplaceAllString(testName, "-")
	testName = strings.Trim(testName, "-")
	boxName := fmt.Sprintf("e1e-%s-%s-%s", testRunID, counter, testName)
	AddCanonicalization(boxName, "BOX_NAME")
	return boxName
}

// BoxOpts holds optional parameters for newBox.
type BoxOpts struct {
	Image   string
	Command string
	NoEmail bool
}

// NewBox requests a new box from the open repl pty.
// This returns the new box name.
func (se *ServerEnv) NewBox(testName, testRunID string, pty *PTY, opts ...BoxOpts) (string, error) {
	boxName := BoxName(testName, testRunID)
	boxNameRe := regexp.QuoteMeta(boxName)

	// Use first opts if provided, otherwise default
	var opt BoxOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Build the command line
	cmdLine := "new --name=" + boxName
	if opt.Image != "" {
		cmdLine += " --image=" + strconv.Quote(opt.Image)
	}
	if opt.Command != "" {
		cmdLine += " --command=" + strconv.Quote(opt.Command)
	}
	if opt.NoEmail {
		cmdLine += " --no-email"
	}

	if err := pty.SendLine(cmdLine); err != nil {
		return "", err
	}
	pty.Reject("Sorry")
	if err := pty.WantRE("Creating .*" + boxNameRe); err != nil {
		return "", err
	}
	// Calls to action
	if err := pty.Want("App"); err != nil {
		return "", err
	}
	if err := pty.Want("http://"); err != nil {
		return "", err
	}
	if err := pty.Want("SSH"); err != nil {
		return "", err
	}
	if err := pty.Wantf("ssh -p %v %v@exe.cloud", se.SSHProxy.Port(), boxName); err != nil {
		return "", err
	}

	// Confirm it is there.
	if err := pty.SendLine("ls"); err != nil {
		return "", err
	}
	if err := pty.Want("VMs"); err != nil {
		return "", err
	}
	if err := pty.WantRE(boxNameRe + ".*running.*\n"); err != nil {
		return "", err
	}

	return boxName, nil
}

// CreateInviteCode creates a new invite code via the debug API.
// planType must be "trial" or "free".
// Returns the generated invite code.
func (se *ServerEnv) CreateInviteCode(planType string) (string, error) {
	createURL := fmt.Sprintf("http://localhost:%d/debug/invite", se.Exed.HTTPPort)
	req, err := http.NewRequest("POST", createURL, strings.NewReader(url.Values{
		"action":    {"create"},
		"plan_type": {planType},
	}.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to POST to /debug/invite: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("POST /debug/invite failed with status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	return result.Code, nil
}

// GiveInvitesToUser assigns invite codes to a user via the debug API.
// The user must already exist.
func (se *ServerEnv) GiveInvitesToUser(email string, count int, planType string) error {
	giveURL := fmt.Sprintf("http://localhost:%d/debug/invite", se.Exed.HTTPPort)
	resp, err := http.PostForm(giveURL, url.Values{
		"action":    {"give_to_user"},
		"email":     {email},
		"count":     {fmt.Sprintf("%d", count)},
		"plan_type": {planType},
	})
	if err != nil {
		return fmt.Errorf("failed to POST to /debug/invite: %v", err)
	}
	defer resp.Body.Close()

	// The endpoint redirects on success, so we might get 200 or 303
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /debug/invite failed with status %d: %s", resp.StatusCode, body)
	}

	return nil
}

// DebugAddTeamMember adds an existing user to a team via the debug API.
// This bypasses the SSH `team add` restriction that prevents adding existing users.
func (se *ServerEnv) DebugAddTeamMember(teamID, email, role string) error {
	addURL := fmt.Sprintf("http://localhost:%d/debug/teams/add-member", se.Exed.HTTPPort)
	resp, err := http.PostForm(addURL, url.Values{
		"team_id":          {teamID},
		"email":            {email},
		"role":             {role},
		"confirm_existing": {"true"},
	})
	if err != nil {
		return fmt.Errorf("failed to POST to /debug/teams/add-member: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /debug/teams/add-member failed with status %d: %s", resp.StatusCode, body)
	}

	return nil
}
