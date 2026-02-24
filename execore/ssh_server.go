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
	"sync"
	"time"

	"exe.dev/boxname"
	"exe.dev/ctrlc"
	"exe.dev/domz"
	emailpkg "exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/exeweb"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/termfun"
	"exe.dev/tracing"
	"github.com/anmitsu/go-shlex"
	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

var errRegistrationCancelled = errors.New("Registration cancelled")

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

// shellSession wraps ssh.Session to implement exemenu.ShellSession with push-back support.
// It serializes all reads to prevent concurrent calls from splitting data.
type shellSession struct {
	ssh.Session
	mu     sync.Mutex
	buf    []byte
	reader io.Reader // When set, Read() uses this instead of Session

	// clientAddr is the original client's IP address from sshpiper.
	// This is used for IPQS checks during signup validation.
	clientAddr string

	// Window size tracking. gliderlabs/ssh only provides a single channel for
	// window changes, but we need to support multiple consumers (readline,
	// remote SSH sessions, etc). We use a condition variable for broadcast.
	//
	// TODO(bmizerany): This exists because gliderlabs consumes the underlying
	// x/crypto/ssh window-change channel. Replace gliderlabs with raw
	// x/crypto/ssh and this goes away.
	winMu      sync.Mutex
	winCond    *sync.Cond
	winStarted bool
	winVer     uint64 // incremented on each window change
	pty        ssh.Pty
	hasPty     bool
}

func NewSSHShell(s ssh.Session, clientAddr string) *shellSession {
	shell := &shellSession{Session: s, clientAddr: clientAddr}
	shell.winCond = sync.NewCond(&shell.winMu)
	// Close the session when context is done to unblock any pending reads.
	context.AfterFunc(s.Context(), func() {
		shell.Close()
		shell.winCond.Broadcast() // wake any waiters
	})
	return shell
}

// Pty returns a copy of the PTY associated with the session.
// The first call starts the background watcher for window changes.
func (s *shellSession) Pty() (ssh.Pty, bool) {
	s.winMu.Lock()
	defer s.winMu.Unlock()

	if !s.winStarted {
		pty, winCh, ok := s.Session.Pty()
		s.pty = pty
		s.hasPty = ok
		s.winStarted = true
		if ok && winCh != nil {
			go s.watchWindowChanges(winCh)
		}
	}

	return s.pty, s.hasPty
}

// watchWindowChanges reads from gliderlabs' single window channel and
// broadcasts to all waiters via condition variable.
func (s *shellSession) watchWindowChanges(winCh <-chan ssh.Window) {
	ctx := s.Session.Context()
	for {
		select {
		case w, ok := <-winCh:
			if !ok {
				s.winCond.Broadcast()
				return
			}
			s.winMu.Lock()
			s.pty.Window = w
			s.winVer++
			s.winCond.Broadcast()
			s.winMu.Unlock()
		case <-ctx.Done():
			s.winCond.Broadcast()
			return
		}
	}
}

// WaitWindowChange blocks until the terminal window size changes.
// Returns true if a change occurred, false if the session ended.
func (s *shellSession) WaitWindowChange() bool {
	s.winMu.Lock()
	defer s.winMu.Unlock()
	ver := s.winVer
	for s.winVer == ver {
		if s.Session.Context().Err() != nil {
			return false
		}
		s.winCond.Wait()
	}
	return true
}

func (s *shellSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.buf) > 0 {
		n := copy(p, s.buf)
		s.buf = s.buf[n:]
		s.mu.Unlock()
		return n, nil
	}
	r := s.reader
	if r == nil {
		r = s.Session
	}
	s.mu.Unlock()
	return r.Read(p)
}

func (s *shellSession) Push(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(slices.Clone(data), s.buf...)
}

func (s *shellSession) Context() context.Context {
	return s.Session.Context()
}

// SSHServer wraps the gliderlabs SSH server implementation
type SSHServer struct {
	server *Server

	srvMu   sync.Mutex
	srv     *ssh.Server
	stopped bool

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
	ss.srvMu.Lock()
	if ss.stopped {
		return errors.New("starting closed SSH server")
	}
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
	ss.srvMu.Unlock()

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
	ss.srvMu.Lock()
	defer ss.srvMu.Unlock()

	if ss.stopped {
		return nil
	}
	ss.stopped = true

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
	_, isPty := s.Pty()

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
	// Create and set a random trace id
	traceID := tracing.GenerateTraceID()
	ctx.SetValue("trace_id", traceID)

	// Increment auth attempts metric
	ss.server.sshMetrics.authAttempts.WithLabelValues("attempt", "public_key").Inc()
	// Convert gliderlabs public key to golang.org/x/crypto/ssh public key for compatibility
	goKey, err := gossh.ParsePublicKey(key.Marshal())
	if err != nil {
		ss.server.slog().ErrorContext(ctx, "failed to parse public key", "error", err)
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
		ss.server.slog().ErrorContext(ctx, "authentication failed", "error", err)
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
	ctx.SetValue("client_addr", perms.Extensions["client_addr"])

	return true
}

// handleSession handles SSH sessions
func (ss *SSHServer) handleSession(s ssh.Session) {
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
	isExec := len(cmd) > 0

	if isExec {
		// Detect Warp terminal's bootstrap script. Grumble grumble.
		// See https://github.com/boldsoftware/exe.dev/issues/39
		full := strings.Join(cmd, " ")
		isWarpBootstrap := strings.Contains(full, "TERM_PROGRAM=WarpTerminal") || strings.Contains(full, "WARP_SESSION_ID=")
		if isWarpBootstrap {
			// Warp is trying to bootstrap its shell integration.
			// Treat this as an interactive shell session instead, and swallow the command. (Sigh.)
			isExec = false
		}
	}

	if isExec {
		// Handle exec commands
		ss.handleExec(s, cmd, publicKey, registered)
		return
	}

	// Handle interactive shell session
	ss.handleShell(s, publicKey, registered)
}

// handleShell handles interactive shell sessions with readline
func (ss *SSHServer) handleShell(s ssh.Session, publicKey string, registered bool) {
	// Get the real client address from context (set by piper during auth)
	clientAddr := ""
	if ca := s.Context().Value("client_addr"); ca != nil {
		clientAddr = ca.(string)
	}
	if clientAddr == "" {
		clientAddr = s.RemoteAddr().String() // fallback for direct connections
	}
	shell := NewSSHShell(s, clientAddr)
	if !registered {
		// Handle registration flow
		ss.handleRegistration(shell, publicKey)
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

	// Check if user is locked out
	if user.IsLockedOut {
		traceID := tracing.TraceIDFromContext(s.Context())
		ss.server.slog().WarnContext(s.Context(), "locked out user attempted SSH access", "userID", user.UserID, "trace_id", traceID)
		fmt.Fprintf(s, "\r\ncontact support@exe.dev (trace: %s)\r\n", traceID)
		return
	}

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
		line("- \033[1mnew\033[0m to create your first VM")
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
	ctx := s.Context()
	ss.server.slog().DebugContext(ctx, "start runMainShellWithReadline", "public_key", publicKey, "email", user.Email)
	// Show welcome message, hints, tips, etc.
	ss.displayWelcomeTip(s, user)

	ss.server.recordUserEventBestEffort(ctx, user.UserID, userEventUsedREPL)

	// Create a terminal using golang.org/x/term
	terminal := term.NewTerminal(s, fmt.Sprintf("\033[1;36m%s\033[0m \033[37m▶\033[0m ", ss.server.env.ReplHost))

	// Load shell history from database
	history, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetShellHistory, user.UserID)
	if err != nil {
		ss.server.slog().WarnContext(ctx, "failed to load shell history", "error", err)
	}
	for _, entry := range history {
		terminal.History.Add(entry)
	}

	// Set the terminal size to the pty size, and keep it updated whenever
	// the pty changes.
	pty, _ := s.Pty()
	terminal.SetSize(pty.Window.Width, pty.Window.Height)

	go func() {
		for s.WaitWindowChange() {
			pty, _ := s.Pty()
			terminal.SetSize(pty.Window.Width, pty.Window.Height)
		}
	}()

	ss.server.slog().InfoContext(ctx, "starting repl", "public_key", publicKey, "email", user.Email)
	for {
		// Read line with tab completion
		line, err := ss.readLineWithCompletion(terminal, user, publicKey, s)
		if err != nil {
			if err == io.EOF {
				fmt.Fprint(s, "Goodbye!\r\n")
			}
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ss.server.slog().DebugContext(ctx, "command received", "line", line)

		// Add to shell history in database (best-effort), skip sensitive content
		if shouldSaveToHistory(line) {
			if err := withTx1(ss.server, ctx, (*exedb.Queries).AddShellHistory, exedb.AddShellHistoryParams{UserID: user.UserID, Command: line}); err != nil {
				ss.server.slog().WarnContext(ctx, "failed to save shell history", "error", err)
			}
		}

		parts, err := shlex.Split(line, true)
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
			PublicKey:  publicKey,
			Args:       []string{}, // ExecuteCommand will determine the real args
			Output:     s,
			SSHSession: s,
			Terminal:   terminal, // Interactive terminal available
			DevMode:    ss.server.env.ReplDev,
			Logger:     ss.server.slog(),
		}

		// Execute command using new system
		rc := ss.executeCommandWithLogging(ctx, cc, parts)
		if rc == -1 {
			// EOF
			return
		}
	}
}

// executeCommandWithLogging wraps ExecuteCommand to add structured logging
// with timing information and accumulated attributes (similar to sloghttp).
func (ss *SSHServer) executeCommandWithLogging(ctx context.Context, cc *exemenu.CommandContext, parts []string) int {
	start := time.Now()
	cl := GetCommandLog(ctx)
	if cl == nil {
		cl = NewCommandLog(start)
		ctx = WithCommandLog(ctx, cl)
	}

	rc := ss.commands.ExecuteCommand(ctx, cc, parts)

	// Build log attributes
	cmdStr := strings.Join(parts, " ")
	attrs := []any{
		"log_type", "ssh_command",
		"command", cmdStr,
		"rc", rc,
		"duration", time.Since(start),
	}
	// Add command_name (first word) and subcommand (first two words) for easy GROUP BY.
	if len(parts) > 0 {
		attrs = append(attrs, "command_name", parts[0])
	}
	if len(parts) > 1 {
		attrs = append(attrs, "subcommand", parts[0]+" "+parts[1])
	}
	if cc.User != nil {
		attrs = append(attrs, "user_id", cc.User.ID)
	}

	// Add accumulated attributes from handlers
	for _, attr := range cl.Attrs() {
		attrs = append(attrs, attr.Key, attr.Value.Any())
	}

	// This is a canonical log line!
	ss.server.slog().InfoContext(ctx, "ssh command completed", attrs...)

	return rc
}

// showAnimatedWelcome displays the ASCII art with a beautiful fade-out animation
func (ss *SSHServer) showAnimatedWelcome(s *shellSession) {
	// Skip animation in test mode for faster tests
	// ...except for the one case where we do want the full animation in a test. (Sigh.)
	if ss.server.env.SkipBanner && s.User() != "real_banner_please" {
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

	// Use black as the background color. Querying the terminal's actual
	// background color (OSC 11) is problematic because:
	// 1. Many terminals don't support it
	// 2. On high-latency connections, the query timeout can cause issues
	// The visual difference (fading to black vs actual background) is minimal.
	bg := termfun.RGB{R: 0, G: 0, B: 0}

	// Fade from bright green to background color
	from := termfun.RGB{R: 80, G: 255, B: 120}
	to := bg

	// Animate with proper 24-bit colors - more frames for smoother animation
	termfun.FadeTextInPlace(s, asciiArt, from, to, 900*time.Millisecond, 30)

	// After animation, cursor is at the last line of the art
	// Move back to first line and clear the art lines
	fmt.Fprintf(s, "\033[%dA", len(asciiArt)-1)
	for i := range asciiArt {
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
func (ss *SSHServer) readLineWithEchoAndDefault(s *shellSession, defaultValue string) (string, bool) {
	var line []byte

	// Pre-fill with default value if provided
	if defaultValue != "" {
		line = []byte(defaultValue)
		fmt.Fprint(s, defaultValue)
	}

	var b [1]byte
	for {
		if _, err := s.Read(b[:]); err != nil {
			return "", false
		}

		switch b[0] {
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
		case 27: // ESC
			if ss.swallowOSCBackgroundColorResponse(s) {
				continue
			}
			// Ignore bare escape sequences we do not recognize.
		default:
			if b[0] >= 32 && b[0] < 127 { // Printable characters
				line = append(line, b[0])
				// Echo the character
				fmt.Fprintf(s, "%c", b[0])
			}
		}
	}
}

func (ss *SSHServer) swallowOSCBackgroundColorResponse(s *shellSession) bool {
	ctx, cancel := context.WithTimeout(s.Context(), time.Second)
	defer cancel()

	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()

	buf := []byte{27} // ESC
	var b [1]byte

	readNext := func() (byte, error) {
		if _, err := s.Read(b[:]); err != nil {
			return 0, err
		}
		buf = append(buf, b[0])
		return b[0], nil
	}

	// Expect the start of an OSC 11 response: ESC ] 11 ;
	expected := []byte{']', '1', '1', ';'}
	for _, want := range expected {
		got, err := readNext()
		if err != nil || got != want {
			if len(buf) > 1 {
				s.Push(append([]byte(nil), buf[1:]...))
			}
			return false
		}
	}

	// Consume the payload until BEL or ST (ESC \).
	const maxPayload = 2048
	payloadLen := 0
	for {
		if _, err := s.Read(b[:]); err != nil {
			// We requested this sequence, so drop partial data on read errors.
			return true
		}

		switch b[0] {
		case 7: // BEL terminator
			return true
		case 27: // Possible ST (ESC \)
			if _, err := s.Read(b[:]); err != nil {
				return true
			}
			if b[0] == '\\' {
				return true
			}
			payloadLen += 2
			if payloadLen > maxPayload {
				return true
			}
			continue
		default:
			payloadLen++
			if payloadLen > maxPayload {
				return true
			}
		}
	}
}

// handleRegistration handles the registration flow using readline
func (ss *SSHServer) handleRegistration(s *shellSession, publicKey string) {
	ss.showAnimatedWelcome(s)
	ctx := s.Context()

	// Check if the SSH username is a valid invite code
	sshUsername := s.User()
	var inviteCode *exedb.InviteCode
	if sshUsername != "" {
		inviteCode = ss.server.lookupUnusedInviteCode(ctx, sshUsername)
		if inviteCode != nil {
			ss.server.slog().InfoContext(ctx, "valid invite code provided via ssh username", "code", sshUsername)
		}
	}

	// Attempt to identify this as a GitHub user based on their validated public key.
	ghInfo, err := ss.server.githubUser.InfoString(s.Context(), publicKey)
	if err != nil {
		ss.server.slog().InfoContext(ctx, "failed to retrieve GitHub user info", "publicKey", publicKey, "error", err)
	}

	fmt.Fprint(s, "\r\n\033[1;33mEXE.DEV: get a VM over ssh\033[0m\r\n")
	if inviteCode != nil {
		switch inviteCode.PlanType {
		case "free":
			fmt.Fprint(s, "\r\n\033[1;32m✓ Invite code accepted: free account\033[0m\r\n")
		case "trial":
			fmt.Fprint(s, "\r\n\033[1;32m✓ Invite code accepted: 1 month free trial\033[0m\r\n")
		}
	}
	if ghInfo.Email != "" {
		fmt.Fprintf(s, "\r\n✨ Recognized \033[1m@%s\033[0m's public GitHub SSH key. ✨\r\n", ghInfo.Login)
		fmt.Fprintf(s, "(This key and email are public on GitHub; see %s/docs/ssh-github)\r\n", ss.server.webBaseURLNoRequest())
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

	// Validate signup eligibility (use the real client IP from piper, not 127.0.0.1)
	ipStr := exeweb.ClientIPFromRemoteAddr(s.clientAddr)
	if err := ss.server.validateNewSignup(s.Context(), signupValidationParams{
		ip:               ipStr,
		email:            email,
		source:           "ssh",
		trustedGitHubKey: ghInfo.IsGitHubUser && ghInfo.CreditOK,
		hasInviteCode:    inviteCode != nil,
	}); err != nil {
		ss.server.slog().InfoContext(s.Context(), "signup blocked", "reason", err, "ip", ipStr, "email", email)
		trace := tracing.TraceIDFromContext(s.Context())
		fmt.Fprintf(s, "\r\n\033[1;31m%s\033[0m\r\n%s\r\n", err, trace)
		return
	}

	needsEmailVerification := ghInfo.Email == "" || email != ghInfo.Email
	var user *exedb.User
	if needsEmailVerification {
		user, err = ss.waitForEmailVerification(s, publicKey, email, inviteCode)
		if errors.Is(err, errRegistrationCancelled) {
			ss.server.slog().InfoContext(ctx, "email registration cancelled", "email", email)
			fmt.Fprintf(s, "\r\n\033[1;31m%v\033[0m\r\n", err)
			return
		}
		if err != nil || user == nil {
			ss.server.slog().WarnContext(ctx, "email verification failed", "email", email, "error", err)
			fmt.Fprintf(s, "\r\n\033[1;31m%v\033[0m\r\n", err)
			return
		}
	} else {
		// Email matches GitHub's. Rely on their verification; create user directly now.
		ss.server.slog().InfoContext(ctx, "email matches GitHub, skipping verification", "email", email)
		// Skip email quality check if user has an invite code
		qc := AllQualityChecks
		if inviteCode != nil {
			qc = SkipQualityChecks
		}
		inviterEmail := ss.server.getInviteGiverEmail(ctx, inviteCode)
		newUser, err := ss.server.createUserWithSSHKey(s.Context(), email, publicKey, qc, inviterEmail)
		if err != nil {
			ss.server.slog().ErrorContext(ctx, "failed to create user with SSH key during github auto-verification", "error", err)
			fmt.Fprintf(s, "\r\n\033[1;31minternal error: failed to create user account\033[0m\r\n")
			return
		}
		user = newUser
		ss.server.slackFeed.EmailVerified(s.Context(), newUser.UserID)

		// Apply invite code if one was provided
		if inviteCode != nil {
			if err := ss.server.applyInviteCode(ctx, inviteCode, user.UserID); err != nil {
				ss.server.slog().ErrorContext(ctx, "failed to apply invite code", "error", err, "code", inviteCode.Code)
				// Don't fail registration, just log the error
			} else {
				ss.server.slog().InfoContext(ctx, "invite code applied successfully", "code", inviteCode.Code, "user_id", user.UserID, "plan_type", inviteCode.PlanType)
			}
		}
	}

	// Get user's alloc for the menu
	// Visual feedback that we're entering the menu
	fmt.Fprintf(s, "\r\n\r\n")

	// Transition directly to the main shell menu
	ss.runMainShellWithReadline(s, publicKey, user)
}

func (ss *SSHServer) waitForEmailVerification(s *shellSession, publicKey, email string, inviteCode *exedb.InviteCode) (*exedb.User, error) {
	ctx := s.Context()
	ss.server.slog().DebugContext(ctx, "starting email verification", "email", email)
	verification, err := ss.startEmailVerification(s, publicKey, email, inviteCode)
	if err != nil {
		switch {
		case errors.Is(err, errNoEmailService):
			return nil, fmt.Errorf("internal error: email service is not configured")
		case strings.Contains(err.Error(), "marked as inactive"):
			return nil, fmt.Errorf("This email address has been blocked by the email provider.\r\nPlease try a different email address.")
		case strings.Contains(err.Error(), "failed to send verification email"):
			ss.server.slog().ErrorContext(ctx, "email sending failed", "email", email, "error", err)
			return nil, fmt.Errorf("Failed to send verification email. Please check your email address and try again.")
		}
		return nil, err
	}

	fmt.Fprintf(s, "\r\nVerification email sent to: \033[1;32m%s\033[0m\r\n", email)
	// fmt.Fprintf(s, "Pairing code: \033[1;32m%s\033[0m\r\n", verification.PairingCode)
	fmt.Fprintf(s, "\033[2mWaiting for email verification...\033[0m\r\n")

	var r io.Reader
	var stop func()
	ctx, r, stop = ctrlc.WithReader(ctx, s.Session)
	s.mu.Lock()
	s.reader = r
	s.mu.Unlock()

	select {
	case <-verification.CompleteChan:
		stop()
		// Check if billing checkout failed or was canceled
		if verification.Err != nil {
			fmt.Fprintf(s, "\r\n%s✗ %s%s\r\n", "\033[1;31m", verification.Err.Error(), "\033[0m")
			return nil, verification.Err
		}
		fmt.Fprintf(s, "%s✓ Email verified successfully!%s\r\n\r\n", "\033[1;32m", "\033[0m")
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), ctrlc.ErrCanceled) {
			return nil, errRegistrationCancelled
		}
		return nil, fmt.Errorf("session disconnected")
	case <-time.After(10 * time.Minute):
		return nil, fmt.Errorf("Email verification timed out. Please try again.")
	}

	// After successful verification, the user should have been created by the HTTP handler
	// Get the user to verify it was created
	user, userErr := ss.server.getUserByPublicKey(s.Context(), publicKey)
	if userErr != nil || user == nil {
		fingerprint := ""
		if pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(publicKey)); err == nil {
			fingerprint = ss.server.GetPublicKeyFingerprint(pk)
		}
		if userErr != nil {
			ss.server.slog().ErrorContext(s.Context(), "lookup user after verification failed", "error", userErr, "fingerprint", fingerprint)
		} else {
			ss.server.slog().ErrorContext(s.Context(), "lookup user after verification returned nil user", "fingerprint", fingerprint)
		}
		return nil, fmt.Errorf("internal error: user not found after verification")
	}

	// Note: The SSH key was already inserted by the HTTP handler that processed the email verification.
	// We don't need to insert it again here.

	// Check if user is locked out - reject registration if so
	if user.IsLockedOut {
		traceID := tracing.TraceIDFromContext(s.Context())
		ss.server.slog().WarnContext(s.Context(), "locked out user attempted registration", "userID", user.UserID, "trace_id", traceID)
		fmt.Fprintf(s, "\r\ncontact support@exe.dev (trace: %s)\r\n", traceID)
		return nil, fmt.Errorf("account locked")
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
func (ss *SSHServer) handleExec(s ssh.Session, cmd []string, publicKey string, registered bool) {
	defer s.Exit(0) // Always send exit status

	if !registered {
		sshTo := ss.server.replSSHConnectionCommand()
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

	// Check if user is locked out
	if user != nil && user.IsLockedOut {
		traceID := tracing.TraceIDFromContext(s.Context())
		ss.server.slog().WarnContext(s.Context(), "locked out user attempted SSH exec", "userID", user.UserID, "trace_id", traceID)
		fmt.Fprintf(s, "contact support@exe.dev (trace: %s)\r\n", traceID)
		s.Exit(1)
		return
	}

	if len(cmd) == 0 {
		return
	}

	// Get the real client address from context (set by piper during auth)
	clientAddr := ""
	if ca := s.Context().Value("client_addr"); ca != nil {
		clientAddr = ca.(string)
	}
	if clientAddr == "" {
		clientAddr = s.RemoteAddr().String() // fallback for direct connections
	}

	cc := &exemenu.CommandContext{
		User: &exemenu.UserInfo{
			ID:    user.UserID,
			Email: user.Email,
		},
		PublicKey:  publicKey,
		Args:       cmd[1:],                        // Skip the command name itself
		Output:     exemenu.NewANSIFilterWriter(s), // Filter out ANSI control codes from non-interactive sessions.
		SSHSession: NewSSHShell(s, clientAddr),
		Terminal:   nil, // No interactive terminal for exec mode
		DevMode:    ss.server.env.ReplDev,
		Logger:     ss.server.slog(),
	}

	var ctx context.Context = s.Context()
	rc := ss.executeCommandWithLogging(ctx, cc, cmd)
	if rc > 0 {
		s.Close()
		s.Exit(rc)
	}
}

// handleContainerLogs shows logs for a failed instance
func (ss *SSHServer) handleContainerLogs(s ssh.Session, allocID, containerID, boxName string) {
	// Show error message about instance failure
	fmt.Fprintf(s, "\033[1;31mInstance '%s' is not running\033[0m\r\n\r\n", boxName)

	// Extract trace_id from SSH context and add to Go context for gRPC propagation
	var baseCtx context.Context = s.Context()

	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()

	// Get the box to find which exelet it's on
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxByNameAndAlloc, exedb.GetBoxByNameAndAllocParams{
		Name:            boxName,
		CreatedByUserID: allocID,
	})
	if err != nil {
		fmt.Fprintf(s, "\033[1;33mFailed to look up instance: %v\033[0m\r\n", err)
		fmt.Fprintf(s, "To delete this instance, run:\r\n")
		fmt.Fprintf(s, "  \033[1m%s rm %s\033[0m\r\n", ss.server.replSSHConnectionCommand(), boxName)
		return
	}

	// Find the exelet client for this box
	exeletClient := ss.server.getExeletClient(box.Ctrhost)
	if exeletClient == nil {
		fmt.Fprintf(s, "\033[1;33mExelet host not available\033[0m\r\n")
		fmt.Fprintf(s, "To delete this instance, run:\r\n")
		fmt.Fprintf(s, "  \033[1m%s rm %s\033[0m\r\n", ss.server.replSSHConnectionCommand(), boxName)
		return
	}

	// Get instance logs from exelet
	stream, err := exeletClient.client.GetInstanceLogs(ctx, &computeapi.GetInstanceLogsRequest{ID: containerID})
	if err != nil {
		fmt.Fprintf(s, "\033[1;33mFailed to retrieve instance logs: %v\033[0m\r\n", err)
		fmt.Fprintf(s, "To delete this instance, run:\r\n")
		fmt.Fprintf(s, "  \033[1m%s rm %s\033[0m\r\n", ss.server.replSSHConnectionCommand(), boxName)
		return
	}

	// Collect logs from stream (limit to ~100 lines)
	var logs []string
	logCount := 0
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(s, "\033[1;33mError reading logs: %v\033[0m\r\n", err)
			break
		}
		if resp.Log != nil && logCount < 100 {
			logs = append(logs, resp.Log.Message)
			logCount++
		}
	}

	if len(logs) > 0 {
		fmt.Fprintf(s, "\033[1;36mInstance logs:\033[0m\r\n")
		fmt.Fprintf(s, "────────────────────────────────────────\r\n")
		for _, line := range logs {
			fmt.Fprintf(s, "%s\r\n", line)
		}
		fmt.Fprintf(s, "────────────────────────────────────────\r\n\r\n")
	} else {
		fmt.Fprintf(s, "\033[1;33mNo logs available\033[0m\r\n")
	}

	fmt.Fprintf(s, "To delete this instance, run:\r\n")
	fmt.Fprintf(s, "  \033[1m%s rm %s\033[0m\r\n", ss.server.replSSHConnectionCommand(), boxName)
}

func (ss *SSHServer) startEmailVerification(s *shellSession, publicKey, email string, inviteCode *exedb.InviteCode) (*EmailVerification, error) {
	// Check if this SSH key already belongs to another user
	existingEmail, verified, err := ss.server.GetEmailBySSHKey(s.Context(), publicKey)
	if err == nil && verified && existingEmail != email {
		return nil, fmt.Errorf("this SSH key is already registered to another account")
	}

	// Check whether this email already exists
	_, err = ss.server.GetUserIDByEmail(s.Context(), email)
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
		// Note: invite codes are only for new accounts, not existing users adding a device.
		verif := ss.server.addEmailVerification(publicKey, email, isNewAccount, nil)

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
		verifyURL := fmt.Sprintf("%s/verify-device?token=%s", ss.server.webBaseURLNoRequest(), verif.Token)
		body := fmt.Sprintf(`Hello,

A new ssh key is trying to register with your EXE.DEV account email, with public key:

%s

If this was you, please click the link below to authorize this device:

%s

If you did not attempt to register from a new device, please ignore this email.

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, publicKey, verifyURL)

		if err := ss.server.sendEmail(s.Context(), emailpkg.TypeDeviceVerification, email, subject, body); err != nil {
			ss.server.deleteEmailVerification(verif)
			return nil, fmt.Errorf("failed to send verification email: %w", err)
		}
		if ss.server.env.FakeEmail {
			fmt.Fprintf(s, "\r\n[DEV-ONLY] Emailed link: \033[1;36m%s\033[0m\r\n\r\n", verifyURL)
		}

		return verif, nil
	}

	// New user registration
	verif := ss.server.addEmailVerification(publicKey, email, isNewAccount, inviteCode)

	// Send verification email
	subject := "Welcome to EXE.DEV - Verify Your Email"
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s&s=exemenu", ss.server.webBaseURLNoRequest(), verif.Token)
	body := fmt.Sprintf(`Welcome to EXE.DEV!

Please click the link below to verify your email address:

%s

This link will expire in 15 minutes.

Best regards,
The EXE.DEV team`, verifyURL)

	if err := ss.server.sendEmail(s.Context(), emailpkg.TypeNewUserVerification, email, subject, body); err != nil {
		ss.server.deleteEmailVerification(verif)
		return nil, fmt.Errorf("failed to send verification email: %w", err)
	}
	if ss.server.env.FakeEmail {
		fmt.Fprintf(s, "\r\n[DEV-ONLY] Emailed link: \033[1;36m%s\033[0m\r\n\r\n", verifyURL)
	}

	return verif, nil
}

func (s *Server) addEmailVerification(publicKey, email string, isNewAccount bool, inviteCode *exedb.InviteCode) *EmailVerification {
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
		InviteCode:   inviteCode,
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

// lookupUnusedInviteCode checks if the given code is a valid, unused invite code.
// Returns the invite code if valid, or nil if not found or already used.
func (s *Server) lookupUnusedInviteCode(ctx context.Context, code string) *exedb.InviteCode {
	invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetInviteCodeByCode, code)
	if err != nil {
		// Not found or error
		return nil
	}
	// Check if already used
	if invite.UsedByUserID != nil {
		return nil
	}
	return &invite
}

// applyInviteCode marks an invite code as used and sets the user's billing exemption.
func (s *Server) applyInviteCode(ctx context.Context, inviteCode *exedb.InviteCode, userID string) error {
	return s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		// Mark the invite code as used
		if err := q.UseInviteCode(ctx, exedb.UseInviteCodeParams{
			UsedByUserID: &userID,
			ID:           inviteCode.ID,
		}); err != nil {
			return fmt.Errorf("failed to mark invite code as used: %w", err)
		}

		// Set user billing exemption based on plan type
		var billingExemption *string
		var trialEndsAt *time.Time

		switch inviteCode.PlanType {
		case "free":
			exemption := "free"
			billingExemption = &exemption
		case "trial":
			exemption := "trial"
			billingExemption = &exemption
			// Trial ends in 1 month
			t := time.Now().AddDate(0, 1, 0)
			trialEndsAt = &t
		}

		if err := q.SetUserBillingExemption(ctx, exedb.SetUserBillingExemptionParams{
			BillingExemption:     billingExemption,
			BillingTrialEndsAt:   trialEndsAt,
			SignedUpWithInviteID: &inviteCode.ID,
			UserID:               userID,
		}); err != nil {
			return fmt.Errorf("failed to set user billing exemption: %w", err)
		}

		return nil
	})
}

// getInviteGiverEmail returns the email of the user who owns the invite code.
// Returns empty string if the invite code has no assigned user (system-generated).
func (s *Server) getInviteGiverEmail(ctx context.Context, inviteCode *exedb.InviteCode) string {
	if inviteCode == nil || inviteCode.AssignedToUserID == nil {
		return ""
	}
	email, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailByUserID, *inviteCode.AssignedToUserID)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to get invite giver email", "error", err, "user_id", *inviteCode.AssignedToUserID)
		return ""
	}
	return email
}

// readLineWithCompletion reads a line from the terminal with tab completion support
func (ss *SSHServer) readLineWithCompletion(terminal *term.Terminal, user *exedb.User, publicKey string, s exemenu.ShellSession) (string, error) {
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
			PublicKey:  publicKey,
			Output:     s,
			SSHSession: s,
			Terminal:   terminal,
			DevMode:    ss.server.env.ReplDev,
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

// normalizeBoxName extracts the box name from user input.
// It handles both plain box names ("connx") and full hostnames ("connx.exe.xyz").
func (ss *SSHServer) normalizeBoxName(name string) string {
	// Try to extract the box name from a full hostname (e.g., "connx.exe.xyz" -> "connx")
	if label := domz.Label(name, ss.server.env.BoxHost); label != "" {
		return label
	}
	// Return as-is if not a full hostname
	return name
}

func shouldSaveToHistory(line string) bool {
	return !strings.Contains(line, "PRIVATE KEY")
}
