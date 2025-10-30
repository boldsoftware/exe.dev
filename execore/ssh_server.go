package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"exe.dev/boxname"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sqlite"
	"exe.dev/sshsession"
	"exe.dev/termfun"
	"github.com/anmitsu/go-shlex"
	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// minimalConnMetadata implements ssh.ConnMetadata with just the fields we need
type minimalConnMetadata struct {
	user       string
	remoteAddr net.Addr
}

func (m *minimalConnMetadata) User() string          { return m.user }
func (m *minimalConnMetadata) SessionID() []byte     { return nil }
func (m *minimalConnMetadata) ClientVersion() []byte { return nil }
func (m *minimalConnMetadata) ServerVersion() []byte { return nil }
func (m *minimalConnMetadata) RemoteAddr() net.Addr  { return m.remoteAddr }
func (m *minimalConnMetadata) LocalAddr() net.Addr   { return nil }

// SSHServer wraps the gliderlabs SSH server implementation
type SSHServer struct {
	server   *Server
	srv      *ssh.Server
	commands *exemenu.CommandTree
}

// NewSSHServer creates a new SSH server using gliderlabs/ssh
func NewSSHServer(s *Server) *SSHServer {
	ss := &SSHServer{
		server: s,
	}
	// TODO: untangle this circular reference btw CommandTree and SSHServer.
	ss.commands = NewCommandTree(ss)
	return ss
}

// Start initializes and starts the SSH server
func (ss *SSHServer) Start(ln net.Listener) error {
	// Initialize the gliderlabs SSH server
	ss.srv = &ssh.Server{
		Addr:             ln.Addr().String(),
		Handler:          ss.handleSession,
		PublicKeyHandler: ss.authenticatePublicKey,
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"session": sshSessionHandlerWithJobEasterEgg,
		},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": func(s ssh.Session) {
				fmt.Fprintf(s.Stderr(), "scp/sftp is not supported on the exe.dev server.\r\n")
				s.Close()
			},
		},
		RequestHandlers: map[string]ssh.RequestHandler{},
	}

	// Transfer the host key from the main server to the gliderlabs SSH server
	// The main server should have already loaded/generated host keys via setupSSHServer
	if ss.server.sshHostKey != nil {
		// Use the stored host key from the main server configuration
		ss.srv.AddHostKey(ss.server.sshHostKey)
		ss.server.slog().Info("added host key from main server configuration")
	} else {
		ss.server.slog().Warn("no host key found in main server configuration")
	}

	return ss.srv.Serve(ln)
}

// sshSessionHandlerWithJobEasterEgg sends a single GLOBAL_REQUEST per connection (visible at -vv/-vvv),
// then delegates to the default session handler.
func sshSessionHandlerWithJobEasterEgg(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
	const jobNoteOnceValue = "session-job-easter-egg"
	if ctx.Value(jobNoteOnceValue) == nil {
		ctx.SetValue(jobNoteOnceValue, true)
		name := "Hello, fellow -vv spelunker! Come work with us: david+sshvv@bold.dev"
		_, _, _ = conn.SendRequest(name, false, nil)
	}
	ssh.DefaultSessionHandler(srv, conn, newChan, ctx)
}

// Stop gracefully stops the SSH server
func (ss *SSHServer) Stop() error {
	if ss.srv != nil {
		return ss.srv.Close()
	}
	return nil
}

// shouldShowSpinner determines if we should show spinner/progress indicators
// Based on TTY detection, environment variables, and terminal capabilities
func (ss *SSHServer) shouldShowSpinner(s exemenu.ShellSession) bool {
	// If we are not in an SSH session (e.g., invoked from HTTP), don't show spinner
	if s == nil {
		return false
	}
	// Check environment variables first
	env := s.Environ()
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Check NO_COLOR - if set (any value), disable colors/spinners
	if _, hasNoColor := envMap["NO_COLOR"]; hasNoColor {
		return false
	}

	// Check TERM for dumb terminal
	if term, hasTerm := envMap["TERM"]; hasTerm && term == "dumb" {
		return false
	}

	// Check CI environment variable (implies non-human)
	if _, hasCI := envMap["CI"]; hasCI {
		return false
	}

	// Check NONINTERACTIVE
	if _, hasNonInteractive := envMap["NONINTERACTIVE"]; hasNonInteractive {
		return false
	}

	// Check FORCE_COLOR - if set, override and show spinner
	if _, hasForceColor := envMap["FORCE_COLOR"]; hasForceColor {
		return true
	}

	// Check if we have a PTY allocated
	// When user runs `ssh localexe new`, there's no PTY by default
	// But when they run `ssh localexe` (interactive shell), there is a PTY
	// We want to show spinner for direct commands too, since a human is likely watching
	_, _, isPty := s.Pty()

	// If we have a PTY, definitely show spinner (interactive session)
	if isPty {
		return true
	}

	// No PTY - this could be:
	// 1. A direct command like `ssh localexe new` (human might be watching)
	// 2. Output redirection like `ssh localexe new > file` (no human watching)
	// 3. Piped output like `ssh localexe new | grep something` (no human watching)

	// Since we can't reliably detect client-side redirection over SSH,
	// the safest default for non-PTY sessions is to NOT show progress updates.
	// This prevents messy output in files and pipes.
	//
	// Users who want to see progress for direct commands have two options:
	// 1. Force PTY allocation: `ssh -t localexe new`
	// 2. Run interactively: `ssh localexe` then `new`
	return false
}

// authenticatePublicKey handles public key authentication
func (ss *SSHServer) authenticatePublicKey(ctx ssh.Context, key ssh.PublicKey) bool {
	// Increment auth attempts metric
	ss.server.sshMetrics.authAttempts.WithLabelValues("attempt", "public_key").Inc()
	// Convert gliderlabs public key to golang.org/x/crypto/ssh public key for compatibility
	goKey, err := gossh.ParsePublicKey(key.Marshal())
	if err != nil {
		ss.server.slog().Error("failed to parse public key", "error", err)
		return false
	}

	// Create a minimal ConnMetadata implementation to pass username to AuthenticatePublicKey
	connMeta := &minimalConnMetadata{
		user:       ctx.User(),
		remoteAddr: ctx.RemoteAddr(),
	}

	// Use existing authentication logic
	perms, err := ss.server.AuthenticatePublicKey(connMeta, goKey)
	if err != nil {
		ss.server.slog().Error("authentication failed", "error", err)
		// Increment failed auth metric
		ss.server.sshMetrics.authAttempts.WithLabelValues("failed", "public_key").Inc()
		return false
	}

	// Increment successful auth metric
	ss.server.sshMetrics.authAttempts.WithLabelValues("success", "public_key").Inc()

	// Store permissions in context for later use
	// Note: fingerprint removed as no longer needed in context
	ctx.SetValue("registered", perms.Extensions["registered"])
	ctx.SetValue("email", perms.Extensions["email"])
	ctx.SetValue("public_key", perms.Extensions["public_key"])

	return true
}

// handleSession handles SSH sessions
func (ss *SSHServer) handleSession(rawSession ssh.Session) {
	s := sshsession.NewManaged(rawSession)
	defer s.Close()

	// Track SSH connection
	ss.server.sshMetrics.connectionsTotal.WithLabelValues("connected").Inc()
	ss.server.sshMetrics.connectionsCurrent.Inc()
	sessionStart := time.Now()

	defer func() {
		// Track connection end and duration
		ss.server.sshMetrics.connectionsCurrent.Dec()
		duration := time.Since(sessionStart).Seconds()
		ss.server.sshMetrics.sessionDuration.WithLabelValues("normal").Observe(duration)
	}()

	// Check for special container-logs username format
	username := s.User()
	if strings.HasPrefix(username, "container-logs:") {
		// Parse format: "container-logs:<allocID>:<containerID>:<boxName>"
		parts := strings.Split(username, ":")
		if len(parts) == 4 {
			allocID := parts[1]
			containerID := parts[2]
			boxName := parts[3]

			// Show container logs
			ss.handleContainerLogs(s, allocID, containerID, boxName)
			return
		}
	}
	if slices.Contains(boxname.JobsRelated, username) {
		s.Write([]byte("Oh hai. Nice find. Come work with us: david+magicuser@bold.dev\n"))
		s.Close()
		s.Exit(0)
		return
	}

	// Get authentication info from context
	publicKey, _ := s.Context().Value("public_key").(string)
	registered := s.Context().Value("registered").(string) == "true"
	// email, _ := s.Context().Value("email").(string) // Currently unused

	// Check for exec command
	cmd := s.Command()
	if len(cmd) > 0 {
		// Handle exec commands
		ss.handleExec(s, cmd, publicKey, registered)
		return
	}

	// Handle interactive shell session
	ss.handleShell(s, publicKey, registered)
}

// handleShell handles interactive shell sessions with readline
func (ss *SSHServer) handleShell(s sshsession.Session, publicKey string, registered bool) {
	if !registered {
		// Handle registration flow
		ss.handleRegistration(s, publicKey)
		return
	}

	// Create user session for registered users
	user, err := ss.server.getUserByPublicKey(s.Context(), publicKey)
	if err != nil {
		fmt.Fprintf(s, "Error retrieving user info: %v\r\n", err)
		return
	}
	if user == nil {
		fmt.Fprintf(s, "Error: User not found\r\n")
		return
	}

	// Get or create user's alloc
	alloc, err := ss.server.getUserAlloc(s.Context(), user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprintf(s, "Error: User has no allocation\r\n")
		return
	}

	shell := sshsession.NewShell(s)
	ss.runMainShellWithReadline(shell, publicKey, user)
}

func (ss *SSHServer) displayWelcomeTip(s exemenu.ShellSession, user *exedb.User) {
	// Check what the user has done so far to determine what tips to show
	userEvents := ss.server.allUserEventsBestEffort(s.Context(), user.UserID)
	hasCreatedBox := userEvents[userEventCreatedBox] > 0
	hasUsedRepl := userEvents[userEventUsedREPL] > 0
	hasSetBrowserCookies := ss.server.userHasActiveAuthCookieBestEffort(s.Context(), user.UserID)
	hasRunHelp := userEvents[userEventHasRunHelp] > 0 // TODO: maybe > 1 or > 2? or something recency-based?

	// Check if this is a web shell session
	_, isWebShell := s.(*WebShellSession)

	line := func(msg string, args ...any) {
		fmt.Fprintf(s, msg+"\r\n", args...)
	}

	line("")
	if !hasUsedRepl {
		line("Welcome to EXE.DEV!")
		line("")
	}
	var printedTip bool
	if !hasCreatedBox {
		line("- \033[1mnew\033[0m to create your first box")
		printedTip = true
	}
	if !hasSetBrowserCookies && !isWebShell {
		line("- \033[1mbrowser\033[0m to speed-login on the web")
		printedTip = true
	}
	if !hasRunHelp {
		line("- \033[1mhelp\033[0m to see a list of commands")
		printedTip = true
	}
	if printedTip {
		line("")
	}
}

// runMainShellWithReadline implements the main menu using a simple line reader
func (ss *SSHServer) runMainShellWithReadline(s exemenu.ShellSession, publicKey string, user *exedb.User) {
	ss.server.slog().Debug("start runMainShellWithReadline", "public_key", publicKey, "email", user.Email)
	// Show welcome message, hints, tips, etc.
	ss.displayWelcomeTip(s, user)

	ss.server.recordUserEventBestEffort(s.Context(), user.UserID, userEventUsedREPL)

	// Create a terminal using golang.org/x/term
	terminal := term.NewTerminal(s, "\033[1;36mexe.dev\033[0m \033[37m▶\033[0m ")
	ctx := s.Context()

	// Set the terminal size to the pty size, and keep it updated whenever the pty changes.
	_, winSizeCh, _ := s.Pty()
	go func() {
		for {
			select {
			case w := <-winSizeCh:
				terminal.SetSize(w.Width, w.Height)
			case <-s.Context().Done():
				return
			}
		}
	}()

	alloc, err := ss.server.getUserAlloc(ctx, user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprint(s, "Error: User not associated with any allocation\r\n")
		return
	}

	ss.server.slog().Info("starting repl", "public_key", publicKey, "email", user.Email)
	for {
		// Read line with tab completion
		line, err := ss.readLineWithCompletion(terminal, user, alloc, publicKey, s)
		if err != nil {
			if err == io.EOF {
				fmt.Fprint(s, "Goodbye!\r\n")
			}
			return
		}

		if line == "" {
			continue
		}
		ss.server.slog().Debug("command received", "line", line)

		parts, err := shlex.Split(strings.TrimSpace(line), true)
		if err != nil {
			fmt.Fprintf(s, "Error parsing command: %v\r\n", err)
			continue
		}
		if len(parts) == 0 {
			continue
		}

		cc := &exemenu.CommandContext{
			User: &exemenu.UserInfo{
				ID:    user.UserID,
				Email: user.Email,
			},
			Alloc: &exemenu.AllocInfo{
				ID:        alloc.AllocID,
				Type:      string(alloc.AllocType),
				Region:    string(alloc.Region),
				CreatedAt: alloc.CreatedAt,
			},
			PublicKey:  publicKey,
			Args:       []string{}, // ExecuteCommand will determine the real args
			Output:     s,
			SSHSession: s,
			Terminal:   terminal, // Interactive terminal available
			DevMode:    ss.server.devMode == "local",
			Logger:     ss.server.slog(),
		}

		// Execute command using new system
		rc := ss.commands.ExecuteCommand(ctx, cc, parts)
		if rc == -1 {
			// EOF
			return
		}
	}
}

// showAnimatedWelcome displays the ASCII art with a beautiful fade-out animation
func (ss *SSHServer) showAnimatedWelcome(s sshsession.Session) {
	// Skip animation in test mode for faster tests
	if ss.server.devMode == "test" {
		fmt.Fprint(s, "~~~ EXE.DEV ~~~\r\n")
		return
	}

	asciiArt := []string{
		"███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗",
		"██╔════╝╚██╗██╔╝██╔════╝   ██╔══██╗██╔════╝██║   ██║",
		"█████╗   ╚███╔╝ █████╗     ██║  ██║█████╗  ██║   ██║",
		"██╔══╝   ██╔██╗ ██╔══╝     ██║  ██║██╔══╝  ╚██╗ ██╔╝",
		"███████╗██╔╝ ██╗███████╗██╗██████╔╝███████╗ ╚████╔╝ ",
		"╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  ",
	}

	// Draw each line left-aligned (initial display in green)
	for i, line := range asciiArt {
		fmt.Fprintf(s, "\033[1;92m%s\033[0m", line)
		if i < len(asciiArt)-1 {
			fmt.Fprint(s, "\r\n")
		}
	}

	// Move cursor back to start of ASCII art for animation
	fmt.Fprintf(s, "\033[%dA", len(asciiArt)-1)
	fmt.Fprint(s, "\r")

	// Query background color (with timeout fallback to black)
	bg := termfun.QueryBackgroundColor(s, s.CtxReader())

	// Fade from bright green to background color
	from := termfun.RGB{R: 80, G: 255, B: 120}
	to := bg

	// Animate with proper 24-bit colors - more frames for smoother animation
	termfun.FadeTextInPlace(s, asciiArt, from, to, 900*time.Millisecond, 30)

	// After animation, cursor is at the last line of the art
	// Move back to first line and clear the art lines
	fmt.Fprintf(s, "\033[%dA", len(asciiArt)-1)
	for i := 0; i < len(asciiArt); i++ {
		fmt.Fprint(s, "\033[2K") // Clear entire line
		if i < len(asciiArt)-1 {
			fmt.Fprint(s, "\r\n")
		}
	}

	// Move cursor back to where the art started
	fmt.Fprintf(s, "\033[%dA", len(asciiArt)-1)
}

// readLineWithEchoAndDefault reads a line with echo and optionally pre-fills a default value.
// It returns the entered line and a boolean indicating whether the user pressed enter.
func (ss *SSHServer) readLineWithEchoAndDefault(s ssh.Session, defaultValue string) (string, bool) {
	var line []byte
	buf := make([]byte, 1)

	// Pre-fill with default value if provided
	if defaultValue != "" {
		line = []byte(defaultValue)
		fmt.Fprint(s, defaultValue)
	}

	for {
		n, err := s.Read(buf)
		if err != nil || n == 0 {
			return "", false
		}

		b := buf[0]
		switch b {
		case '\n', '\r':
			// Enter pressed
			fmt.Fprint(s, "\r\n")
			return strings.TrimSpace(string(line)), true
		case 3: // Ctrl+C
			return "", false
		case 127, 8: // Backspace
			if len(line) > 0 {
				// Remove last character
				line = line[:len(line)-1]
				// Move cursor back, write space, move back again
				fmt.Fprint(s, "\b \b")
			}
		default:
			if b >= 32 && b < 127 { // Printable characters
				line = append(line, b)
				// Echo the character
				fmt.Fprintf(s, "%c", b)
			}
		}
	}
}

// handleRegistration handles the registration flow using readline
func (ss *SSHServer) handleRegistration(s sshsession.Session, publicKey string) {
	ss.showAnimatedWelcome(s)

	// Attempt to identify this as a GitHub user based on their validated public key.
	ghInfo, err := ss.server.githubUser.InfoString(s.Context(), publicKey)
	if err != nil {
		ss.server.slog().InfoContext(s.Context(), "failed to retrieve GitHub user info", "publicKey", publicKey, "error", err)
	}

	fmt.Fprint(s, "\r\n\033[1;33mEXE.DEV: get a box over ssh\033[0m\r\n")
	if ghInfo.Email != "" {
		fmt.Fprintf(s, "\r\n✨ Recognized \033[1m@%s\033[0m's public GitHub SSH key. ✨\r\n", ghInfo.Login)
		fmt.Fprintf(s, "(This key and email are public on GitHub; see %s/docs/ssh-github)\r\n", ss.server.getBaseURL())
		fmt.Fprintf(s, "Confirm this email to log in instantly,\r\n")
		fmt.Fprintf(s, "or enter a different one to get a magic login link.\r\n\r\n")
	} else {
		fmt.Fprint(s, "To sign up, please verify your email.\r\n\r\n")
	}

	// Validate email
	var email string
	suggested := ghInfo.Email
	for !isValidEmail(email) {
		if email != "" {
			fmt.Fprintf(s, "%sInvalid email format. Please enter a valid email address.%s\r\n", "\033[1;31m", "\033[0m")
		}
		prompt := "Please enter your email address:"
		if suggested != "" {
			prompt = "Email:"
		}
		fmt.Fprintf(s, "\033[1m%s\033[0m ", prompt)
		var pressedEnter bool
		email, pressedEnter = ss.readLineWithEchoAndDefault(s, suggested)
		if email == "" || !pressedEnter {
			fmt.Fprint(s, "\r\nRegistration cancelled.\r\n")
			return
		}
		// Only suggest an email the first time around, to avoid being annoying
		suggested = ""
	}

	needsEmailVerification := ghInfo.Email == "" || email != ghInfo.Email
	var user *exedb.User
	if needsEmailVerification {
		user, err = ss.waitForEmailVerification(s, publicKey, email)
		if err != nil || user == nil {
			ss.server.slog().Error("email verification failed", "email", email, "error", err)
			fmt.Fprintf(s, "\r\n\033[1;31m%v\033[0m\r\n", err)
			return
		}
	} else {
		// Email matches GitHub's. Rely on their verification; create user directly now.
		ss.server.slog().Info("email matches GitHub, skipping verification", "email", email)
		newUser, err := ss.server.createUserWithSSHKey(s.Context(), email, publicKey)
		if err != nil {
			ss.server.slog().Error("failed to create user with SSH key during github auto-verification", "error", err)
			fmt.Fprintf(s, "\r\n\033[1;31minternal error: failed to create user account\033[0m\r\n")
			return
		}
		user = newUser
		// TODO: handle new device but existing user case!
	}

	// Get user's alloc for the menu
	alloc, err := ss.server.getUserAlloc(s.Context(), user.UserID)
	if err != nil || alloc == nil {
		ss.server.slog().Error("user has no allocation after registration", "user_id", user.UserID, "email", user.Email, "error", err)
		fmt.Fprintf(s, "internal error: no associated alloc found for %v\r\n", user.Email)
		s.Close()
		return
	}

	// Visual feedback that we're entering the menu
	fmt.Fprintf(s, "\r\n\r\n")

	// Transition directly to the main shell menu
	// We pass the session directly and let runMainShellWithReadline create its own reader
	// This avoids issues with partially consumed readers
	shell := sshsession.NewShell(s)
	ss.runMainShellWithReadline(shell, publicKey, user)
}

func (ss *SSHServer) waitForEmailVerification(s sshsession.Session, publicKey, email string) (*exedb.User, error) {
	ss.server.slog().Debug("starting email verification", "email", email)
	verification, err := ss.startEmailVerification(s, publicKey, email)
	if err != nil {
		switch {
		case err.Error() == "email service not configured":
			return nil, fmt.Errorf("internal error: email service is not configured")
		case strings.Contains(err.Error(), "marked as inactive"):
			return nil, fmt.Errorf("This email address has been blocked by the email provider.\r\nPlease try a different email address.")
		}
		return nil, err
	}

	fmt.Fprintf(s, "\r\nVerification email sent to: \033[1;32m%s\033[0m\r\n", email)
	fmt.Fprintf(s, "Pairing code: \033[1;32m%s\033[0m\r\n", verification.PairingCode)
	fmt.Fprintf(s, "\033[2mWaiting for email verification...\033[0m\r\n")

	ctrlCChan := make(chan struct{}, 1)
	inputCtx, cancelInput := context.WithCancel(s.Context())
	defer cancelInput()

	go func() {
		for {
			b, err := s.ReadByteContext(inputCtx)
			if err != nil {
				return
			}
			if b == 3 {
				select {
				case ctrlCChan <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	// Wait for email verification with Ctrl+C support
	select {
	case <-verification.CompleteChan:
		cancelInput() // stop the reader immediately now that input is no longer needed
		fmt.Fprintf(s, "%s✓ Email verified successfully!%s\r\n\r\n", "\033[1;32m", "\033[0m")
	case <-ctrlCChan:
		cancelInput() // ensure the goroutine exits before we bubble up the cancellation
		return nil, fmt.Errorf("Registration cancelled")
	case <-time.After(10 * time.Minute):
		cancelInput() // ensure the goroutine exits before reporting the timeout
		return nil, fmt.Errorf("Email verification timed out. Please try again.")
	case <-s.Context().Done():
		cancelInput() // ensure the goroutine exits before propagating session closure
		return nil, fmt.Errorf("session disconnected")
	}

	// After successful verification, the user should have been created by the HTTP handler
	// Get the user to verify it was created
	user, userErr := ss.server.getUserByPublicKey(s.Context(), publicKey)
	if userErr != nil || user == nil {
		return nil, fmt.Errorf("internal error: user not found after verification")
	}

	// Store/update the SSH key as verified - use context.Background() to ensure cleanup completes even if client disconnects
	storeErr := ss.server.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpsertSSHKeyForUser(ctx, exedb.UpsertSSHKeyForUserParams{
			UserID:    user.UserID,
			PublicKey: publicKey,
		})
	})
	if storeErr != nil {
		ss.server.slog().Warn("failed to store SSH key after registration", "user_id", user.UserID, "email", user.Email, "error", storeErr)
		// Don't fail here, the key might already exist (TODO: is this right??)
	}

	// Registration complete - wait for user to press Enter
	fmt.Fprintf(s, "\r\n%sRegistration complete!%s\r\n\r\n", "\033[1;32m", "\033[0m")
	if verification.IsNewAccount {
		fmt.Fprintf(s, "Your account has been successfully created.\r\n\r\n")
	} else {
		fmt.Fprintf(s, "Your new ssh key has been added to your existing account.\r\n\r\n")
	}
	return user, nil
}

// handleExec handles exec commands
func (ss *SSHServer) handleExec(s sshsession.Session, cmd []string, publicKey string, registered bool) {
	defer s.Exit(0) // Always send exit status

	if !registered {
		sshTo := ss.server.formatExeDevConnectionInfo()
		fmt.Fprintf(s, "Please complete registration by running: %s\r\n", sshTo)
		s.Exit(1)
		return
	}

	// Get user and team info
	// publicKey is already passed as parameter from context
	user, err := ss.server.getUserByPublicKey(s.Context(), publicKey)
	if err != nil {
		fmt.Fprintf(s, "Authentication error: %v\r\n", err)
		return
	}

	alloc, err := ss.server.getUserAlloc(s.Context(), user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprint(s, "Error: User not associated with any allocation\r\n")
		return
	}

	if len(cmd) == 0 {
		return
	}

	cc := &exemenu.CommandContext{
		User: &exemenu.UserInfo{
			ID:    user.UserID,
			Email: user.Email,
		},
		Alloc: &exemenu.AllocInfo{
			ID:        alloc.AllocID,
			Type:      string(alloc.AllocType),
			Region:    string(alloc.Region),
			CreatedAt: alloc.CreatedAt,
		},
		PublicKey:  publicKey,
		Args:       cmd[1:],                        // Skip the command name itself
		Output:     exemenu.NewANSIFilterWriter(s), // Filter out ANSI control codes from non-interactive sessions.
		SSHSession: sshsession.NewShell(s),
		Terminal:   nil, // No interactive terminal for exec mode
		DevMode:    ss.server.devMode == "local",
		Logger:     ss.server.slog(),
	}

	rc := ss.commands.ExecuteCommand(s.Context(), cc, cmd) // Just the command name
	ss.server.slog().Debug("ssh exec command completed", "command", strings.Join(cmd, " "), "rc", rc)
	if rc > 0 {
		s.Close()
		s.Exit(rc)
	}
}

// handleContainerLogs shows logs for a failed container
func (ss *SSHServer) handleContainerLogs(s ssh.Session, allocID, containerID, boxName string) {
	// Show error message about container failure
	fmt.Fprintf(s, "\033[1;31mContainer '%s' failed to start\033[0m\r\n\r\n", boxName)

	// Get logs if container manager is available
	ctx, cancel := context.WithTimeout(s.Context(), 5*time.Second)
	defer cancel()

	// Get container logs
	logs, err := ss.server.containerManager.GetContainerLogs(ctx, allocID, containerID, 100)
	if err != nil {
		fmt.Fprintf(s, "\033[1;33mFailed to retrieve container logs: %v\033[0m\r\n", err)
		return
	}

	if len(logs) > 0 {
		fmt.Fprintf(s, "\033[1;36mContainer logs:\033[0m\r\n")
		fmt.Fprintf(s, "────────────────────────────────────────\r\n")
		for _, line := range logs {
			fmt.Fprintf(s, "%s\r\n", line)
		}
		fmt.Fprintf(s, "────────────────────────────────────────\r\n\r\n")
	} else {
		fmt.Fprintf(s, "\033[1;33mNo logs available\033[0m\r\n")
	}

	fmt.Fprintf(s, "To delete this failed container, run:\r\n")
	fmt.Fprintf(s, "  \033[1m%s delete %s\033[0m\r\n", ss.server.formatExeDevConnectionInfo(), boxName)
}

func (ss *SSHServer) startEmailVerification(s ssh.Session, publicKey, email string) (*EmailVerification, error) {
	// Check whether this email already exists
	_, err := withRxRes(ss.server, s.Context(), func(ctx context.Context, q *exedb.Queries) (any, error) {
		return q.GetUserIDByEmail(ctx, email)
	})
	var isNewAccount bool
	switch {
	case err == nil:
		isNewAccount = false
	case errors.Is(err, sql.ErrNoRows):
		isNewAccount = true
	default:
		return nil, fmt.Errorf("failed to check existing email: %v", err)
	}

	if !isNewAccount {
		// Email already exists - this is a new ssh key for an existing user.
		verif := ss.server.addEmailVerification(publicKey, email, isNewAccount)

		err := ss.server.withTx(s.Context(), func(ctx context.Context, q *exedb.Queries) error {
			return q.InsertPendingSSHKey(ctx, exedb.InsertPendingSSHKeyParams{
				Token:     verif.Token,
				PublicKey: publicKey,
				UserEmail: email,
				ExpiresAt: time.Now().Add(15 * time.Minute),
			})
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create verification token: %v", err)
		}

		// Send new device verification email
		subject := "New ssh key login - EXE.DEV"
		verifyURL := fmt.Sprintf("%s/verify-device?token=%s", ss.server.getBaseURL(), verif.Token)
		body := fmt.Sprintf(`Hello,

A new ssh key is trying to register with your EXE.DEV account email, with public key:

%s

If this was you, please click the link below to authorize this device:

%s

If you did not attempt to register from a new device, please ignore this email.

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, publicKey, verifyURL)

		if err := ss.server.sendEmail(email, subject, body); err != nil {
			ss.server.deleteEmailVerification(verif)
			return nil, fmt.Errorf("failed to send verification email: %v", err)
		}
		if ss.server.devMode != "" {
			fmt.Fprintf(s, "\r\n[DEV-ONLY] Emailed link: \033[1;36m%s\033[0m\r\n\r\n", verifyURL)
		}

		return verif, nil
	}

	// New user registration
	verif := ss.server.addEmailVerification(publicKey, email, isNewAccount)

	// Send verification email
	subject := "Welcome to EXE.DEV - Verify Your Email"
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s&s=exemenu", ss.server.getBaseURL(), verif.Token)
	body := fmt.Sprintf(`Welcome to EXE.DEV!

Please click the link below to verify your email address:

%s

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, verifyURL)

	if err := ss.server.sendEmail(email, subject, body); err != nil {
		ss.server.deleteEmailVerification(verif)
		return nil, fmt.Errorf("failed to send verification email: %v", err)
	}
	if ss.server.devMode != "" {
		fmt.Fprintf(s, "\r\n[DEV-ONLY] Emailed link: \033[1;36m%s\033[0m\r\n\r\n", verifyURL)
	}

	return verif, nil
}

func (s *Server) addEmailVerification(publicKey, email string, isNewAccount bool) *EmailVerification {
	token := generateRegistrationToken()
	pairingCode := generatePairingCode()

	verification := &EmailVerification{
		PublicKey:    publicKey,
		Email:        email,
		Token:        token,
		PairingCode:  pairingCode,
		CompleteChan: make(chan struct{}),
		CreatedAt:    time.Now(),
		IsNewAccount: isNewAccount,
	}
	s.emailVerificationsMu.Lock()
	defer s.emailVerificationsMu.Unlock()
	s.emailVerifications[token] = verification
	return verification
}

func (s *Server) deleteEmailVerification(verif *EmailVerification) {
	s.emailVerificationsMu.Lock()
	defer s.emailVerificationsMu.Unlock()
	delete(s.emailVerifications, verif.Token)
}

func (s *Server) lookUpEmailVerification(token string) *EmailVerification {
	s.emailVerificationsMu.Lock()
	defer s.emailVerificationsMu.Unlock()
	return s.emailVerifications[token]
}

// readLineWithCompletion reads a line from the terminal with tab completion support
func (ss *SSHServer) readLineWithCompletion(terminal *term.Terminal, user *exedb.User, alloc *exedb.Alloc, publicKey string, s exemenu.ShellSession) (string, error) {
	// Set up tab completion using AutoCompleteCallback
	var lastCompletionLine string
	var lastCompletionPos int
	var lastCompletionResults []string
	terminal.AutoCompleteCallback = func(line string, pos int, key rune) (newLine string, newPos int, ok bool) {
		// Only handle tab key
		if key != '\t' {
			return "", 0, false
		}

		if strings.TrimSpace(line) == "" {
			return line, pos, true
		}

		// Create command context for completion
		cc := &exemenu.CommandContext{
			User: &exemenu.UserInfo{
				ID:    user.UserID,
				Email: user.Email,
			},
			Alloc: &exemenu.AllocInfo{
				ID:        alloc.AllocID,
				Type:      string(alloc.AllocType),
				Region:    string(alloc.Region),
				CreatedAt: alloc.CreatedAt,
			},
			PublicKey:  publicKey,
			Output:     s,
			SSHSession: s,
			Terminal:   terminal,
			DevMode:    ss.server.devMode == "local",
			Logger:     ss.server.slog(),
		}

		// Get completions
		completions := ss.commands.CompleteCommand(line, pos, cc)

		if line == lastCompletionLine && pos == lastCompletionPos && slices.Equal(completions, lastCompletionResults) {
			return line, pos, true
		}

		if len(completions) == 0 {
			return "", 0, false // No completions, handle tab normally
		}

		if len(completions) == 1 {
			// Single completion - auto-complete
			lastCompletionLine = ""
			lastCompletionPos = 0
			lastCompletionResults = nil
			newLine, newPos := ss.applySingleCompletion(line, pos, completions[0])
			return newLine, newPos, true
		}

		// Multiple completions - show options and return original line
		ss.showCompletions(terminal, completions)
		lastCompletionLine = line
		lastCompletionPos = pos
		lastCompletionResults = slices.Clone(completions)
		return line, pos, true // Don't modify the line, just show completions
	}

	// Use regular ReadLine with completion enabled
	return terminal.ReadLine()
}

// applySingleCompletion applies a single completion to the line
func (ss *SSHServer) applySingleCompletion(line string, pos int, completion string) (string, int) {
	// Find the start of the word being completed
	wordStart := pos
	for wordStart > 0 && line[wordStart-1] != ' ' && line[wordStart-1] != '\t' {
		wordStart--
	}

	// Replace the partial word with the completion + space
	newLine := line[:wordStart] + completion + " " + line[pos:]
	newPos := wordStart + len(completion) + 1

	return newLine, newPos
}

// showCompletions displays multiple completion options
func (ss *SSHServer) showCompletions(terminal *term.Terminal, completions []string) {
	terminal.Write([]byte("\r\n"))
	for i, completion := range completions {
		terminal.Write([]byte(completion))
		if i < len(completions)-1 {
			terminal.Write([]byte("  "))
		}
	}
	terminal.Write([]byte("\r\n"))
}
