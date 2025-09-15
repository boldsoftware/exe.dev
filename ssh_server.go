package exe

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"exe.dev/billing"
	"exe.dev/exedb"
	"exe.dev/sqlite"
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
	billing  billing.Billing
	commands *CommandTree
}

// NewSSHServer creates a new SSH server using gliderlabs/ssh
func NewSSHServer(s *Server, billing billing.Billing) *SSHServer {
	ss := &SSHServer{
		server:  s,
		billing: billing,
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
			"session": ssh.DefaultSessionHandler,
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
		log.Printf("Added host key from main server configuration")
	} else {
		log.Printf("Warning: No host key found in main server configuration")
	}

	if ss.server == nil || !ss.server.testMode {
		tcpAddr, ok := ln.Addr().(*net.TCPAddr)
		if ok {
			slog.Info("starting SSH server", "addr", tcpAddr.String(), "ip", tcpAddr.IP.String(), "port", tcpAddr.Port)
		} else {
			slog.Info("starting SSH server", "addr", ln.Addr().String(), "net", ln.Addr().Network())
		}
	}

	return ss.srv.Serve(ln)
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
func (ss *SSHServer) shouldShowSpinner(s ssh.Session) bool {
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
		log.Printf("Failed to parse public key: %v", err)
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
		log.Printf("Authentication failed: %v", err)
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
func (ss *SSHServer) handleSession(s ssh.Session) {
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
func (ss *SSHServer) handleShell(s ssh.Session, publicKey string, registered bool) {
	// publicKey is already passed as parameter from context

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

	ss.runMainShellWithReadline(s, publicKey, user)
}

func (ss *SSHServer) displayWelcomeTip(s ssh.Session, user *User) {
	// Check if user has created their first box to determine if we should show the welcome message
	userEvents := ss.server.allUserEventsBestEffort(s.Context(), user.UserID)
	hasCreatedBox := userEvents[userEventCreatedBox] > 0
	hasUsedRepl := userEvents[userEventUsedREPL] > 0

	line := func(msg string, args ...any) {
		fmt.Fprintf(s, msg+"\r\n", args...)
	}

	line("")
	if !hasUsedRepl {
		line("Welcome to EXE.DEV!")
		line("")
	}
	if !hasCreatedBox {
		line("To create your first box, run:")
		line("")
		line("  new")
		line("")
		line("Or type `help` to see a list of commands.")
	}
}

// runMainShellWithReadline implements the main menu using a simple line reader
func (ss *SSHServer) runMainShellWithReadline(s ssh.Session, publicKey string, user *User) {
	slog.Debug("start runMainShellWithReadline", "public_key", publicKey, "email", user.Email)
	// Show welcome message, hints, tips, etc.
	ss.displayWelcomeTip(s, user)

	ss.server.recordUserEventBestEffort(s.Context(), user.UserID, userEventUsedREPL)

	// Create a terminal using golang.org/x/term
	terminal := term.NewTerminal(s, "\033[1;36mexe.dev\033[0m \033[37m▶\033[0m ")
	ctx := s.Context()

	alloc, err := ss.server.getUserAlloc(ctx, user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprint(s, "Error: User not associated with any allocation\r\n")
		return
	}

	// Command loop using new command system
	if !ss.server.testMode {
		log.Printf("Entering command loop")
	}
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
		slog.Debug("Command received: " + line)

		parts, err := shlex.Split(strings.TrimSpace(line), true)
		if err != nil {
			fmt.Fprintf(s, "Error parsing command: %v\r\n", err)
			continue
		}
		if len(parts) == 0 {
			continue
		}

		cc := &CommandContext{
			User:       user,
			Alloc:      alloc,
			PublicKey:  publicKey,
			Args:       []string{}, // ExecuteCommand will determine the real args
			SSHServer:  ss,
			Output:     s,
			SSHSession: s,
			Terminal:   terminal, // Interactive terminal available
		}

		// Execute command using new system
		err = ss.commands.ExecuteCommand(ctx, cc, parts)
		if err == io.EOF {
			return
		}
		if err != nil {
			fmt.Fprintf(s, "Error: %v\r\n", err)
		}
	}
}

// showAnimatedWelcome displays the ASCII art with a beautiful fade-out animation
func (ss *SSHServer) showAnimatedWelcome(s ssh.Session) {
	// Skip animation in test mode for faster tests
	if ss.server.testMode {
		fmt.Fprint(s, "~~~ EXE.DEV ~~~\r\n")
		return
	}

	// More compact ASCII art that fits better in terminals
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
	bg := termfun.QueryBackgroundColor(s, s)

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

// readLineWithEcho reads a line with echo (for registration)
// This uses direct byte reading to avoid buffering issues when transitioning to the menu
func (ss *SSHServer) readLineWithEcho(s ssh.Session) string {
	return ss.readLineWithEchoAndDefault(s, "")
}

// readLineWithEchoAndDefault reads a line with echo and optionally pre-fills a default value
func (ss *SSHServer) readLineWithEchoAndDefault(s ssh.Session, defaultValue string) string {
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
			return ""
		}

		b := buf[0]
		switch b {
		case '\n', '\r':
			// Enter pressed
			fmt.Fprint(s, "\r\n")
			return strings.TrimSpace(string(line))
		case 3: // Ctrl+C
			return ""
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
func (ss *SSHServer) handleRegistration(s ssh.Session, publicKey string) {
	ss.showAnimatedWelcome(s)

	signupContent := "\r\n\033[1;33mEXE.DEV: get a machine over ssh\033[0m\r\n" +
		"To sign up, verify your email and set up billing.\r\n\r\n"
	fmt.Fprint(s, signupContent)

	// Validate email
	var email string
	for !ss.server.isValidEmail(email) {
		if email != "" {
			fmt.Fprintf(s, "%sInvalid email format. Please enter a valid email address.%s\r\n", "\033[1;31m", "\033[0m")
		}
		fmt.Fprint(s, "\033[1mPlease enter your email address:\033[0m ")
		email = ss.readLineWithEcho(s)
		if email == "" {
			fmt.Fprint(s, "\r\nRegistration cancelled.\r\n")
			return
		}
	}

	// No longer ask for team name - boxes will be named directly under exe.dev

	// Log for debugging
	if !ss.server.testMode {
		slog.Debug("Starting email verification", "email", email)
	}

	// Start email verification directly without using sshbuf.Channel
	if err := ss.startEmailVerificationNew(s.Context(), publicKey, email); err != nil {
		// Log the error for debugging
		log.Printf("Email verification failed for %s: %v", email, err)
		// Show user-friendly error message
		if err.Error() == "email service not configured" {
			fmt.Fprintf(s, "\r\n%sError: Email service is not configured. Cannot send verification email.%s\r\n", "\033[1;31m", "\033[0m")
			fmt.Fprintf(s, "Please contact support at support@exe.dev\r\n")
		} else if strings.Contains(err.Error(), "marked as inactive") {
			fmt.Fprintf(s, "\r\nError: This email address cannot receive emails (blocked by email provider).\r\nPlease try a different email address.\r\n")
		} else {
			fmt.Fprintf(s, "\r\nError sending verification email: %v\r\n", err)
		}
		return
	}

	// Get the verification details for displaying the URL
	verification, exists := ss.server.getEmailVerification(publicKey)
	if !exists {
		fmt.Fprintf(s, "%sError: Verification process failed%s\r\n", "\033[1;31m", "\033[0m")
		return
	}

	fmt.Fprintf(s, "%sVerification email sent to %s%s.\r\n", "\033[1;32m", "\033[0m", email)

	// Only show the verification URL in dev mode
	if ss.server.devMode != "" {
		verifyURL := fmt.Sprintf("%s/verify-email?token=%s", ss.server.getBaseURL(), verification.Token)
		fmt.Fprintf(s, "\r\nPlease click the link in your email to verify your account:\r\n")
		fmt.Fprintf(s, "\033[1;36m%s\033[0m\r\n\r\n", verifyURL)
	}

	fmt.Fprintf(s, "\033[2mWaiting for email verification...\033[0m\r\n")

	// Create channels and atomic bool for coordinating with Ctrl+C handler
	ctrlCChan := make(chan struct{})
	goroutineDone := make(chan struct{})
	var verificationComplete atomic.Bool

	// Start goroutine to handle Ctrl+C and discard other input during verification
	go func() {
		defer close(goroutineDone)
		buf := make([]byte, 1)
		for {
			n, err := s.Read(buf)
			if err != nil {
				// Connection closed or error
				return
			}
			if n > 0 {
				// Check if verification is complete
				if verificationComplete.Load() {
					// Verification complete, exit goroutine
					return
				}

				// Check for Ctrl+C
				if buf[0] == 3 { // Ctrl+C
					select {
					case <-ctrlCChan:
						// Already closed
					default:
						close(ctrlCChan)
					}
					return
				}
				// Discard any other input during verification
			}
		}
	}()

	// Wait for email verification with Ctrl+C support
	select {
	case <-verification.CompleteChan:
		fmt.Fprintf(s, "%s✓ Email verified successfully!%s\r\n\r\n", "\033[1;32m", "\033[0m")
		// Signal the goroutine that verification is complete
		verificationComplete.Store(true)
	case <-ctrlCChan:
		fmt.Fprintf(s, "\r\n%sRegistration cancelled.%s\r\n", "\033[1;33m", "\033[0m")
		return
	case <-time.After(10 * time.Minute):
		fmt.Fprintf(s, "%sEmail verification timed out. Please try again.%s\r\n", "\033[1;31m", "\033[0m")
		verificationComplete.Store(true) // Stop the goroutine
		<-goroutineDone                  // Wait for goroutine to exit
		return
	case <-s.Context().Done():
		// Session disconnected
		verificationComplete.Store(true) // Stop the goroutine
		return
	}

	// After successful verification, the user should have been created by the HTTP handler
	// Get the user to verify it was created
	user, userErr := ss.server.getUserByPublicKey(s.Context(), publicKey)
	if userErr != nil || user == nil {
		log.Printf("Error: User not found after verification: %v", userErr)
		fmt.Fprintf(s, "Error loading user profile. Please try registering again.\r\n")
		return
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
		log.Printf("Error storing SSH key: %v", storeErr)
		// Don't fail here, the key might already exist
	}

	// TODO: Set the default team for the SSH key if not already set

	// Registration complete - wait for user to press Enter
	fmt.Fprintf(s, "\r\n%sRegistration complete!%s\r\n\r\n", "\033[1;32m", "\033[0m")
	fmt.Fprintf(s, "Your account has been successfully created.\r\n\r\n")
	fmt.Fprintf(s, "%sPress any key to continue...%s", "\033[1;36m", "\033[0m")

	// Wait for the goroutine to exit (user presses Enter or any key)
	<-goroutineDone

	// Get user's alloc for the menu
	alloc, err := ss.server.getUserAlloc(s.Context(), user.UserID)
	if err != nil || alloc == nil {
		slog.Error("user has no allocation after registration", "user_id", user.UserID, "email", user.Email, "error", err)
		fmt.Fprintf(s, "internal error: no associated alloc found for %v\r\n", user.Email)
		s.Close()
		return
	}

	// Visual feedback that we're entering the menu
	fmt.Fprintf(s, "\r\n\r\n")

	// Transition directly to the main shell menu
	// We pass the session directly and let runMainShellWithReadline create its own reader
	// This avoids issues with partially consumed readers
	ss.runMainShellWithReadline(s, publicKey, user)
}

// handleExec handles exec commands
func (ss *SSHServer) handleExec(s ssh.Session, cmd []string, publicKey string, registered bool) {
	defer s.Exit(0) // Always send exit status

	if !registered {
		sshTo := "exe.dev"
		if ss.server.devMode != "" {
			sshTo = fmt.Sprintf("-p %v localhost", ss.server.piperdPort)
		}
		fmt.Fprintf(s, "Please complete registration by running: ssh %s\r\n", sshTo)
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

	cc := &CommandContext{
		User:       user,
		Alloc:      alloc,
		PublicKey:  publicKey,
		Args:       cmd[1:], // Skip the command name itself
		SSHServer:  ss,
		Output:     NewANSIFilterWriter(s), // Filter out ANSI control codes from non-interactive sessions.
		SSHSession: s,
		Terminal:   nil, // No interactive terminal for exec mode
	}

	err = ss.commands.ExecuteCommand(s.Context(), cc, cmd) // Just the command name
	if err != nil {
		fmt.Fprintf(s, "Error: %v\r\n", err)
	}
}

// getEmailVerification retrieves an email verification by public key
func (s *Server) getEmailVerification(publicKey string) (*EmailVerification, bool) {
	s.emailVerificationsMu.RLock()
	defer s.emailVerificationsMu.RUnlock()

	for _, v := range s.emailVerifications {
		if strings.TrimSpace(v.PublicKey) == strings.TrimSpace(publicKey) {
			return v, true
		}
	}
	return nil, false
}

// handleContainerLogs shows logs for a failed container
func (ss *SSHServer) handleContainerLogs(s ssh.Session, allocID, containerID, boxName string) {
	// Show error message about container failure
	fmt.Fprintf(s, "\033[1;31mContainer '%s' failed to start\033[0m\r\n\r\n", boxName)

	// Get logs if container manager is available
	if ss.server.containerManager != nil {
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
		fmt.Fprintf(s, "  \033[1mdelete %s\033[0m\r\n", boxName)
	} else {
		fmt.Fprintf(s, "\033[1;31mContainer manager not available\033[0m\r\n")
	}
}

// startEmailVerificationNew is a version of startEmailVerification that doesn't depend on sshbuf.Channel
func (ss *SSHServer) startEmailVerificationNew(ctx context.Context, publicKey, email string) error {
	// Check if this email already exists
	err := ss.server.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		_, err := queries.GetUserIDByEmail(ctx, email)
		return err
	})

	if err == nil {
		// Email already exists - this is a new ssh key for an existing user

		// Don't store in ssh_keys yet - only store verified keys there

		// Generate token for new ssh key verification
		token := ss.server.generateToken()
		expires := time.Now().Add(15 * time.Minute)

		err = ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			queries := exedb.New(tx.Conn())
			return queries.InsertPendingSSHKey(ctx, exedb.InsertPendingSSHKeyParams{
				Token:     token,
				PublicKey: publicKey,
				UserEmail: email,
				ExpiresAt: expires,
			})
		})
		if err != nil {
			return fmt.Errorf("failed to create verification token: %v", err)
		}

		// Create verification object
		verification := &EmailVerification{
			PublicKey:    publicKey,
			Email:        email,
			Token:        token,
			CompleteChan: make(chan struct{}),
			CreatedAt:    time.Now(),
		}

		// Store verification
		ss.server.emailVerificationsMu.Lock()
		ss.server.emailVerifications[token] = verification
		ss.server.emailVerificationsMu.Unlock()

		// Send new device verification email
		subject := "New Device Login - EXE.DEV"
		body := fmt.Sprintf(`Hello,

A new device is trying to register with your EXE.DEV account email.

If this was you, please click the link below to authorize this device:

%s/verify-device?token=%s

If you did not attempt to register from a new device, please ignore this email.

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, ss.server.getBaseURL(), token)

		if err := ss.server.sendEmail(email, subject, body); err != nil {
			ss.server.emailVerificationsMu.Lock()
			delete(ss.server.emailVerifications, token)
			ss.server.emailVerificationsMu.Unlock()
			return fmt.Errorf("failed to send verification email: %v", err)
		}

		return nil
	}

	// New user registration
	token := ss.server.generateToken()

	// Create verification object
	verification := &EmailVerification{
		PublicKey:    publicKey,
		Email:        email,
		Token:        token,
		CompleteChan: make(chan struct{}),
		CreatedAt:    time.Now(),
	}

	// Store verification
	ss.server.emailVerificationsMu.Lock()
	ss.server.emailVerifications[token] = verification
	ss.server.emailVerificationsMu.Unlock()

	// Send verification email
	subject := "Welcome to EXE.DEV - Verify Your Email"
	body := fmt.Sprintf(`Welcome to EXE.DEV!

Please click the link below to verify your email address:

%s/verify-email?token=%s

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, ss.server.getBaseURL(), token)

	if err := ss.server.sendEmail(email, subject, body); err != nil {
		ss.server.emailVerificationsMu.Lock()
		delete(ss.server.emailVerifications, token)
		ss.server.emailVerificationsMu.Unlock()
		return fmt.Errorf("failed to send verification email: %v", err)
	}

	return nil
}

// readLineWithCompletion reads a line from the terminal with tab completion support
func (ss *SSHServer) readLineWithCompletion(terminal *term.Terminal, user *User, alloc *Alloc, publicKey string, s ssh.Session) (string, error) {
	// Set up tab completion using AutoCompleteCallback
	terminal.AutoCompleteCallback = func(line string, pos int, key rune) (newLine string, newPos int, ok bool) {
		// Only handle tab key
		if key != '\t' {
			return "", 0, false
		}

		// Create command context for completion
		cc := &CommandContext{
			User:       user,
			Alloc:      alloc,
			PublicKey:  publicKey,
			SSHServer:  ss,
			Output:     s,
			SSHSession: s,
			Terminal:   terminal,
		}

		// Get completions
		completions := ss.commands.CompleteCommand(line, pos, cc)

		if len(completions) == 0 {
			return "", 0, false // No completions, handle tab normally
		}

		if len(completions) == 1 {
			// Single completion - auto-complete
			newLine, newPos := ss.applySingleCompletion(line, pos, completions[0])
			return newLine, newPos, true
		}

		// Multiple completions - show options and return original line
		ss.showCompletions(terminal, completions)
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
