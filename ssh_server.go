package exe

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"exe.dev/container"

	"exe.dev/termfun"
	"github.com/gliderlabs/ssh"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// SSHServer wraps the gliderlabs SSH server implementation
type SSHServer struct {
	server *Server
	srv    *ssh.Server
}

// NewSSHServer creates a new SSH server using gliderlabs/ssh
func NewSSHServer(s *Server) *SSHServer {
	return &SSHServer{
		server: s,
	}
}

// Start initializes and starts the SSH server
func (ss *SSHServer) Start(addr string) error {
	// Initialize the gliderlabs SSH server
	ss.srv = &ssh.Server{
		Addr:             addr,
		Handler:          ss.handleSession,
		PublicKeyHandler: ss.authenticatePublicKey,
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"session": ssh.DefaultSessionHandler,
		},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{},
		RequestHandlers:   map[string]ssh.RequestHandler{},
	}

	// Add host keys from the existing server configuration
	// The server should already have generated host keys via setupSSHServer
	if ss.server.sshConfig != nil {
		// We need to call generateHostKey to ensure the host key is loaded
		if err := ss.server.generateHostKey(); err != nil {
			log.Printf("Failed to generate host key: %v", err)
		}
	}

	if ss.server == nil || !ss.server.testMode {
		log.Printf("Starting SSH server on %s", addr)
	}
	return ss.srv.ListenAndServe()
}

// Stop gracefully stops the SSH server
func (ss *SSHServer) Stop() error {
	if ss.srv != nil {
		return ss.srv.Close()
	}
	return nil
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

	// Use existing authentication logic
	perms, err := ss.server.AuthenticatePublicKey(nil, goKey)
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

	// Get authentication info from context
	publicKey, _ := s.Context().Value("public_key").(string)
	registered := s.Context().Value("registered").(string) == "true"
	// email, _ := s.Context().Value("email").(string) // Currently unused
	username := s.User()

	// Get terminal dimensions
	pty, winCh, isPty := s.Pty()
	var terminalWidth, terminalHeight int
	if isPty {
		terminalWidth = pty.Window.Width
		terminalHeight = pty.Window.Height
	}

	// Handle window size changes
	if winCh != nil {
		go func() {
			for win := range winCh {
				terminalWidth = win.Width
				terminalHeight = win.Height
			}
		}()
	}

	// Check for exec command
	cmd := s.Command()
	if len(cmd) > 0 {
		// Handle exec commands
		ss.handleExec(s, cmd, username, publicKey, registered)
		return
	}

	// Handle interactive shell session
	ss.handleShell(s, username, publicKey, registered, terminalWidth, terminalHeight)
}

// handleShell handles interactive shell sessions with readline
func (ss *SSHServer) handleShell(s ssh.Session, username, publicKey string, registered bool, terminalWidth, terminalHeight int) {
	// publicKey is already passed as parameter from context

	if !registered {
		// Handle registration flow
		ss.handleRegistration(s, publicKey, terminalWidth)
		return
	}

	// Create user session for registered users
	user, err := ss.server.getUserByPublicKey(publicKey)
	if err != nil {
		fmt.Fprintf(s, "Error retrieving user info: %v\r\n", err)
		return
	}
	if user == nil {
		fmt.Fprintf(s, "Error: User not found\r\n")
		return
	}

	// Get or create user's alloc
	alloc, err := ss.server.getUserAlloc(user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprintf(s, "Error: User has no allocation\r\n")
		return
	}

	// Note: Direct container access should never reach this point.
	// Container connections are handled by the SSH piper plugin via handleMachineAccess,
	// which routes directly to the container without involving exed.
	// If we reach here, the user is connecting to the interactive shell.

	// Run the main shell with readline
	ss.runMainShellWithReadline(s, publicKey, user.Email, alloc.AllocID, false)
}

// runMainShellWithReadline implements the main menu using a simple line reader
func (ss *SSHServer) runMainShellWithReadline(s ssh.Session, publicKey, email, allocID string, showWelcome bool) {
	if !ss.server.testMode {
		log.Printf("runMainShellWithReadline called - email: %s, showWelcome: %v", email, showWelcome)
	}

	helpText := "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
		"\033[1mlist\033[0m                    - List your machines\r\n" +
		"\033[1mnew [args]\033[0m              - Create a new machine\r\n" +
		"\033[1mstart <name>\033[0m            - Start a machine\r\n" +
		"\033[1mstop <name> [...]\033[0m       - Stop one or more machines\r\n" +
		"\033[1mdelete <name>\033[0m           - Delete a machine\r\n" +
		"\033[1mlogs <name>\033[0m             - View machine logs\r\n" +
		"\033[1mroute <machine>\033[0m         - Manage machine routes\r\n" +
		"\033[1mwhoami\033[0m                  - Show your email and SSH keys\r\n" +
		"\033[1m?\033[0m                       - Show this help\r\n" +
		"\033[1mexit\033[0m                    - Exit\r\n\r\n" +
		"Run \033[1mhelp <command>\033[0m for more details\r\n\r\n"

	// Show welcome message
	if showWelcome {
		if !ss.server.testMode {
			log.Printf("Showing welcome banner")
		}
		fmt.Fprint(s, helpText)
		if !ss.server.testMode {
			log.Printf("Welcome banner sent, length: %d bytes", len(helpText))
		}
	} else {
		// No welcome for registered users.
		// They can figure it out.
	}

	// Create a terminal using golang.org/x/term
	terminal := term.NewTerminal(s, "\033[1;36mexe.dev\033[0m \033[37m▶\033[0m ")

	// Command loop using term package
	if !ss.server.testMode {
		log.Printf("Entering command loop")
	}
	for {
		// Read line using terminal (it handles the prompt)
		line, err := terminal.ReadLine()
		if err != nil {
			if err == io.EOF {
				fmt.Fprint(s, "Goodbye!\r\n")
			}
			return
		}

		if !ss.server.testMode {
			log.Printf("Command received: %q", line)
		}

		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) == 0 {
			continue
		}

		cmd := parts[0]
		args := parts[1:]

		switch cmd {
		case "exit":
			fmt.Fprint(s, "Goodbye!\r\n")
			return
		case "help", "?":
			// Check if asking for help on a specific command
			if len(args) > 0 {
				ss.handleHelpCommand(s, args[0])
			} else {
				fmt.Fprint(s, helpText)
			}
		case "list", "ls":
			ss.handleListCommand(s, publicKey, allocID)
		case "new":
			ss.handleNewCommand(s, publicKey, allocID, args)
		case "start":
			ss.handleStartCommand(s, publicKey, allocID, args)
		case "stop":
			ss.handleStopCommand(s, publicKey, allocID, args)
		case "delete":
			ss.handleDeleteCommand(s, publicKey, allocID, args)
		case "logs":
			ss.handleLogsCommand(s, publicKey, allocID, args)
		case "route":
			ss.handleRouteCommand(s, publicKey, allocID, args)
		case "alloc":
			ss.handleAllocCommand(s, publicKey, allocID, args)
		case "whoami":
			ss.handleWhoamiCommand(s, email)
		default:
			fmt.Fprint(s, "Unknown command. Type 'help' for available commands.\r\n")
		}
	}
}

// showAnimatedWelcome displays the ASCII art with a beautiful fade-out animation
func (ss *SSHServer) showAnimatedWelcome(s ssh.Session, terminalWidth int) {
	// Skip animation in test mode for faster tests
	if ss.server.testMode {
		fmt.Fprint(s, "███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗\r\n")
		fmt.Fprint(s, "╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  \r\n\r\n")
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

	// Use provided terminal width or default
	if terminalWidth <= 0 {
		terminalWidth = 140 // Default reasonable width
	}

	// Calculate art width (longest line) - count visual characters, not bytes
	artWidth := len([]rune(asciiArt[0]))
	leftPadding := (terminalWidth - artWidth) / 2
	if leftPadding < 0 {
		leftPadding = 0 // Handle edge case of very narrow terminals
	}

	// Clear screen and move cursor to top
	fmt.Fprint(s, "\033[2J\033[H")

	// Add just 2 lines of vertical padding from the top
	fmt.Fprint(s, "\r\n\r\n")

	// Draw each line with padding centered (initial display in green)
	padding := strings.Repeat(" ", leftPadding)
	for i, line := range asciiArt {
		fmt.Fprintf(s, "%s\033[1;92m%s\033[0m", padding, line)
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
	termfun.FadeTextInPlace(s, asciiArt, leftPadding, from, to, 900*time.Millisecond, 30)

	// After animation, cursor is at the last line of the art
	// Move back to first line and clear everything
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
func (ss *SSHServer) handleRegistration(s ssh.Session, publicKey string, terminalWidth int) {
	// Show the animated welcome first
	ss.showAnimatedWelcome(s, terminalWidth)

	// For the new SSH server, we'll use default colors
	// Terminal detection would require implementing the OSC query which is complex
	grayText := "\033[2m" // Default gray text

	// Show the signup content after the animation
	signupContent := "\r\n\033[1;33mEXE.DEV: get a machine over ssh\033[0m\r\n" +
		"Signup involves verifying your email and setting up billing.\r\n\r\n" +
		"\033[1mPlease enter your email address:\033[0m "
	fmt.Fprint(s, signupContent)

	// Simple line input with echo for email
	email := ss.readLineWithEcho(s)
	if email == "" {
		fmt.Fprint(s, "\r\nRegistration cancelled.\r\n")
		return
	}

	// Validate email
	for !ss.server.isValidEmail(email) {
		fmt.Fprintf(s, "%sInvalid email format. Please enter a valid email address.%s\r\n", "\033[1;31m", "\033[0m")
		fmt.Fprint(s, "\033[1mPlease enter your email address:\033[0m ")
		email = ss.readLineWithEcho(s)
		if email == "" {
			fmt.Fprint(s, "\r\nRegistration cancelled.\r\n")
			return
		}
	}

	// No longer ask for team name - machines will be named directly under exe.dev

	// Log for debugging
	if !ss.server.testMode && !ss.server.quietMode {
		log.Printf("Starting email verification for %s", email)
	}

	// Start email verification directly without using sshbuf.Channel
	if err := ss.startEmailVerificationNew(publicKey, email); err != nil {
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

	fmt.Fprintf(s, "%sWaiting for email verification...%s\r\n", grayText, "\033[0m")

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
	user, userErr := ss.server.getUserByPublicKey(publicKey)
	if userErr != nil || user == nil {
		log.Printf("Error: User not found after verification: %v", userErr)
		fmt.Fprintf(s, "Error loading user profile. Please try registering again.\r\n")
		return
	}

	// Store/update the SSH key as verified
	_, storeErr := ss.server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key, verified, device_name)
		VALUES (?, ?, 1, 'Primary Device')
		ON CONFLICT(public_key) DO UPDATE SET verified = 1, user_id = ?`,
		user.UserID, publicKey, user.UserID)
	if storeErr != nil {
		log.Printf("Error storing SSH key: %v", storeErr)
		// Don't fail here, the key might already exist
	}

	// TODO: Set the default team for the SSH key if not already set

	// Registration complete - wait for user to press Enter
	fmt.Fprintf(s, "\r\n%sRegistration complete!%s\r\n\r\n", "\033[1;32m", "\033[0m")
	fmt.Fprintf(s, "Your account has been successfully created.\r\n\r\n")
	fmt.Fprintf(s, "%sPress any key continue...%s", "\033[1;36m", "\033[0m")

	// Wait for the goroutine to exit (user presses Enter or any key)
	<-goroutineDone

	// Get user's alloc for the menu
	alloc, err := ss.server.getUserAlloc(user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprintf(s, "Error: User not associated with any allocation\r\n")
		return
	}

	// Visual feedback that we're entering the menu
	fmt.Fprintf(s, "\r\n\r\n")

	// Transition directly to the main shell menu
	// We pass the session directly and let runMainShellWithReadline create its own reader
	// This avoids issues with partially consumed readers
	ss.runMainShellWithReadline(s, publicKey, user.Email, alloc.AllocID, true)
}

// handleExec handles exec commands
func (ss *SSHServer) handleExec(s ssh.Session, cmd []string, username, publicKey string, registered bool) {
	defer s.Exit(0) // Always send exit status

	if !registered {
		fmt.Fprint(s, "Please complete registration by running: ssh exe.dev\r\n")
		s.Exit(1)
		return
	}

	// Get user and team info
	// publicKey is already passed as parameter from context
	user, err := ss.server.getUserByPublicKey(publicKey)
	if err != nil {
		fmt.Fprintf(s, "Authentication error: %v\r\n", err)
		return
	}

	alloc, err := ss.server.getUserAlloc(user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprint(s, "Error: User not associated with any allocation\r\n")
		return
	}

	// Handle the command
	if len(cmd) == 0 {
		return
	}

	command := cmd[0]
	args := cmd[1:]

	// Use the new handlers that work directly with ssh.Session
	switch command {
	case "new":
		ss.handleNewCommand(s, publicKey, alloc.AllocID, args)
	case "list", "ls":
		ss.handleListCommand(s, publicKey, alloc.AllocID)
	case "start":
		ss.handleStartCommand(s, publicKey, alloc.AllocID, args)
	case "stop":
		ss.handleStopCommand(s, publicKey, alloc.AllocID, args)
	case "delete":
		ss.handleDeleteCommand(s, publicKey, alloc.AllocID, args)
	case "logs":
		ss.handleLogsCommand(s, publicKey, alloc.AllocID, args)
	case "diag", "diagnostics":
		fmt.Fprintf(s, "\033[1;33mDiagnostics not implemented in new server yet\033[0m\r\n")
	case "route":
		ss.handleRouteCommand(s, publicKey, alloc.AllocID, args)
	case "alloc":
		ss.handleAllocCommand(s, publicKey, alloc.AllocID, args)
	case "whoami":
		ss.handleWhoamiCommand(s, user.Email)
	case "help", "?":
		// Check if asking for help on a specific command
		if len(args) > 0 {
			ss.handleHelpCommand(s, args[0])
		} else {
			// Show help text directly
			helpText := "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
				"\033[1mlist\033[0m                    - List your machines\r\n" +
				"\033[1mnew [args]\033[0m              - Create a new machine\r\n" +
				"\033[1mstart <name>\033[0m            - Start a machine\r\n" +
				"\033[1mstop <name> [...]\033[0m       - Stop one or more machines\r\n" +
				"\033[1mdelete <name>\033[0m           - Delete a machine\r\n" +
				"\033[1mlogs <name>\033[0m             - View machine logs\r\n" +
				"\033[1mdiag <name>\033[0m             - Get machine startup diagnostics\r\n" +
				"\033[1mroute <machine>\033[0m         - Manage machine routes\r\n" +
				"\033[1malloc\033[0m                   - Resource allocation info\r\n" +
				"\033[1mwhoami\033[0m                  - Show your email and SSH keys\r\n" +
				"\033[1m?\033[0m                       - Show this help\r\n\r\n" +
				"Run \033[1mhelp <command>\033[0m for more details\r\n\r\n"
			fmt.Fprint(s, helpText)
		}
	default:
		fmt.Fprintf(s, "Unknown command: %s\r\nRun 'ssh exe.dev help' for available commands.\r\n", command)
	}
}

// handleMachineSSH handles direct SSH access to a machine

// SSHSessionChannel wraps a gliderlabs SSH session to implement compatibility with sshbuf.Channel
type SSHSessionChannel struct {
	ssh.Session
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error
	done   chan struct{}
}

// NewSSHSessionChannel creates a new SSHSessionChannel with buffering support
func NewSSHSessionChannel(s ssh.Session) *SSHSessionChannel {
	c := &SSHSessionChannel{
		Session: s,
		buf:     make([]byte, 0, 4096),
		done:    make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)

	// Start read loop for buffering
	go c.readLoop()

	return c
}

func (c *SSHSessionChannel) readLoop() {
	defer close(c.done)

	readBuf := make([]byte, 4096)
	for {
		n, err := c.Session.Read(readBuf)

		c.mu.Lock()
		if n > 0 {
			c.buf = append(c.buf, readBuf[:n]...)
			c.cond.Signal()
		}
		if err != nil {
			c.err = err
			c.closed = true
			c.cond.Broadcast()
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

// Read implements buffered reading for compatibility with sshbuf.Channel
func (c *SSHSessionChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for len(c.buf) == 0 && !c.closed {
		c.cond.Wait()
	}

	if len(c.buf) == 0 && c.closed {
		if c.err != nil {
			return 0, c.err
		}
		return 0, io.EOF
	}

	n := copy(p, c.buf)
	c.buf = c.buf[n:]

	if len(c.buf) == 0 && cap(c.buf) > 8192 {
		c.buf = make([]byte, 0, 4096)
	}

	return n, nil
}

// ReadCtx implements context-aware reading
func (c *SSHSessionChannel) ReadCtx(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Fast path: data already available or channel closed
	if len(c.buf) > 0 || c.closed {
		if len(c.buf) == 0 && c.closed {
			if c.err != nil {
				return 0, c.err
			}
			return 0, io.EOF
		}

		n := copy(p, c.buf)
		c.buf = c.buf[n:]

		if len(c.buf) == 0 && cap(c.buf) > 8192 {
			c.buf = make([]byte, 0, 4096)
		}

		return n, nil
	}

	// Wait for data with context cancellation
	done := make(chan struct{})
	var n int
	var err error

	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		defer close(done)

		for len(c.buf) == 0 && !c.closed {
			c.cond.Wait()
		}

		if len(c.buf) == 0 && c.closed {
			if c.err != nil {
				err = c.err
			} else {
				err = io.EOF
			}
			return
		}

		n = copy(p, c.buf)
		c.buf = c.buf[n:]

		if len(c.buf) == 0 && cap(c.buf) > 8192 {
			c.buf = make([]byte, 0, 4096)
		}
	}()

	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		c.cond.Broadcast()
		return 0, ctx.Err()
	case <-done:
		c.mu.Lock()
		return n, err
	}
}

// Unread puts data back at the front of the buffer
func (c *SSHSessionChannel) Unread(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(data) == 0 {
		return
	}

	// Prepend the data to the buffer
	newBuf := make([]byte, len(data)+len(c.buf))
	copy(newBuf, data)
	copy(newBuf[len(data):], c.buf)
	c.buf = newBuf

	// Signal any waiting readers
	c.cond.Signal()
}

// Write implements the Write method for compatibility
func (c *SSHSessionChannel) Write(p []byte) (n int, err error) {
	return c.Session.Write(p)
}

// Close implements the Close method for compatibility
func (c *SSHSessionChannel) Close() error {
	return c.Session.Close()
}

// CloseWrite implements the CloseWrite method for compatibility
func (c *SSHSessionChannel) CloseWrite() error {
	// gliderlabs SSH doesn't have CloseWrite, so we just return nil
	return nil
}

// SendRequest implements the SendRequest method for compatibility
func (c *SSHSessionChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	// Not directly supported in gliderlabs SSH sessions
	return false, nil
}

// Stderr implements the Stderr method for compatibility
func (c *SSHSessionChannel) Stderr() io.ReadWriter {
	return c.Session.Stderr()
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

// Command handlers for the new SSH server
func (ss *SSHServer) handleListCommand(s ssh.Session, publicKey, allocID string) {
	// If container manager is available, get real-time status
	if ss.server.containerManager != nil {
		containers, err := ss.server.containerManager.ListContainers(context.Background(), allocID)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError listing machines: %v\033[0m\r\n", err)
			return
		}

		if len(containers) == 0 {
			fmt.Fprintf(s, "No machines found. Create one with 'new'.\r\n")
			return
		}

		fmt.Fprintf(s, "\033[1;36mYour machines:\033[0m\r\n")
		for _, c := range containers {
			status := string(c.Status)
			statusColor := ""
			if c.Status == container.StatusRunning {
				statusColor = "\033[1;32m" // green
				status = "running"
			} else if c.Status == container.StatusStopped {
				statusColor = "\033[1;31m" // red
				status = "stopped"
			} else if c.Status == container.StatusPending {
				statusColor = "\033[1;33m" // yellow
				status = "starting"
			}

			// Show machine with colored status
			fmt.Fprintf(s, "  • \033[1m%s\033[0m - %s%s\033[0m", c.Name, statusColor, status)

			// Add image info if available
			if c.Image != "" && c.Image != "exeuntu" {
				displayImage := container.GetDisplayImageName(c.Image)
				fmt.Fprintf(s, " (%s)", displayImage)
			}

			fmt.Fprintf(s, "\r\n")
		}
		return
	}

	// Fallback to database if container manager not available
	machines, err := ss.server.getMachinesForAlloc(allocID)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError listing machines: %v\033[0m\r\n", err)
		return
	}

	if len(machines) == 0 {
		fmt.Fprintf(s, "No machines found. Create one with 'new'.\r\n")
		return
	}

	fmt.Fprintf(s, "\033[1;36mYour machines:\033[0m\r\n")
	for _, m := range machines {
		status := m.Status
		statusColor := ""
		if status == "running" {
			statusColor = "\033[1;32m"
		} else if status == "stopped" {
			statusColor = "\033[1;31m"
		} else if status == "pending" {
			statusColor = "\033[1;33m"
		}

		fmt.Fprintf(s, "  • \033[1m%s\033[0m - %s%s\033[0m", m.Name, statusColor, status)

		// Add image info if available
		if m.Image != "" && m.Image != "exeuntu" && m.Image != "ubuntu" {
			fmt.Fprintf(s, " (%s)", m.Image)
		}

		fmt.Fprintf(s, "\r\n")
	}
}

func (ss *SSHServer) handleNewCommand(s ssh.Session, publicKey, allocID string, args []string) {
	if ss.server.containerManager == nil {
		fmt.Fprintf(s, "\033[1;31mMachine management is not available\033[0m\r\n")
		return
	}

	// Get user information - needed for machine creation
	user, err := ss.server.getUserByPublicKey(publicKey)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Failed to get user info: %v\033[0m\r\n", err)
		return
	}

	// Create a FlagSet for parsing
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	var machineName string
	var image string
	var size string

	fs.StringVar(&machineName, "name", "", "machine name (auto-generated if not specified)")
	fs.StringVar(&image, "image", "exeuntu", "container image")
	fs.StringVar(&size, "size", "medium", "machine size (small, medium, or large)")

	// Capture the output to avoid printing errors to the session
	var buf bytes.Buffer
	fs.SetOutput(&buf)

	// Parse the flags
	parseErr := fs.Parse(args)
	if parseErr != nil {
		fmt.Fprintf(s, "\033[1;31mError: %v\033[0m\r\n", parseErr)
		fmt.Fprintf(s, "Usage: new [--name=<name>] [--image=<image>] [--size=<size>]\r\n")
		return
	}

	// Check for non-flag arguments (future command execution)
	if fs.NArg() > 0 {
		fmt.Fprintf(s, "\033[1;31mError: Command execution after machine creation is a TODO\033[0m\r\n")
		return
	}

	// Generate machine name if not provided
	if machineName == "" {
		machineName = generateRandomContainerName()
		// Check if name is already taken
		_, err := ss.server.getMachineByName(machineName)
		if err == nil {
			// Name exists, try again
			for attempts := 0; attempts < 10; attempts++ {
				machineName = generateRandomContainerName()
				_, err = ss.server.getMachineByName(machineName)
				if err != nil {
					break
				}
			}
		}
	}

	// Validate machine name (both provided and generated)
	if !ss.server.isValidMachineName(machineName) {
		fmt.Fprintf(s, "\033[1;31mError: Invalid machine name '%s'. Machine names must be lowercase, start with a letter, contain only letters, numbers and hyphens (no consecutive hyphens), and be up to 32 characters\033[0m\r\n", machineName)
		return
	}

	// Get the display image name
	displayImage := container.GetDisplayImageName(image)
	if image == "exeuntu" {
		displayImage = "boldsoftware/exeuntu"
	}

	// Show creation message with proper formatting
	fmt.Fprintf(s, "Creating \033[1m%s\033[0m (%s) using image \033[1m%s\033[0m...\r\n",
		machineName, size, displayImage)

	// Get size preset
	sizePreset, exists := container.ContainerSizes[size]
	if !exists {
		fmt.Fprintf(s, "\033[1;31mError: Invalid size '%s'. Valid sizes: micro, small, medium, large, xlarge\033[0m\r\n", size)
		return
	}

	// Create container request
	req := &container.CreateContainerRequest{
		AllocID:       allocID,
		Name:          machineName,
		Image:         image,
		Size:          size,
		CPURequest:    sizePreset.CPURequest,
		MemoryRequest: sizePreset.MemoryRequest,
		StorageSize:   sizePreset.StorageSize,
		Ephemeral:     false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	createdContainer, err := ss.server.containerManager.CreateContainer(ctx, req)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mFailed to create machine: %v\033[0m\r\n", err)
		return
	}

	// Store container info in database with SSH keys
	imageToStore := container.GetDisplayImageName(image)
	if createdContainer.SSHServerIdentityKey == "" {
		fmt.Fprintf(s, "\033[1;31mError: Container created without SSH keys - this should not happen\033[0m\r\n")
		return
	}

	// Container has SSH keys, use the SSH-enabled storage
	sshKeys := &container.ContainerSSHKeys{
		ServerIdentityKey: createdContainer.SSHServerIdentityKey,
		AuthorizedKeys:    createdContainer.SSHAuthorizedKeys,
		CAPublicKey:       createdContainer.SSHCAPublicKey,
		HostCertificate:   createdContainer.SSHHostCertificate,
		ClientPrivateKey:  createdContainer.SSHClientPrivateKey,
		SSHPort:           createdContainer.SSHPort,
	}
	if err := ss.server.createMachineWithSSHAndDockerHost(user.UserID, allocID, machineName, createdContainer.ID, imageToStore, createdContainer.DockerHost, sshKeys, createdContainer.SSHPort); err != nil {
		fmt.Fprintf(s, "\033[1;33mWarning: Failed to store machine info: %v\033[0m\r\n", err)
	}

	// Check if container is already running (warm pool case)
	if createdContainer.Status == container.StatusRunning {
		sshCommand := ss.server.formatSSHConnectionInfo(allocID, machineName)
		fmt.Fprintf(s, "Ready in ~1s! Access with \033[1m%s\033[0m\r\n\r\n", sshCommand)
		return
	}

	// Show spinner animation while waiting for startup
	fmt.Fprintf(s, "Waiting for startup... ")

	maxWaitTime := 3 * time.Minute
	containerCheckInterval := 2 * time.Second
	timerUpdateInterval := 100 * time.Millisecond
	startTime := time.Now()
	lastContainerCheck := time.Time{}

	// Spinner characters
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerIndex := 0

	for time.Since(startTime) < maxWaitTime {
		// Show spinner and elapsed time (updates every 100ms)
		elapsed := time.Since(startTime)
		spinner := spinners[spinnerIndex%len(spinners)]
		spinnerIndex++
		fmt.Fprintf(s, "\r\033[KWaiting for startup... %s (%.1fs)", spinner, elapsed.Seconds())

		// Check container status only every 2 seconds
		if time.Since(lastContainerCheck) >= containerCheckInterval {
			lastContainerCheck = time.Now()

			containers, err := ss.server.containerManager.ListContainers(context.Background(), allocID)
			if err != nil {
				continue
			}

			var containerFound bool
			var containerStatus container.ContainerStatus
			for _, c := range containers {
				if c.Name == machineName {
					containerFound = true
					containerStatus = c.Status
					break
				}
			}

			if containerFound && containerStatus == container.StatusRunning {
				totalTime := time.Since(startTime)
				sshCommand := ss.server.formatSSHConnectionInfo(allocID, machineName)
				fmt.Fprintf(s, "\r\033[KReady in %.1fs! Access with \033[1m%s\033[0m\r\n\r\n",
					totalTime.Seconds(), sshCommand)
				return
			}
		}

		time.Sleep(timerUpdateInterval)
	}

	// Timed out
	fmt.Fprintf(s, "\r\033[K\033[1;31mTimeout: Machine failed to start within 3 minutes\033[0m\r\n")
}

func (ss *SSHServer) handleStartCommand(s ssh.Session, publicKey, allocID string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify a machine name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: start <machine-name>\r\n")
		return
	}

	machineName := args[0]

	if ss.server.containerManager == nil {
		fmt.Fprintf(s, "\033[1;31mMachine management is not available\033[0m\r\n")
		return
	}

	// Get machine info
	machine, err := ss.server.getMachineByName(machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Machine '%s' not found\033[0m\r\n", machineName)
		return
	}

	if machine.ContainerID == nil {
		fmt.Fprintf(s, "\033[1;31mError: Machine '%s' has no container ID\033[0m\r\n", machineName)
		return
	}

	fmt.Fprintf(s, "Starting \033[1m%s\033[0m...\r\n", machineName)

	// Start the container
	ctx := context.Background()
	err = ss.server.containerManager.StartContainer(ctx, machine.AllocID, *machine.ContainerID)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError starting machine: %v\033[0m\r\n", err)
		return
	}

	// Update database status
	_, err = ss.server.db.Exec(`
		UPDATE machines SET status = 'running', last_started_at = CURRENT_TIMESTAMP
		WHERE name = ?`,
		machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;33mWarning: Failed to update machine status: %v\033[0m\r\n", err)
	}

	sshCommand := ss.server.formatSSHConnectionInfo(allocID, machineName)
	fmt.Fprintf(s, "\033[1;32mMachine started!\033[0m Access with \033[1m%s\033[0m\r\n", sshCommand)
}

func (ss *SSHServer) handleStopCommand(s ssh.Session, publicKey, allocID string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify at least one machine name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: stop <machine-name> [...]\r\n")
		return
	}

	if ss.server.containerManager == nil {
		fmt.Fprintf(s, "\033[1;31mMachine management is not available\033[0m\r\n")
		return
	}

	for _, machineName := range args {
		// Get machine info
		machine, err := ss.server.getMachineByName(machineName)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError: Machine '%s' not found\033[0m\r\n", machineName)
			continue
		}

		if machine.ContainerID == nil {
			fmt.Fprintf(s, "\033[1;31mError: Machine '%s' has no container ID\033[0m\r\n", machineName)
			continue
		}

		fmt.Fprintf(s, "Stopping \033[1m%s\033[0m...\r\n", machineName)

		// Stop the container
		ctx := context.Background()
		err = ss.server.containerManager.StopContainer(ctx, machine.AllocID, *machine.ContainerID)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError stopping machine %s: %v\033[0m\r\n", machineName, err)
			continue
		}

		// Update database status
		_, err = ss.server.db.Exec(`
			UPDATE machines SET status = 'stopped'
			WHERE name = ?`,
			machineName)
		if err != nil {
			fmt.Fprintf(s, "\033[1;33mWarning: Failed to update machine status: %v\033[0m\r\n", err)
		}

		fmt.Fprintf(s, "\033[1;32mMachine '%s' stopped\033[0m\r\n", machineName)
	}
}

func (ss *SSHServer) handleDeleteCommand(s ssh.Session, publicKey, allocID string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify a machine name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: delete <machine-name>\r\n")
		return
	}

	machineName := args[0]

	if ss.server.containerManager == nil {
		// Just delete from database if no container manager
		fmt.Fprintf(s, "Deleting \033[1m%s\033[0m...\r\n", machineName)

		_, err := ss.server.db.Exec(`
			DELETE FROM machines
			WHERE name = ?`,
			machineName)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError deleting machine: %v\033[0m\r\n", err)
			return
		}

		// Deregister from IP allocation strategy if enabled
		if ss.server.ipAllocator != nil {
			if allocErr := ss.server.ipAllocator.Deallocate(allocID, machineName); allocErr != nil {
				// Don't fail the operation if IP deallocation fails
				fmt.Fprintf(s, "\033[1;33mWarning: Failed to deregister machine from IP allocation: %v\033[0m\r\n", allocErr)
			}
		}

		fmt.Fprintf(s, "\033[1;32mMachine '%s' deleted\033[0m\r\n", machineName)
		return
	}

	// Get machine info
	machine, err := ss.server.getMachineByName(machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Machine '%s' not found\033[0m\r\n", machineName)
		return
	}

	fmt.Fprintf(s, "Deleting \033[1m%s\033[0m...\r\n", machineName)

	// Delete the container if it exists
	if machine.ContainerID != nil {
		ctx := context.Background()
		err = ss.server.containerManager.DeleteContainer(ctx, machine.AllocID, *machine.ContainerID)
		if err != nil {
			fmt.Fprintf(s, "\033[1;33mWarning: Failed to delete container: %v\033[0m\r\n", err)
		}
	}

	// Delete from database
	_, err = ss.server.db.Exec(`
		DELETE FROM machines
		WHERE name = ?`,
		machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError deleting machine from database: %v\033[0m\r\n", err)
		return
	}

	// Deregister from IP allocation strategy if enabled
	if ss.server.ipAllocator != nil {
		if allocErr := ss.server.ipAllocator.Deallocate(allocID, machineName); allocErr != nil {
			// Don't fail the operation if IP deallocation fails
			fmt.Fprintf(s, "\033[1;33mWarning: Failed to deregister machine from IP allocation: %v\033[0m\r\n", allocErr)
		}
	}

	fmt.Fprintf(s, "\033[1;32mMachine '%s' deleted successfully\033[0m\r\n", machineName)
}

func (ss *SSHServer) handleLogsCommand(s ssh.Session, publicKey, allocID string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify a machine name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: logs <machine-name>\r\n")
		return
	}

	machineName := args[0]
	fmt.Fprintf(s, "Fetching logs for machine '%s'...\r\n", machineName)
	fmt.Fprintf(s, "\033[1;33mNote: Logs not implemented in new server yet\033[0m\r\n")
}

func (ss *SSHServer) handleRouteCommand(s ssh.Session, publicKey, allocID string, args []string) {
	ss.server.handleRouteCommand(s, publicKey, allocID, args)
}

func (ss *SSHServer) handleHelpCommand(s ssh.Session, command string) {
	switch command {
	case "new":
		helpText := "\r\n\033[1;33mCommand: new\033[0m\r\n\r\n" +
			"Create a new machine with specified options.\r\n\r\n" +
			"\033[1mUsage:\033[0m new [options]\r\n\r\n" +
			"\033[1mOptions:\033[0m\r\n" +
			"  \033[1m--name=<name>\033[0m     Machine name (auto-generated if not specified)\r\n" +
			"  \033[1m--image=<image>\033[0m   Container image (default: exeuntu)\r\n\r\n" +
			"\033[1mExamples:\033[0m\r\n" +
			"  new                                # just give me a computer\r\n" +
			"  new --name=m --image=ubuntu:22.04  # custom image and name\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "alloc":
		allocHelpText := "\r\n\033[1;33mCommand: alloc\033[0m\r\n\r\n" +
			"Show resource allocation information.\r\n\r\n" +
			"\033[1mSubcommands:\033[0m\r\n" +
			"  \033[1malloc info\033[0m              - Show allocation usage\r\n\r\n"
		fmt.Fprint(s, allocHelpText)
	case "list", "ls":
		helpText := "\r\n\033[1;33mCommand: list (or ls)\033[0m\r\n\r\n" +
			"List all machines in your allocation.\r\n\r\n" +
			"\033[1mUsage:\033[0m list\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "ssh":
		helpText := "\r\n\033[1;33mCommand: ssh\033[0m\r\n\r\n" +
			"SSH into a machine.\r\n\r\n" +
			"\033[1mUsage:\033[0m ssh <machine-name>\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "start":
		helpText := "\r\n\033[1;33mCommand: start\033[0m\r\n\r\n" +
			"Start a stopped machine.\r\n\r\n" +
			"\033[1mUsage:\033[0m start <machine-name>\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "stop":
		helpText := "\r\n\033[1;33mCommand: stop\033[0m\r\n\r\n" +
			"Stop one or more running machines.\r\n\r\n" +
			"\033[1mUsage:\033[0m stop <machine-name> [<machine-name>...]\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "delete":
		helpText := "\r\n\033[1;33mCommand: delete\033[0m\r\n\r\n" +
			"Delete a machine permanently.\r\n\r\n" +
			"\033[1mUsage:\033[0m delete <machine-name>\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "logs":
		helpText := "\r\n\033[1;33mCommand: logs\033[0m\r\n\r\n" +
			"View logs for a machine.\r\n\r\n" +
			"\033[1mUsage:\033[0m logs <machine-name>\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "route":
		routeHelpText := "\r\n\033[1;33mCommand: route\033[0m\r\n\r\n" +
			"Manage HTTP routing rules for a machine.\r\n\r\n" +
			"\033[1mSubcommands:\033[0m\r\n" +
			"  \033[1mroute <machine> list\033[0m         - List all routes\r\n" +
			"  \033[1mroute <machine> add\033[0m          - Add a new route\r\n" +
			"  \033[1mroute <machine> delete\033[0m       - Delete a route\r\n\r\n"
		fmt.Fprint(s, routeHelpText)
	case "diag":
		helpText := "\r\n\033[1;33mCommand: diag\033[0m\r\n\r\n" +
			"Get startup diagnostics for a machine.\r\n\r\n" +
			"\033[1mUsage:\033[0m diag <machine-name>\r\n\r\n"
		fmt.Fprint(s, helpText)
	case "whoami":
		helpText := "\r\n\033[1;33mCommand: whoami\033[0m\r\n\r\n" +
			"Show your user information including email and all SSH keys.\r\n" +
			"The currently connected key is highlighted.\r\n\r\n" +
			"\033[1mUsage:\033[0m whoami\r\n\r\n"
		fmt.Fprint(s, helpText)
	default:
		fmt.Fprintf(s, "\r\n\033[1;31mNo help available for command: %s\033[0m\r\n\r\n", command)
		fmt.Fprintf(s, "Run \033[1mhelp\033[0m without arguments to see all available commands.\r\n\r\n")
	}
}

func (ss *SSHServer) handleWhoamiCommand(s ssh.Session, email string) {
	fmt.Fprintf(s, "\r\n\033[1;36mUser Information:\033[0m\r\n\r\n")
	fmt.Fprintf(s, "\033[1mEmail Address:\033[0m %s\r\n", email)

	// Get the current session's public key
	currentPublicKey, _ := s.Context().Value("public_key").(string)

	// Get all public keys for this user
	rows, err := ss.server.db.Query(`SELECT public_key, COALESCE(device_name, '') FROM ssh_keys WHERE user_id = (SELECT user_id FROM users WHERE email = ?) AND verified = 1 ORDER BY public_key`, email)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError retrieving SSH keys: %v\033[0m\r\n", err)
		return
	}
	defer rows.Close()

	fmt.Fprintf(s, "\033[1mSSH Keys:\033[0m\r\n")
	keyCount := 0
	for rows.Next() {
		var dbPublicKey, deviceName string
		if err := rows.Scan(&dbPublicKey, &deviceName); err != nil {
			continue
		}
		keyCount++

		// Check if this is the current key being used
		isCurrent := strings.TrimSpace(dbPublicKey) == strings.TrimSpace(currentPublicKey)
		currentIndicator := ""
		if isCurrent {
			currentIndicator = " \033[1;32m← current\033[0m"
		}

		fmt.Fprintf(s, "  \033[1mDevice:\033[0m %s%s\r\n", deviceName, currentIndicator)

		// Use the current session's key if this is the current key, otherwise use DB key
		displayKey := dbPublicKey
		if isCurrent && currentPublicKey != "" {
			displayKey = currentPublicKey
		}

		if displayKey != "" {
			fmt.Fprintf(s, "  \033[1mPublic Key:\033[0m %s\r\n", strings.TrimSpace(displayKey))
		} else {
			fmt.Fprintf(s, "  \033[1mPublic Key:\033[0m \033[2m(not available)\033[0m\r\n")
		}
		fmt.Fprintf(s, "\r\n")
	}

	if keyCount == 0 {
		fmt.Fprintf(s, "  \033[2mNo SSH keys found\033[0m\r\n")
	}

	fmt.Fprintf(s, "\r\n")
}

func (ss *SSHServer) handleAllocCommand(s ssh.Session, publicKey, allocID string, args []string) {
	// Show allocation info
	user, err := ss.server.getUserByPublicKey(publicKey)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Failed to get user info: %v\033[0m\r\n", err)
		return
	}

	alloc, err := ss.server.getUserAlloc(user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprintf(s, "\033[1;31mError: No allocation found\033[0m\r\n")
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;36mYour Allocation:\033[0m\r\n\r\n")
	fmt.Fprintf(s, "  ID: \033[1m%s\033[0m\r\n", alloc.AllocID)
	fmt.Fprintf(s, "  Type: \033[1m%s\033[0m\r\n", alloc.AllocType)
	fmt.Fprintf(s, "  Region: \033[1m%s\033[0m\r\n", alloc.Region)
	fmt.Fprintf(s, "  Created: %s\r\n\r\n", alloc.CreatedAt.Format("Jan 2, 2006"))
}

// getMachinesForTeam is obsolete - use getMachinesForAlloc instead
func (s *Server) getMachinesForTeam(allocID string) ([]*Machine, error) {
	// This function is kept for backward compatibility but redirects to getMachinesForAlloc
	return s.getMachinesForAlloc(allocID)
}

// startEmailVerificationNew is a version of startEmailVerification that doesn't depend on sshbuf.Channel
func (ss *SSHServer) startEmailVerificationNew(publicKey, email string) error {
	// Check if this email already exists
	var existingUserID string
	err := ss.server.db.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&existingUserID)

	if err == nil {
		// Email already exists - this is a new device for an existing user

		// Store this key as unverified in ssh_keys table
		_, err = ss.server.db.Exec(`
			INSERT OR REPLACE INTO ssh_keys (user_id, public_key, verified, device_name)
			VALUES ((SELECT user_id FROM users WHERE email = ?), ?, 0, 'Pending Verification')`,
			email, publicKey)
		if err != nil {
			return fmt.Errorf("failed to store pending key: %v", err)
		}

		// Generate token for new device verification
		token := ss.server.generateToken()
		expires := time.Now().Add(15 * time.Minute)

		_, err = ss.server.db.Exec(`
			INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
			VALUES (?, ?, ?, ?)`,
			token, publicKey, email, expires)
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

func (ss *SSHServer) handleRouteList(s ssh.Session, publicKey, teamName, machineName string) {
	// Get machine
	machine, err := ss.getMachine(publicKey, teamName, machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: %v\033[0m\r\n", err)
		return
	}

	// Get routes
	routes, err := machine.GetRoutes()
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError parsing routes: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;36mRoutes for machine '%s':\033[0m\r\n\r\n", machineName)

	if len(routes) == 0 {
		fmt.Fprintf(s, "No routes configured.\r\n")
		return
	}

	for _, route := range routes {
		methods := strings.Join(route.Methods, ",")
		ports := make([]string, len(route.Ports))
		for i, port := range route.Ports {
			ports[i] = fmt.Sprintf("%d", port)
		}
		portList := strings.Join(ports, ",")

		fmt.Fprintf(s, "  \033[1m%s\033[0m (priority %d)\r\n", route.Name, route.Priority)
		fmt.Fprintf(s, "    Methods: %s\r\n", methods)
		fmt.Fprintf(s, "    Path prefix: %s\r\n", route.Paths.Prefix)
		fmt.Fprintf(s, "    Policy: %s\r\n", route.Policy)
		fmt.Fprintf(s, "    Ports: %s\r\n\r\n", portList)
	}
}

func (ss *SSHServer) handleRouteAdd(s ssh.Session, publicKey, teamName, machineName string, args []string) {
	// Create a FlagSet for parsing
	fs := flag.NewFlagSet("route add", flag.ContinueOnError)
	var name, methodsStr, prefix, policy, portsStr string
	var priority int

	fs.StringVar(&name, "name", "", "route name (auto-generated if not specified)")
	fs.IntVar(&priority, "priority", -1, "priority (lower = higher priority, defaults to lowest priority)")
	fs.StringVar(&methodsStr, "methods", "*", "HTTP methods (comma-separated, or '*' for all)")
	fs.StringVar(&prefix, "prefix", "/", "path prefix to match")
	fs.StringVar(&policy, "policy", "private", "'public' or 'private'")
	fs.StringVar(&portsStr, "ports", "80,8000,8080,8888", "allowed ports (comma-separated)")

	// Capture the output to avoid printing errors to the session
	var buf bytes.Buffer
	fs.SetOutput(&buf)

	// Parse the flags
	err := fs.Parse(args)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError parsing flags: %v\033[0m\r\n", err)
		return
	}

	// Generate name if not provided
	if name == "" {
		name = ss.server.generateRandomRouteName()
	}

	// Get machine
	machine, err := ss.getMachine(publicKey, teamName, machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: %v\033[0m\r\n", err)
		return
	}

	// Get existing routes
	routes, err := machine.GetRoutes()
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError parsing routes: %v\033[0m\r\n", err)
		return
	}

	// Set default priority if not specified
	if priority == -1 {
		// Find the highest priority number and set this route to be lower priority (higher number)
		maxPriority := 0
		for _, route := range routes {
			if route.Priority > maxPriority {
				maxPriority = route.Priority
			}
		}
		priority = maxPriority + 10 // Add some gap
	}

	// Parse methods
	var methods []string
	if methodsStr == "*" {
		methods = []string{"*"}
	} else {
		methods = strings.Split(methodsStr, ",")
		for i, method := range methods {
			methods[i] = strings.TrimSpace(strings.ToUpper(method))
		}
	}

	// Parse ports
	portStrs := strings.Split(portsStr, ",")
	var ports []int
	for _, portStr := range portStrs {
		portStr = strings.TrimSpace(portStr)
		port, err := strconv.Atoi(portStr)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError: Invalid port '%s': %v\033[0m\r\n", portStr, err)
			return
		}
		ports = append(ports, port)
	}

	// Validate policy
	if policy != "public" && policy != "private" {
		fmt.Fprintf(s, "\033[1;31mError: Policy must be 'public' or 'private'\033[0m\r\n")
		return
	}

	// Check for duplicate name or priority
	for _, route := range routes {
		if route.Name == name {
			fmt.Fprintf(s, "\033[1;31mError: Route with name '%s' already exists\033[0m\r\n", name)
			return
		}
		if route.Priority == priority {
			fmt.Fprintf(s, "\033[1;31mError: Route with priority %d already exists\033[0m\r\n", priority)
			return
		}
	}

	// Create new route
	newRoute := Route{
		Name:     name,
		Priority: priority,
		Methods:  methods,
		Paths:    PathMatcher{Prefix: prefix},
		Policy:   policy,
		Ports:    ports,
	}

	// Add to routes list
	routes = append(routes, newRoute)

	// Set routes back on machine
	err = machine.SetRoutes(routes)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError encoding routes: %v\033[0m\r\n", err)
		return
	}

	// Update database
	_, err = ss.server.db.Exec(`
		UPDATE machines SET routes = ? 
		WHERE name = ?`,
		*machine.Routes, machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError saving route: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\033[1;32mRoute '%s' added successfully\033[0m\r\n", name)
}

func (ss *SSHServer) handleRouteRemove(s ssh.Session, publicKey, teamName, machineName string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify route name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: route %s remove <name>\r\n", machineName)
		return
	}

	routeName := args[0]

	// Get machine
	machine, err := ss.getMachine(publicKey, teamName, machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: %v\033[0m\r\n", err)
		return
	}

	// Get existing routes
	routes, err := machine.GetRoutes()
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError parsing routes: %v\033[0m\r\n", err)
		return
	}

	// Find and remove the route
	var newRoutes MachineRoutes
	found := false
	for _, route := range routes {
		if route.Name == routeName {
			found = true
			continue // Skip this route (delete it)
		}
		newRoutes = append(newRoutes, route)
	}

	if !found {
		fmt.Fprintf(s, "\033[1;31mError: Route '%s' not found\033[0m\r\n", routeName)
		return
	}

	// Set routes back on machine
	err = machine.SetRoutes(newRoutes)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError encoding routes: %v\033[0m\r\n", err)
		return
	}

	// Update database
	_, err = ss.server.db.Exec(`
		UPDATE machines SET routes = ? 
		WHERE name = ?`,
		*machine.Routes, machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError saving routes: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\033[1;32mRoute '%s' deleted successfully\033[0m\r\n", routeName)
}

// getMachine retrieves a machine for the given user/team/name
func (ss *SSHServer) getMachine(publicKey, allocID, machineName string) (*Machine, error) {
	// First verify user has access to the alloc
	var exists bool
	err := ss.server.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM allocs a
			JOIN users u ON a.user_id = u.user_id
			JOIN ssh_keys sk ON u.user_id = sk.user_id
			WHERE sk.public_key = ? AND sk.verified = 1 AND a.alloc_id = ?
		)`, publicKey, allocID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("database error: %v", err)
	}
	if !exists {
		return nil, fmt.Errorf("access denied to allocation '%s'", allocID)
	}

	// Get the machine
	var machine Machine
	err = ss.server.db.QueryRow(`
		SELECT id, alloc_id, name, status, image, container_id, 
		       created_by_user_id, created_at, updated_at, 
		       last_started_at, docker_host, routes
		FROM machines 
		WHERE name = ? AND alloc_id = ?`, machineName, allocID).Scan(
		&machine.ID, &machine.AllocID, &machine.Name, &machine.Status,
		&machine.Image, &machine.ContainerID, &machine.CreatedByUserID,
		&machine.CreatedAt, &machine.UpdatedAt, &machine.LastStartedAt,
		&machine.DockerHost, &machine.Routes)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("machine '%s' not found", machineName)
		}
		return nil, fmt.Errorf("database error: %v", err)
	}

	return &machine, nil
}
