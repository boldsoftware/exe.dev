package exe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"exe.dev/container"
	"exe.dev/sshproxy"
	"exe.dev/termfun"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
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
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": ss.handleSFTP,
		},
		RequestHandlers: map[string]ssh.RequestHandler{
			"tcpip-forward":        ss.handlePortForward,
			"cancel-tcpip-forward": ss.handleCancelPortForward,
		},
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
	// Convert gliderlabs public key to golang.org/x/crypto/ssh public key for compatibility
	goKey, err := gossh.ParsePublicKey(key.Marshal())
	if err != nil {
		log.Printf("Failed to parse public key: %v", err)
		return false
	}

	// Use existing authentication logic
	perms, err := ss.server.authenticatePublicKey(nil, goKey)
	if err != nil {
		log.Printf("Authentication failed: %v", err)
		return false
	}

	// Store permissions in context for later use
	ctx.SetValue("fingerprint", perms.Extensions["fingerprint"])
	ctx.SetValue("registered", perms.Extensions["registered"])
	ctx.SetValue("email", perms.Extensions["email"])
	ctx.SetValue("public_key", perms.Extensions["public_key"])

	return true
}

// handleSession handles SSH sessions
func (ss *SSHServer) handleSession(s ssh.Session) {
	defer s.Close()

	// Get authentication info from context
	fingerprint, _ := s.Context().Value("fingerprint").(string)
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

	// Check if this is a direct machine access attempt
	if username != "" && registered && ss.server.containerManager != nil {
		if machine := ss.server.findMachineByNameForUser(fingerprint, username); machine != nil {
			// This is a direct machine connection
			ss.handleMachineSSH(s, machine, fingerprint)
			return
		}
	}

	// Check for exec command
	cmd := s.Command()
	if len(cmd) > 0 {
		// Handle exec commands
		ss.handleExec(s, cmd, username, fingerprint, registered)
		return
	}

	// Handle interactive shell session
	ss.handleShell(s, username, fingerprint, registered, terminalWidth, terminalHeight)
}

// handleShell handles interactive shell sessions with readline
func (ss *SSHServer) handleShell(s ssh.Session, username, fingerprint string, registered bool, terminalWidth, terminalHeight int) {
	if !registered {
		// Get public key from context for registration
		publicKey, _ := s.Context().Value("public_key").(string)
		// Handle registration flow
		ss.handleRegistration(s, fingerprint, publicKey, terminalWidth)
		return
	}

	// Create user session for registered users
	user, err := ss.server.getUserByFingerprint(fingerprint)
	if err != nil {
		fmt.Fprintf(s, "Error retrieving user info: %v\r\n", err)
		return
	}
	if user == nil {
		fmt.Fprintf(s, "Error: User not found\r\n")
		return
	}

	teams, err := ss.server.getUserTeams(fingerprint)
	if err != nil || len(teams) == 0 {
		fmt.Fprintf(s, "Error: User not associated with any team\r\n")
		return
	}

	// Get the default team for this SSH key
	defaultTeam, err := ss.server.getDefaultTeamForKey(fingerprint)
	if err != nil || defaultTeam == "" {
		defaultTeam = teams[0].TeamName
	}

	// Find the team membership details
	var team TeamMember
	for _, t := range teams {
		if t.TeamName == defaultTeam {
			team = t
			break
		}
	}
	if team.TeamName == "" {
		team = teams[0]
	}

	// Check if username is a container name for direct access
	if username != "" && ss.server.containerManager != nil {
		if container := ss.server.findContainerByName(fingerprint, username); container != nil {
			// For container connections, we need to use a different approach
			// since the existing methods expect sshbuf.Channel
			// TODO: Implement proper container connection for new SSH server
			fmt.Fprintf(s, "Container connection not yet implemented in new SSH server\r\n")
			return
		}
	}

	// Run the main shell with readline
	ss.runMainShellWithReadline(s, fingerprint, user.Email, team.TeamName, team.IsAdmin, false)
}

// runMainShellWithReadline implements the main menu using a simple line reader
func (ss *SSHServer) runMainShellWithReadline(s ssh.Session, fingerprint, email, teamName string, isAdmin bool, showWelcome bool) {
	if !ss.server.testMode {
		log.Printf("runMainShellWithReadline called - email: %s, showWelcome: %v", email, showWelcome)
	}

	helpText := "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
		"\033[1;36mMachine Management:\033[0m\r\n" +
		"\033[1mlist\033[0m                    - List your machines\r\n" +
		"\033[1mcreate [image] [name]\033[0m   - Create a new machine (defaults: ubuntu, auto-generated name)\r\n" +
		"\033[1mssh <name>\033[0m              - SSH into a machine\r\n" +
		"\033[1mstart <name>\033[0m            - Start a machine\r\n" +
		"\033[1mstop <name> [...]\033[0m       - Stop one or more machines\r\n" +
		"\033[1mdelete <name>\033[0m           - Delete a machine\r\n" +
		"\033[1mlogs <name>\033[0m             - View machine logs\r\n\r\n" +
		"\033[1;36mTeam Management:\033[0m\r\n" +
		"\033[1mteam\033[0m                    - List team members\r\n" +
		"\033[1mteam invite <email>\033[0m     - Invite someone to your team\r\n" +
		"\033[1mteam join <code>\033[0m        - Join a team with an invite code\r\n\r\n" +
		"\033[1mhelp\033[0m or \033[1m?\033[0m              - Show this help\r\n" +
		"\033[1mexit\033[0m                   - Exit\r\n\r\n"

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
			fmt.Fprint(s, helpText)
		case "list", "ls":
			ss.handleListCommand(s, fingerprint, teamName)
		case "create":
			ss.handleCreateCommand(s, fingerprint, teamName, args)
		case "ssh":
			ss.handleSSHCommandMenu(s, fingerprint, teamName, args)
		case "start":
			ss.handleStartCommand(s, fingerprint, teamName, args)
		case "stop":
			ss.handleStopCommand(s, fingerprint, teamName, args)
		case "delete":
			ss.handleDeleteCommand(s, fingerprint, teamName, args)
		case "logs":
			ss.handleLogsCommand(s, fingerprint, teamName, args)
		case "team":
			ss.handleTeamCommand(s, fingerprint, teamName, args)
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
	from := termfun.RGB{80, 255, 120}
	to := bg

	// Animate with proper 24-bit colors
	termfun.FadeTextInPlace(s, asciiArt, leftPadding, from, to, 900*time.Millisecond, 12)

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
	var line []byte
	buf := make([]byte, 1)

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
func (ss *SSHServer) handleRegistration(s ssh.Session, fingerprint, publicKey string, terminalWidth int) {
	// Show the animated welcome first
	ss.showAnimatedWelcome(s, terminalWidth)

	// For the new SSH server, we'll use default colors
	// Terminal detection would require implementing the OSC query which is complex
	grayText := "\033[2m" // Default gray text

	// Show the signup content after the animation
	signupContent := "\r\n\033[1;33mEXE.DEV: get a machine over ssh\033[0m\r\n" +
		"Signup involves verifying your email, picking a team name and setting up billing.\r\n\r\n" +
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

	// Ask for team name BEFORE email verification
	var teamName string
	for {
		fmt.Fprint(s, "\033[1mChoose a team name:\033[0m ")
		teamName = ss.readLineWithEcho(s)
		if teamName == "" {
			fmt.Fprint(s, "\r\nRegistration cancelled.\r\n")
			return
		}

		// Validate team name format
		if !ss.server.isValidTeamName(teamName) {
			fmt.Fprintf(s, "\r\n%sInvalid team name. Team names can only contain lowercase letters, numbers, and hyphens.%s\r\n", "\033[1;31m", "\033[0m")
			continue
		}

		// Check if team name is taken
		taken, err := ss.server.isTeamNameTakenOrReserved(teamName)
		if err != nil {
			fmt.Fprintf(s, "\r\n%sError checking team name availability: %v%s\r\n", "\033[1;31m", err, "\033[0m")
			continue
		}
		if taken {
			fmt.Fprintf(s, "\r\n%sTeam name '%s' is already taken. Please choose a different name.%s\r\n", "\033[1;31m", teamName, "\033[0m")
			continue
		}

		break
	}

	// Log for debugging
	if !ss.server.testMode && !ss.server.quietMode {
		log.Printf("Starting email verification for %s with team %s", email, teamName)
	}

	// Start email verification directly without using sshbuf.Channel
	if err := ss.startEmailVerificationNew(fingerprint, email, publicKey, teamName); err != nil {
		// Log the error for debugging
		log.Printf("Email verification failed for %s (fingerprint: %s): %v", email, fingerprint, err)
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
	verification, exists := ss.server.getEmailVerification(fingerprint)
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
	user, err := ss.server.getUserByFingerprint(fingerprint)
	if err != nil || user == nil {
		log.Printf("Error: User not found after verification for fingerprint %s: %v", fingerprint, err)
		fmt.Fprintf(s, "Error loading user profile. Please try registering again.\r\n")
		return
	}

	// Store/update the SSH key as verified
	_, err = ss.server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
		VALUES (?, ?, ?, 1, 'Primary Device')
		ON CONFLICT(fingerprint) DO UPDATE SET verified = 1, public_key = ?, user_email = ?`,
		fingerprint, user.Email, publicKey, publicKey, user.Email)
	if err != nil {
		log.Printf("Error storing SSH key: %v", err)
		// Don't fail here, the key might already exist
	}

	// Set the default team for the SSH key if not already set
	var currentDefaultTeam string
	err = ss.server.db.QueryRow(`
		SELECT default_team FROM ssh_keys WHERE fingerprint = ?`,
		fingerprint).Scan(&currentDefaultTeam)
	if err != nil || currentDefaultTeam == "" {
		// Get the personal team name
		var personalTeamName string
		err = ss.server.db.QueryRow(`
			SELECT name FROM teams
			WHERE owner_fingerprint = ? AND is_personal = TRUE`,
			fingerprint).Scan(&personalTeamName)
		if err == nil && personalTeamName != "" {
			ss.server.db.Exec(`
				UPDATE ssh_keys SET default_team = ? WHERE fingerprint = ?`,
				personalTeamName, fingerprint)
		}
	}

	// Registration complete - wait for user to press Enter
	fmt.Fprintf(s, "\r\n%sRegistration complete!%s\r\n\r\n", "\033[1;32m", "\033[0m")
	fmt.Fprintf(s, "Your account has been successfully created.\r\n\r\n")
	fmt.Fprintf(s, "%sPress any key continue...%s", "\033[1;36m", "\033[0m")

	// Wait for the goroutine to exit (user presses Enter or any key)
	<-goroutineDone

	// Get team info for the menu
	teams, err := ss.server.getUserTeams(fingerprint)
	if err != nil || len(teams) == 0 {
		fmt.Fprintf(s, "Error: User not associated with any team\r\n")
		return
	}

	// Get the default team for this SSH key
	defaultTeam, err := ss.server.getDefaultTeamForKey(fingerprint)
	if err != nil || defaultTeam == "" {
		defaultTeam = teams[0].TeamName
	}

	// Find the team membership details
	var team TeamMember
	for _, t := range teams {
		if t.TeamName == defaultTeam {
			team = t
			break
		}
	}
	if team.TeamName == "" {
		team = teams[0]
	}

	// Visual feedback that we're entering the menu
	fmt.Fprintf(s, "\r\n\r\n")

	// Transition directly to the main shell menu
	// We pass the session directly and let runMainShellWithReadline create its own reader
	// This avoids issues with partially consumed readers
	ss.runMainShellWithReadline(s, fingerprint, user.Email, team.TeamName, team.IsAdmin, true)
}

// handleExec handles exec commands
func (ss *SSHServer) handleExec(s ssh.Session, cmd []string, username, fingerprint string, registered bool) {
	defer s.Exit(0) // Always send exit status

	if !registered {
		fmt.Fprint(s, "Please complete registration by running: ssh exe.dev\r\n")
		s.Exit(1)
		return
	}

	// Get user and team info
	_, err := ss.server.getUserByFingerprint(fingerprint)
	if err != nil {
		fmt.Fprintf(s, "Authentication error: %v\r\n", err)
		return
	}

	teams, err := ss.server.getUserTeams(fingerprint)
	if err != nil || len(teams) == 0 {
		fmt.Fprint(s, "Error: User not associated with any team\r\n")
		return
	}

	defaultTeam, err := ss.server.getDefaultTeamForKey(fingerprint)
	if err != nil || defaultTeam == "" {
		defaultTeam = teams[0].TeamName
	}

	var team TeamMember
	for _, t := range teams {
		if t.TeamName == defaultTeam {
			team = t
			break
		}
	}
	if team.TeamName == "" {
		team = teams[0]
	}

	// Handle the command
	if len(cmd) == 0 {
		return
	}

	command := cmd[0]
	args := cmd[1:]

	// Use the new handlers that work directly with ssh.Session
	switch command {
	case "create":
		ss.handleCreateCommand(s, fingerprint, team.TeamName, args)
	case "list", "ls":
		ss.handleListCommand(s, fingerprint, team.TeamName)
	case "ssh":
		ss.handleSSHCommandMenu(s, fingerprint, team.TeamName, args)
	case "start":
		ss.handleStartCommand(s, fingerprint, team.TeamName, args)
	case "stop":
		ss.handleStopCommand(s, fingerprint, team.TeamName, args)
	case "delete":
		ss.handleDeleteCommand(s, fingerprint, team.TeamName, args)
	case "logs":
		ss.handleLogsCommand(s, fingerprint, team.TeamName, args)
	case "diag", "diagnostics":
		fmt.Fprintf(s, "\033[1;33mDiagnostics not implemented in new server yet\033[0m\r\n")
	case "team":
		ss.handleTeamCommand(s, fingerprint, team.TeamName, args)
	case "help", "?":
		// Show help text directly
		helpText := "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
			"\033[1;36mMachine Management:\033[0m\r\n" +
			"\033[1mlist\033[0m                    - List your machines\r\n" +
			"\033[1mcreate [image] [name]\033[0m   - Create a new machine (defaults: ubuntu, auto-generated name)\r\n" +
			"\033[1mssh <name>\033[0m              - SSH into a machine\r\n" +
			"\033[1mstart <name>\033[0m            - Start a machine\r\n" +
			"\033[1mstop <name> [...]\033[0m       - Stop one or more machines\r\n" +
			"\033[1mdelete <name>\033[0m           - Delete a machine\r\n" +
			"\033[1mlogs <name>\033[0m             - View machine logs\r\n" +
			"\033[1mdiag <name>\033[0m             - Get machine startup diagnostics\r\n\r\n" +
			"\033[1;36mTeam Management:\033[0m\r\n" +
			"\033[1mteam\033[0m                    - List team members\r\n" +
			"\033[1mteam invite <email>\033[0m     - Invite someone to your team\r\n" +
			"\033[1mteam join <code>\033[0m        - Join a team with an invite code\r\n" +
			"\033[1mteam remove <email>\033[0m     - Remove a team member (admin only)\r\n\r\n" +
			"\033[1mhelp\033[0m or \033[1m?\033[0m              - Show this help\r\n\r\n"
		fmt.Fprint(s, helpText)
	default:
		fmt.Fprintf(s, "Unknown command: %s\r\nRun 'ssh exe.dev help' for available commands.\r\n", command)
	}
}

// handleMachineSSH handles direct SSH access to a machine
func (ss *SSHServer) handleMachineSSH(s ssh.Session, machine *Machine, fingerprint string) {
	if machine.ContainerID == nil {
		fmt.Fprintf(s, "Machine is not running\r\n")
		return
	}

	// Show connection message
	fmt.Fprintf(s, "Connecting to machine %s...\r\n", machine.Name)

	// Get container connection to ensure it's available
	conn, err := ss.server.containerManager.ConnectToContainer(context.Background(), machine.CreatedByFingerprint, *machine.ContainerID)
	if err != nil {
		fmt.Fprintf(s, "Failed to connect to machine: %v\r\n", err)
		return
	}
	if conn != nil && conn.StopFunc != nil {
		defer conn.StopFunc()
	}

	// Get PTY if requested
	pty, _, isPty := s.Pty()

	// Determine the shell to use
	shell, err := ss.server.determineUserShell(machine.CreatedByFingerprint, *machine.ContainerID)
	if err != nil {
		shell = "/bin/bash" // Fallback
	}

	// Check what command was requested
	cmd := s.Command()
	if len(cmd) > 0 {
		// Execute specific command
		err = ss.server.containerManager.ExecuteInContainer(
			context.Background(),
			machine.CreatedByFingerprint,
			*machine.ContainerID,
			cmd,
			s,          // stdin
			s,          // stdout
			s.Stderr(), // stderr
		)

		// Send exit status
		exitStatus := 0
		if err != nil {
			exitStatus = 1
			fmt.Fprintf(s.Stderr(), "Command execution failed: %v\r\n", err)
		}
		s.Exit(exitStatus)
		return
	}

	// Interactive shell session
	shellCmd := []string{shell}

	// Add interactive flags if we have a PTY
	if isPty {
		// Set terminal size environment variables if available
		if pty.Window.Width > 0 && pty.Window.Height > 0 {
			shellCmd = []string{shell, "-c", fmt.Sprintf("stty cols %d rows %d; exec %s", pty.Window.Width, pty.Window.Height, shell)}
		}
	}

	// Execute interactive shell
	err = ss.server.containerManager.ExecuteInContainer(
		context.Background(),
		machine.CreatedByFingerprint,
		*machine.ContainerID,
		shellCmd,
		s,          // stdin
		s,          // stdout
		s.Stderr(), // stderr
	)

	if err != nil {
		fmt.Fprintf(s.Stderr(), "Shell execution failed: %v\r\n", err)
		s.Exit(1)
	} else {
		s.Exit(0)
	}
}

// handleSFTP handles SFTP subsystem requests
func (ss *SSHServer) handleSFTP(s ssh.Session) {
	// Get the username to determine if this is for a specific machine
	username := s.User()
	fingerprint, _ := s.Context().Value("fingerprint").(string)

	// Check if this is a machine-specific SFTP request
	if username != "" && fingerprint != "" && ss.server.containerManager != nil {
		machine := ss.server.findMachineByNameForUser(fingerprint, username)
		if machine != nil && machine.ContainerID != nil {
			// Handle SFTP for the specific machine
			ss.handleMachineSFTP(s, machine, fingerprint)
			return
		}
	}

	// No machine found or general SFTP request
	fmt.Fprintf(s, "SFTP subsystem not available for this context\r\n")
}

// handleMachineSFTP handles SFTP requests for a specific machine
func (ss *SSHServer) handleMachineSFTP(s ssh.Session, machine *Machine, fingerprint string) {
	if machine.ContainerID == nil {
		fmt.Fprintf(s, "Machine is not running\r\n")
		return
	}

	// Use the sshproxy package for SFTP
	containerFS := sshproxy.NewUnixContainerFS(
		ss.server.containerManager,
		machine.CreatedByFingerprint,
		*machine.ContainerID,
		"/workspace",
	)

	handler := sshproxy.NewSFTPHandler(context.Background(), containerFS, "/workspace")
	handlers := sftp.Handlers{
		FileGet:  handler,
		FilePut:  handler,
		FileCmd:  handler,
		FileList: handler,
	}

	server := sftp.NewRequestServer(s, handlers)
	if err := server.Serve(); err != nil && err != io.EOF {
		log.Printf("SFTP server error for machine %s: %v", machine.Name, err)
	}
}

// handlePortForward handles port forwarding requests
func (ss *SSHServer) handlePortForward(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	// TODO: Implement port forwarding
	return false, nil
}

// handleCancelPortForward handles cancel port forwarding requests
func (ss *SSHServer) handleCancelPortForward(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	// TODO: Implement cancel port forwarding
	return false, nil
}

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

// GetPublicKeyFingerprint calculates the SHA256 fingerprint of a public key
func GetPublicKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(hash[:])
}

// getEmailVerification retrieves an email verification by fingerprint
func (s *Server) getEmailVerification(fingerprint string) (*EmailVerification, bool) {
	s.emailVerificationsMu.RLock()
	defer s.emailVerificationsMu.RUnlock()

	for _, v := range s.emailVerifications {
		if v.PublicKeyFingerprint == fingerprint {
			return v, true
		}
	}
	return nil, false
}

// Command handlers for the new SSH server
func (ss *SSHServer) handleListCommand(s ssh.Session, fingerprint, teamName string) {
	// If container manager is available, get real-time status
	if ss.server.containerManager != nil {
		containers, err := ss.server.containerManager.ListContainers(context.Background(), fingerprint)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError listing machines: %v\033[0m\r\n", err)
			return
		}

		if len(containers) == 0 {
			fmt.Fprintf(s, "No machines found. Create one with 'create'.\r\n")
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
	machines, err := ss.server.getMachinesForTeam(teamName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError listing machines: %v\033[0m\r\n", err)
		return
	}

	if len(machines) == 0 {
		fmt.Fprintf(s, "No machines found. Create one with 'create'.\r\n")
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

func (ss *SSHServer) handleCreateCommand(s ssh.Session, fingerprint, teamName string, args []string) {
	if ss.server.containerManager == nil {
		fmt.Fprintf(s, "\033[1;31mMachine management is not available\033[0m\r\n")
		return
	}

	// Parse flags (simplified version - just handle basic cases for now)
	var machineName string
	var image string = "exeuntu"
	var size string = "small"

	// Simple argument parsing - this is a simplified version
	// In production, we'd use the full flag parsing from handleCreateCommandWithStdin
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--name=") {
			machineName = strings.TrimPrefix(arg, "--name=")
		} else if strings.HasPrefix(arg, "--image=") {
			image = strings.TrimPrefix(arg, "--image=")
		} else if strings.HasPrefix(arg, "--size=") {
			size = strings.TrimPrefix(arg, "--size=")
		} else if !strings.HasPrefix(arg, "--") && machineName == "" {
			// Positional argument for name (legacy support)
			machineName = arg
		}
	}

	// Generate machine name if not provided
	if machineName == "" {
		machineName = generateRandomContainerName()
		// Check if name is already taken
		_, err := ss.server.getMachineByName(teamName, machineName)
		if err == nil {
			// Name exists, try again
			for attempts := 0; attempts < 10; attempts++ {
				machineName = generateRandomContainerName()
				_, err = ss.server.getMachineByName(teamName, machineName)
				if err != nil {
					break
				}
			}
		}
	}

	// Get the display image name
	displayImage := container.GetDisplayImageName(image)
	if image == "exeuntu" {
		displayImage = "boldsoftware/exeuntu"
	}

	// Show creation message with proper formatting
	fmt.Fprintf(s, "Creating \033[1m%s\033[0m (%s) for team \033[1;36m%s\033[0m using image \033[1m%s\033[0m...\r\n",
		machineName, size, teamName, displayImage)

	// Get size preset
	sizePreset, exists := container.ContainerSizes[size]
	if !exists {
		fmt.Fprintf(s, "\033[1;31mError: Invalid size '%s'. Valid sizes: micro, small, medium, large, xlarge\033[0m\r\n", size)
		return
	}

	// Create container request
	req := &container.CreateContainerRequest{
		UserID:        fingerprint,
		Name:          machineName,
		TeamName:      teamName,
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

	// Store container info in database
	imageToStore := container.GetDisplayImageName(image)
	if err := ss.server.createMachine(fingerprint, teamName, machineName, createdContainer.ID, imageToStore); err != nil {
		fmt.Fprintf(s, "\033[1;33mWarning: Failed to store machine info: %v\033[0m\r\n", err)
	}

	// Check if container is already running (warm pool case)
	if createdContainer.Status == container.StatusRunning {
		sshCommand := ss.server.formatSSHConnectionInfo(machineName)
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

			containers, err := ss.server.containerManager.ListContainers(context.Background(), fingerprint)
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
				sshCommand := ss.server.formatSSHConnectionInfo(machineName)
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

func (ss *SSHServer) handleSSHCommandMenu(s ssh.Session, fingerprint, teamName string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify a machine name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: ssh <machine-name>\r\n")
		return
	}

	machineName := args[0]
	fmt.Fprintf(s, "Connecting to machine '%s'...\r\n", machineName)
	fmt.Fprintf(s, "\033[1;33mNote: SSH to machines not fully implemented in new server yet\033[0m\r\n")
}

func (ss *SSHServer) handleStartCommand(s ssh.Session, fingerprint, teamName string, args []string) {
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
	machine, err := ss.server.getMachineByName(teamName, machineName)
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
	err = ss.server.containerManager.StartContainer(ctx, fingerprint, *machine.ContainerID)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError starting machine: %v\033[0m\r\n", err)
		return
	}

	// Update database status
	_, err = ss.server.db.Exec(`
		UPDATE machines SET status = 'running', last_started_at = CURRENT_TIMESTAMP
		WHERE name = ? AND team_name = ?`,
		machineName, teamName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;33mWarning: Failed to update machine status: %v\033[0m\r\n", err)
	}

	sshCommand := ss.server.formatSSHConnectionInfo(machineName)
	fmt.Fprintf(s, "\033[1;32mMachine started!\033[0m Access with \033[1m%s\033[0m\r\n", sshCommand)
}

func (ss *SSHServer) handleStopCommand(s ssh.Session, fingerprint, teamName string, args []string) {
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
		machine, err := ss.server.getMachineByName(teamName, machineName)
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
		err = ss.server.containerManager.StopContainer(ctx, fingerprint, *machine.ContainerID)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError stopping machine %s: %v\033[0m\r\n", machineName, err)
			continue
		}

		// Update database status
		_, err = ss.server.db.Exec(`
			UPDATE machines SET status = 'stopped'
			WHERE name = ? AND team_name = ?`,
			machineName, teamName)
		if err != nil {
			fmt.Fprintf(s, "\033[1;33mWarning: Failed to update machine status: %v\033[0m\r\n", err)
		}

		fmt.Fprintf(s, "\033[1;32mMachine '%s' stopped\033[0m\r\n", machineName)
	}
}

func (ss *SSHServer) handleDeleteCommand(s ssh.Session, fingerprint, teamName string, args []string) {
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
			WHERE name = ? AND team_name = ?`,
			machineName, teamName)
		if err != nil {
			fmt.Fprintf(s, "\033[1;31mError deleting machine: %v\033[0m\r\n", err)
			return
		}

		fmt.Fprintf(s, "\033[1;32mMachine '%s' deleted\033[0m\r\n", machineName)
		return
	}

	// Get machine info
	machine, err := ss.server.getMachineByName(teamName, machineName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Machine '%s' not found\033[0m\r\n", machineName)
		return
	}

	fmt.Fprintf(s, "Deleting \033[1m%s\033[0m...\r\n", machineName)

	// Delete the container if it exists
	if machine.ContainerID != nil {
		ctx := context.Background()
		err = ss.server.containerManager.DeleteContainer(ctx, fingerprint, *machine.ContainerID)
		if err != nil {
			fmt.Fprintf(s, "\033[1;33mWarning: Failed to delete container: %v\033[0m\r\n", err)
		}
	}

	// Delete from database
	_, err = ss.server.db.Exec(`
		DELETE FROM machines
		WHERE name = ? AND team_name = ?`,
		machineName, teamName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError deleting machine from database: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\033[1;32mMachine '%s' deleted successfully\033[0m\r\n", machineName)
}

func (ss *SSHServer) handleLogsCommand(s ssh.Session, fingerprint, teamName string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(s, "\033[1;31mError: Please specify a machine name\033[0m\r\n")
		fmt.Fprintf(s, "Usage: logs <machine-name>\r\n")
		return
	}

	machineName := args[0]
	fmt.Fprintf(s, "Fetching logs for machine '%s'...\r\n", machineName)
	fmt.Fprintf(s, "\033[1;33mNote: Logs not implemented in new server yet\033[0m\r\n")
}

func (ss *SSHServer) handleTeamCommand(s ssh.Session, fingerprint, teamName string, args []string) {
	if len(args) == 0 {
		// List team members
		ss.handleTeamList(s, fingerprint, teamName)
		return
	}

	subCmd := args[0]
	// subArgs := args[1:] // Currently unused

	switch subCmd {
	case "list", "ls":
		ss.handleTeamList(s, fingerprint, teamName)
	case "invite":
		fmt.Fprintf(s, "\033[1;33mTeam invite not implemented in new server yet\033[0m\r\n")
	case "join":
		fmt.Fprintf(s, "\033[1;33mTeam join not implemented in new server yet\033[0m\r\n")
	default:
		fmt.Fprintf(s, "\033[1;31mUnknown team command: %s\033[0m\r\n", subCmd)
	}
}

func (ss *SSHServer) handleTeamList(s ssh.Session, fingerprint, teamName string) {
	// Get team members
	rows, err := ss.server.db.Query(`
		SELECT u.email, tm.is_admin, tm.joined_at
		FROM team_members tm
		JOIN users u ON tm.user_fingerprint = u.public_key_fingerprint
		WHERE tm.team_name = ?
		ORDER BY tm.joined_at ASC`,
		teamName)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError retrieving team members: %v\033[0m\r\n", err)
		return
	}
	defer rows.Close()

	fmt.Fprintf(s, "\033[1;36mTeam: %s\033[0m\r\n", teamName)
	fmt.Fprintf(s, "─────────────────────────────────────────────────────────────\r\n")

	memberCount := 0
	for rows.Next() {
		var email string
		var isAdmin bool
		var joinedAt time.Time

		if err := rows.Scan(&email, &isAdmin, &joinedAt); err != nil {
			continue
		}

		memberCount++

		role := "Member"
		if isAdmin {
			role = "\033[1;33mAdmin\033[0m"
		}

		joinedStr := joinedAt.Format("Jan 2, 2006")
		fmt.Fprintf(s, "  • \033[1m%s\033[0m - %s (joined %s)\r\n", email, role, joinedStr)
	}

	if memberCount == 0 {
		fmt.Fprintf(s, "  No team members found.\r\n")
	} else {
		fmt.Fprintf(s, "\r\nTotal members: %d\r\n", memberCount)
	}
}

// Helper method to get machines for a team
func (s *Server) getMachinesForTeam(teamName string) ([]*Machine, error) {
	rows, err := s.db.Query(`
		SELECT id, team_name, name, status, image, container_id,
		       created_by_fingerprint, created_at, updated_at, last_started_at
		FROM machines
		WHERE team_name = ?
		ORDER BY name ASC`,
		teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var machines []*Machine
	for rows.Next() {
		m := &Machine{}
		err := rows.Scan(&m.ID, &m.TeamName, &m.Name, &m.Status, &m.Image,
			&m.ContainerID, &m.CreatedByFingerprint, &m.CreatedAt,
			&m.UpdatedAt, &m.LastStartedAt)
		if err != nil {
			return nil, err
		}
		machines = append(machines, m)
	}

	return machines, rows.Err()
}

// startEmailVerificationNew is a version of startEmailVerification that doesn't depend on sshbuf.Channel
func (ss *SSHServer) startEmailVerificationNew(fingerprint, email, publicKey, teamName string) error {
	// Check if this email already exists
	var existingFingerprint string
	err := ss.server.db.QueryRow("SELECT public_key_fingerprint FROM users WHERE email = ?", email).Scan(&existingFingerprint)

	if err == nil {
		// Email already exists - this is a new device for an existing user

		// Store this key as unverified in ssh_keys table
		_, err = ss.server.db.Exec(`
			INSERT OR REPLACE INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
			VALUES (?, ?, ?, 0, 'Pending Verification')`,
			fingerprint, email, publicKey)
		if err != nil {
			return fmt.Errorf("failed to store pending key: %v", err)
		}

		// Generate token for new device verification
		token := ss.server.generateToken()
		expires := time.Now().Add(15 * time.Minute)

		_, err = ss.server.db.Exec(`
			INSERT INTO pending_ssh_keys (token, fingerprint, public_key, user_email, expires_at)
			VALUES (?, ?, ?, ?, ?)`,
			token, fingerprint, publicKey, email, expires)
		if err != nil {
			return fmt.Errorf("failed to create verification token: %v", err)
		}

		// Create verification object
		verification := &EmailVerification{
			PublicKeyFingerprint: fingerprint,
			PublicKey:            publicKey,
			Email:                email,
			TeamName:             "", // Existing users don't need team name
			Token:                token,
			CompleteChan:         make(chan struct{}),
			CreatedAt:            time.Now(),
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

Device fingerprint: %s

If you did not attempt to register from a new device, please ignore this email.

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, ss.server.getBaseURL(), token, fingerprint[:16])

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
		PublicKeyFingerprint: fingerprint,
		PublicKey:            publicKey,
		Email:                email,
		TeamName:             teamName, // Team name selected by new user
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
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
