package exe

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"exe.dev/container"
	"exe.dev/termfun"
	"github.com/anmitsu/go-shlex"
	"github.com/gliderlabs/ssh"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/customer"
	"github.com/stripe/stripe-go/v76/paymentmethod"
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
	// username := s.User()

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
		"\033[1mbilling\033[0m                 - Manage billing and payment info\r\n" +
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

		parts, err := shlex.Split(strings.TrimSpace(line), true)
		if err != nil {
			fmt.Fprintf(s, "Error parsing command: %v\r\n", err)
			continue
		}
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
			ss.handleListCommand(s, allocID)
		case "new":
			ss.handleNewCommand(s, publicKey, allocID, args)
		case "start":
			ss.handleStartCommand(s, allocID, args)
		case "stop":
			ss.handleStopCommand(s, args)
		case "delete":
			ss.handleDeleteCommand(s, allocID, args)
		case "logs":
			ss.handleLogsCommand(s, publicKey, allocID, args)
		case "route":
			ss.handleRouteCommand(s, publicKey, allocID, args)
		case "alloc":
			ss.handleAllocCommand(s, publicKey, allocID, args)
		case "billing":
			ss.handleBillingCommand(s, publicKey, args)
		case "whoami":
			ss.handleWhoamiCommand(s, email)
		default:
			fmt.Fprint(s, "Unknown command. Type 'help' for available commands.\r\n")
		}
	}
}

// showAnimatedWelcome displays the ASCII art with a beautiful fade-out animation
func (ss *SSHServer) showAnimatedWelcome(s ssh.Session) {
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
	user, userErr := ss.server.getUserByPublicKey(publicKey)
	if userErr != nil || user == nil {
		log.Printf("Error: User not found after verification: %v", userErr)
		fmt.Fprintf(s, "Error loading user profile. Please try registering again.\r\n")
		return
	}

	// Store/update the SSH key as verified
	_, storeErr := ss.server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key)
		VALUES (?, ?)
		ON CONFLICT(public_key) DO UPDATE SET user_id = ?`,
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
func (ss *SSHServer) handleExec(s ssh.Session, cmd []string, publicKey string, registered bool) {
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
		ss.handleListCommand(s, alloc.AllocID)
	case "start":
		ss.handleStartCommand(s, alloc.AllocID, args)
	case "stop":
		ss.handleStopCommand(s, args)
	case "delete":
		ss.handleDeleteCommand(s, alloc.AllocID, args)
	case "logs":
		ss.handleLogsCommand(s, publicKey, alloc.AllocID, args)
	case "diag", "diagnostics":
		fmt.Fprintf(s, "\033[1;33mDiagnostics not implemented in new server yet\033[0m\r\n")
	case "route":
		ss.handleRouteCommand(s, publicKey, alloc.AllocID, args)
	case "alloc":
		ss.handleAllocCommand(s, publicKey, alloc.AllocID, args)
	case "billing":
		ss.handleBillingCommand(s, publicKey, args)
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
				"\033[1mbilling\033[0m                 - Manage billing and payment info\r\n" +
				"\033[1mwhoami\033[0m                  - Show your email and SSH keys\r\n" +
				"\033[1m?\033[0m                       - Show this help\r\n\r\n" +
				"Run \033[1mhelp <command>\033[0m for more details\r\n\r\n"
			fmt.Fprint(s, helpText)
		}
	default:
		fmt.Fprintf(s, "Unknown command: %s\r\nRun 'ssh exe.dev help' for available commands.\r\n", command)
	}
}

/*
const helpText = "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
	"\033[1mlist\033[0m                    - List your machines\r\n" +
	"\033[1mnew [args]\033[0m              - Create a new machine\r\n" +
	"\033[1mstart <name>\033[0m            - Start a machine\r\n" +
	"\033[1mstop <name> [...]\033[0m       - Stop one or more machines\r\n" +
	"\033[1mdelete <name>\033[0m           - Delete a machine\r\n" +
	"\033[1mlogs <name>\033[0m             - View machine logs\r\n" +
	"\033[1mdiag <name>\033[0m             - Get machine startup diagnostics\r\n" +
	"\033[1mroute <machine>\033[0m         - Manage machine routes\r\n" +
	"\033[1malloc\033[0m                   - Resource allocation info\r\n" +
	"\033[1mbilling\033[0m                 - Manage billing and payment info\r\n" +
	"\033[1mwhoami\033[0m                  - Show your email and SSH keys\r\n" +
	"\033[1m?\033[0m                       - Show this help\r\n\r\n" +
	"Run \033[1mhelp <command>\033[0m for more details\r\n\r\n"

const helpText2 = "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
	"\033[1mlist\033[0m                    - List your machines\r\n" +
	"\033[1mnew [args]\033[0m              - Create a new machine\r\n" +
	"\033[1mstart <name>\033[0m            - Start a machine\r\n" +
	"\033[1mstop <name> [...]\033[0m       - Stop one or more machines\r\n" +
	"\033[1mdelete <name>\033[0m           - Delete a machine\r\n" +
	"\033[1mlogs <name>\033[0m             - View machine logs\r\n" +
	"\033[1mroute <machine>\033[0m         - Manage machine routes\r\n" +
	"\033[1mbilling\033[0m                 - Manage billing and payment info\r\n" +
	"\033[1mwhoami\033[0m                  - Show your email and SSH keys\r\n" +
	"\033[1m?\033[0m                       - Show this help\r\n" +
	"\033[1mexit\033[0m                    - Exit\r\n\r\n" +
	"Run \033[1mhelp <command>\033[0m for more details\r\n\r\n"
*/

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
func (ss *SSHServer) handleListCommand(s ssh.Session, allocID string) {
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
			switch c.Status {
			case container.StatusRunning:
				statusColor = "\033[1;32m" // green
				status = "running"
			case container.StatusStopped:
				statusColor = "\033[1;31m" // red
				status = "stopped"
			case container.StatusPending:
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
		statusColors := map[string]string{
			"running": "\033[1;32m",
			"stopped": "\033[1;31m",
			"pending": "\033[1;33m",
		}
		fmt.Fprintf(s, "  • \033[1m%s\033[0m - %s%s\033[0m", m.Name, statusColors[status], status)

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
	var command string

	fs.StringVar(&machineName, "name", "", "machine name (auto-generated if not specified)")
	fs.StringVar(&image, "image", "exeuntu", "container image")
	fs.StringVar(&size, "size", "medium", "machine size (small, medium, or large)")
	fs.StringVar(&command, "command", "auto", "container command: auto, none, or a custom command")

	// Capture the output to avoid printing errors to the session
	var buf bytes.Buffer
	fs.SetOutput(&buf)

	// Parse the flags
	parseErr := fs.Parse(args)
	if parseErr != nil {
		fmt.Fprintf(s, "\033[1;31mError: %v\033[0m\r\n", parseErr)
		fmt.Fprintf(s, "Usage: new [--name=<name>] [--image=<image>] [--size=<size>] [--command=<auto|none|command>]\r\n")
		return
	}

	// Check for non-flag arguments - not supported
	if fs.NArg() > 0 {
		fmt.Fprintf(s, "\033[1;31mError: Unexpected arguments: %s\033[0m\r\n", strings.Join(fs.Args(), " "))
		fmt.Fprintf(s, "Usage: new [--name=<name>] [--image=<image>] [--size=<size>] [--command=<auto|none|command>]\r\n")
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
		fmt.Fprintf(s, "\033[1;31mError: Invalid machine name '%s'. Machine names must be at least 5 characters, lowercase, start with a letter, contain only letters, numbers and hyphens (no consecutive hyphens), not use common computer terms, and be up to 32 characters\033[0m\r\n", machineName)
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
		AllocID:         allocID,
		Name:            machineName,
		Image:           image,
		Size:            size,
		CPURequest:      sizePreset.CPURequest,
		MemoryRequest:   sizePreset.MemoryRequest,
		StorageSize:     sizePreset.StorageSize,
		Ephemeral:       false,
		CommandOverride: command,
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

func (ss *SSHServer) handleStartCommand(s ssh.Session, allocID string, args []string) {
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

func (ss *SSHServer) handleStopCommand(s ssh.Session, args []string) {
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

func (ss *SSHServer) handleDeleteCommand(s ssh.Session, allocID string, args []string) {
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
	case "billing":
		helpText := "\r\n\033[1;33mCommand: billing\033[0m\r\n\r\n" +
			"Manage your billing information and payment methods.\r\n\r\n" +
			"If you haven't set up billing yet, this command will guide you through\r\n" +
			"the setup process including payment method verification.\r\n\r\n" +
			"If billing is already configured, you can view, update, or delete\r\n" +
			"your billing information.\r\n\r\n" +
			"\033[1mUsage:\033[0m billing\r\n\r\n"
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
	rows, err := ss.server.db.Query(`SELECT public_key FROM ssh_keys WHERE user_id = (SELECT user_id FROM users WHERE email = ?) ORDER BY public_key`, email)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError retrieving SSH keys: %v\033[0m\r\n", err)
		return
	}
	defer rows.Close()

	fmt.Fprintf(s, "\033[1mSSH Keys:\033[0m\r\n")
	keyCount := 0
	for rows.Next() {
		var dbPublicKey string
		if err := rows.Scan(&dbPublicKey); err != nil {
			continue
		}
		keyCount++

		// Check if this is the current key being used
		isCurrent := strings.TrimSpace(dbPublicKey) == strings.TrimSpace(currentPublicKey)
		currentIndicator := ""
		if isCurrent {
			currentIndicator = " \033[1;32m← current\033[0m"
		}

		// Use the current session's key if this is the current key, otherwise use DB key
		displayKey := dbPublicKey
		if isCurrent && currentPublicKey != "" {
			displayKey = currentPublicKey
		}

		if displayKey != "" {
			fmt.Fprintf(s, "  \033[1mPublic Key:\033[0m %s%s\r\n", strings.TrimSpace(displayKey), currentIndicator)
		} else {
			fmt.Fprintf(s, "  \033[1mPublic Key:\033[0m \033[2m(not available)\033[0m%s\r\n", currentIndicator)
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

func (ss *SSHServer) handleBillingCommand(s ssh.Session, publicKey string, args []string) {
	// Get user info
	user, err := ss.server.getUserByPublicKey(publicKey)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Failed to get user info: %v\033[0m\r\n", err)
		return
	}

	// Get allocation info
	alloc, err := ss.server.getUserAlloc(user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprintf(s, "\033[1;31mError: No allocation found\033[0m\r\n")
		return
	}

	// Check if billing info already exists
	hasBilling := alloc.StripeCustomerID.Valid && alloc.StripeCustomerID.String != ""

	if hasBilling {
		ss.showBillingInfo(s, alloc)
	} else {
		ss.setupBilling(s, alloc, user.Email)
	}
}

// startEmailVerificationNew is a version of startEmailVerification that doesn't depend on sshbuf.Channel
func (ss *SSHServer) startEmailVerificationNew(publicKey, email string) error {
	// Check if this email already exists
	var existingUserID string
	err := ss.server.db.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&existingUserID)

	if err == nil {
		// Email already exists - this is a new ssh key for an existing user

		// Don't store in ssh_keys yet - only store verified keys there

		// Generate token for new ssh key verification
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

func (ss *SSHServer) showBillingInfo(s ssh.Session, alloc *Alloc) {
	fmt.Fprintf(s, "\r\n\033[1;36mBilling Information:\033[0m\r\n\r\n")

	// Show current billing info
	if alloc.BillingEmail.Valid {
		fmt.Fprintf(s, "  Email: \033[1m%s\033[0m\r\n", alloc.BillingEmail.String)
	}
	if alloc.StripeCustomerID.Valid {
		fmt.Fprintf(s, "  Stripe Customer ID: \033[1m%s\033[0m\r\n", alloc.StripeCustomerID.String)
	}
	fmt.Fprintf(s, "  Status: \033[1;32mConfigured\033[0m\r\n\r\n")

	// Show options
	fmt.Fprintf(s, "\033[1mOptions:\033[0m\r\n")
	fmt.Fprintf(s, "  1. Update payment method\r\n")
	fmt.Fprintf(s, "  2. Update billing email\r\n")
	fmt.Fprintf(s, "  3. Delete billing info\r\n")
	fmt.Fprintf(s, "  4. Back to main menu\r\n\r\n")

	// Get user choice
	terminal := term.NewTerminal(s, "Choose an option (1-4): ")
	for {
		choice, err := terminal.ReadLine()
		if err != nil {
			fmt.Fprintf(s, "\r\nExiting billing menu.\r\n")
			return
		}

		switch strings.TrimSpace(choice) {
		case "1":
			ss.updatePaymentMethod(s, alloc)
			return
		case "2":
			ss.updateBillingEmail(s, alloc)
			return
		case "3":
			ss.deleteBillingInfo(s, alloc)
			return
		case "4":
			fmt.Fprintf(s, "\r\nReturning to main menu.\r\n")
			return
		default:
			fmt.Fprintf(s, "\r\nInvalid choice. Please enter 1-4: ")
			terminal.SetPrompt("Choose an option (1-4): ")
		}
	}
}

func (ss *SSHServer) setupBilling(s ssh.Session, alloc *Alloc, userEmail string) {
	fmt.Fprintf(s, "\r\n\033[1;33mBilling Setup\033[0m\r\n\r\n")
	fmt.Fprintf(s, "You need to set up billing information to continue using exe.dev.\r\n\r\n")

	terminal := term.NewTerminal(s, "")

	// Get billing email
	terminal.SetPrompt("Billing email (press Enter to use " + userEmail + "): ")
	billingEmail, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	if strings.TrimSpace(billingEmail) == "" {
		billingEmail = userEmail
	}

	// Validate email
	if !ss.server.isValidEmail(billingEmail) {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid email format.\033[0m\r\n")
		return
	}

	// Get credit card info
	fmt.Fprintf(s, "\r\nNow we need to verify your payment method.\r\n")
	fmt.Fprintf(s, "Please enter a credit card number to verify your payment method.\r\n")
	fmt.Fprintf(s, "For testing, you can use: \033[1m4242424242424242\033[0m (Visa test card)\r\n\r\n")

	terminal.SetPrompt("Credit card number: ")
	cardNumber, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	cardNumber = strings.ReplaceAll(strings.TrimSpace(cardNumber), " ", "")
	if len(cardNumber) < 13 {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid card number.\033[0m\r\n")
		return
	}

	// Get expiry month
	terminal.SetPrompt("Expiry month (MM): ")
	expMonth, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	// Get expiry year
	terminal.SetPrompt("Expiry year (YYYY): ")
	expYear, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	// Get CVC
	terminal.SetPrompt("CVC: ")
	cvc, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	// Create Stripe customer and payment method
	fmt.Fprintf(s, "\r\nProcessing payment method...\r\n")

	customerID, err := ss.createStripeCustomer(billingEmail, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError setting up billing: %v\033[0m\r\n", err)
		return
	}

	// Update allocation with billing info
	err = ss.updateAllocBilling(alloc.AllocID, customerID, billingEmail)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError saving billing info: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing setup completed successfully!\033[0m\r\n")
	fmt.Fprintf(s, "Your payment method has been verified and saved.\r\n\r\n")
}

func (ss *SSHServer) updatePaymentMethod(s ssh.Session, alloc *Alloc) {
	fmt.Fprintf(s, "\r\n\033[1;33mUpdate Payment Method\033[0m\r\n\r\n")

	terminal := term.NewTerminal(s, "")

	// Get new credit card info
	fmt.Fprintf(s, "Please enter your new payment method details.\r\n")
	fmt.Fprintf(s, "For testing, you can use: \033[1m4242424242424242\033[0m (Visa test card)\r\n\r\n")

	terminal.SetPrompt("Credit card number: ")
	cardNumber, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	cardNumber = strings.ReplaceAll(strings.TrimSpace(cardNumber), " ", "")
	if len(cardNumber) < 13 {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid card number.\033[0m\r\n")
		return
	}

	// Get expiry details
	terminal.SetPrompt("Expiry month (MM): ")
	expMonth, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	terminal.SetPrompt("Expiry year (YYYY): ")
	expYear, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	terminal.SetPrompt("CVC: ")
	cvc, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	fmt.Fprintf(s, "\r\nUpdating payment method...\r\n")

	// Update the payment method in Stripe
	err = ss.updateStripePaymentMethod(alloc.StripeCustomerID.String, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError updating payment method: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Payment method updated successfully!\033[0m\r\n\r\n")
}

func (ss *SSHServer) updateBillingEmail(s ssh.Session, alloc *Alloc) {
	fmt.Fprintf(s, "\r\n\033[1;33mUpdate Billing Email\033[0m\r\n\r\n")

	terminal := term.NewTerminal(s, "")
	terminal.SetPrompt("New billing email: ")

	newEmail, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	newEmail = strings.TrimSpace(newEmail)
	if !ss.server.isValidEmail(newEmail) {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid email format.\033[0m\r\n")
		return
	}

	fmt.Fprintf(s, "\r\nUpdating billing email...\r\n")

	// Update in database
	_, err = ss.server.db.Exec("UPDATE allocs SET billing_email = ? WHERE alloc_id = ?", newEmail, alloc.AllocID)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError updating billing email: %v\033[0m\r\n", err)
		return
	}

	// Update in Stripe
	if alloc.StripeCustomerID.Valid {
		err = ss.updateStripeCustomerEmail(alloc.StripeCustomerID.String, newEmail)
		if err != nil {
			fmt.Fprintf(s, "\r\n\033[1;33mWarning: Database updated but Stripe update failed: %v\033[0m\r\n", err)
		} else {
			fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing email updated successfully!\033[0m\r\n\r\n")
		}
	} else {
		fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing email updated successfully!\033[0m\r\n\r\n")
	}
}

func (ss *SSHServer) deleteBillingInfo(s ssh.Session, alloc *Alloc) {
	fmt.Fprintf(s, "\r\n\033[1;33mDelete Billing Information\033[0m\r\n\r\n")
	fmt.Fprintf(s, "\033[1;31mWarning: This will remove all billing information from your account.\033[0m\r\n")
	fmt.Fprintf(s, "You will need to set up billing again to continue using exe.dev.\r\n\r\n")

	terminal := term.NewTerminal(s, "")
	terminal.SetPrompt("Are you sure? Type 'yes' to confirm: ")

	confirmation, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nOperation cancelled.\r\n")
		return
	}

	if strings.ToLower(strings.TrimSpace(confirmation)) != "yes" {
		fmt.Fprintf(s, "\r\nOperation cancelled.\r\n")
		return
	}

	fmt.Fprintf(s, "\r\nDeleting billing information...\r\n")

	// Clear billing info in database
	_, err = ss.server.db.Exec("UPDATE allocs SET stripe_customer_id = NULL, billing_email = NULL WHERE alloc_id = ?", alloc.AllocID)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError deleting billing info: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing information deleted successfully!\033[0m\r\n")
	fmt.Fprintf(s, "You can set up billing again using the 'billing' command.\r\n\r\n")
}

// Stripe integration helper functions
func (ss *SSHServer) createStripeCustomer(email, cardNumber, expMonth, expYear, cvc string) (string, error) {
	// Convert expiry month and year to int64
	expMonthInt, err := strconv.ParseInt(expMonth, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiry month: %v", err)
	}
	expYearInt, err := strconv.ParseInt(expYear, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiry year: %v", err)
	}

	// Create payment method
	pmParams := &stripe.PaymentMethodParams{
		Type: stripe.String("card"),
		Card: &stripe.PaymentMethodCardParams{
			Number:   stripe.String(cardNumber),
			ExpMonth: stripe.Int64(expMonthInt),
			ExpYear:  stripe.Int64(expYearInt),
			CVC:      stripe.String(cvc),
		},
	}

	pm, err := paymentmethod.New(pmParams)
	if err != nil {
		return "", fmt.Errorf("failed to create payment method: %v", err)
	}

	// Create customer
	customerParams := &stripe.CustomerParams{
		Email:         stripe.String(email),
		PaymentMethod: stripe.String(pm.ID),
		InvoiceSettings: &stripe.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(pm.ID),
		},
	}

	cust, err := customer.New(customerParams)
	if err != nil {
		return "", fmt.Errorf("failed to create customer: %v", err)
	}

	// Attach payment method to customer
	pmAttachParams := &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(cust.ID),
	}
	_, err = paymentmethod.Attach(pm.ID, pmAttachParams)
	if err != nil {
		return "", fmt.Errorf("failed to attach payment method: %v", err)
	}

	return cust.ID, nil
}

func (ss *SSHServer) updateStripePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error {
	// Convert expiry month and year to int64
	expMonthInt, err := strconv.ParseInt(expMonth, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiry month: %v", err)
	}
	expYearInt, err := strconv.ParseInt(expYear, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiry year: %v", err)
	}

	// Create new payment method
	pmParams := &stripe.PaymentMethodParams{
		Type: stripe.String("card"),
		Card: &stripe.PaymentMethodCardParams{
			Number:   stripe.String(cardNumber),
			ExpMonth: stripe.Int64(expMonthInt),
			ExpYear:  stripe.Int64(expYearInt),
			CVC:      stripe.String(cvc),
		},
	}

	pm, err := paymentmethod.New(pmParams)
	if err != nil {
		return fmt.Errorf("failed to create payment method: %v", err)
	}

	// Attach to customer
	pmAttachParams := &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(customerID),
	}
	_, err = paymentmethod.Attach(pm.ID, pmAttachParams)
	if err != nil {
		return fmt.Errorf("failed to attach payment method: %v", err)
	}

	// Update customer default payment method
	customerUpdateParams := &stripe.CustomerParams{
		InvoiceSettings: &stripe.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(pm.ID),
		},
	}
	_, err = customer.Update(customerID, customerUpdateParams)
	if err != nil {
		return fmt.Errorf("failed to update customer default payment method: %v", err)
	}

	return nil
}

func (ss *SSHServer) updateStripeCustomerEmail(customerID, email string) error {
	customerUpdateParams := &stripe.CustomerParams{
		Email: stripe.String(email),
	}
	_, err := customer.Update(customerID, customerUpdateParams)
	if err != nil {
		return fmt.Errorf("failed to update customer email: %v", err)
	}
	return nil
}

func (ss *SSHServer) updateAllocBilling(allocID, customerID, billingEmail string) error {
	_, err := ss.server.db.Exec(
		"UPDATE allocs SET stripe_customer_id = ?, billing_email = ? WHERE alloc_id = ?",
		customerID, billingEmail, allocID,
	)
	return err
}
