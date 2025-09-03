// This file provides shared infrastructure for the e2e tests.

package expect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"exe.dev/container"
	"exe.dev/vouch"
	"github.com/Netflix/go-expect"
	"golang.org/x/crypto/ssh"
)

var (
	flagVerbosePiperd = flag.Bool("vpiperd", false, "enable verbose logging from sshpiperd")
	flagVerboseExed   = flag.Bool("vexed", false, "enable verbose logging from exed")
	flagVerbosePorts  = flag.Bool("vports", false, "enable verbose logging about ports")
	flagVerboseEmail  = flag.Bool("vemail", false, "enable verbose logging from email server")
	flagCinema        = flag.Bool("cinema", false, "enable ASCIIcinema recording for each test")
)

func TestMain(m *testing.M) {
	vouch.For("josh")
	flag.Parse()

	if testing.Short() {
		// ain't nothing short about these tests
		fmt.Println("skipping tests in short mode")
		return
	}

	// Skip tests in CI if exe-ctr-colima is not accessible via SSH
	if os.Getenv("CI") != "" {
		cmd := exec.Command("ssh", "-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes", "exe-ctr-colima", "true")
		if err := cmd.Run(); err != nil {
			fmt.Printf("skipping tests in CI: exe-ctr-colima not accessible via SSH (%v)\n", err)
			return
		}
	}

	env, err := setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "test setup failed: %v\n", err)
		env.Close()
		os.Exit(1)
	}
	Env = env
	fmt.Printf("running tests\n")
	code := m.Run()
	env.Close()
	os.Exit(code)
}

var Env *testEnv

type testEnv struct {
	proxy  *tcpProxy
	exed   exedInstance
	piperd piperdInstance
	email  *emailServer

	asciinemaMu      sync.Mutex // protects asciinemaWriters
	asciinemaWriters map[string]*expect.AsciinemaWriter
}

type exedInstance struct {
	DBPath          string
	Cmd             *exec.Cmd
	SSHPort         int // direct SSH port, not via sshpiper
	HTTPPort        int
	PiperPluginPort int
}

type piperdInstance struct {
	Cmd     *exec.Cmd
	SSHPort int
}

func (e *testEnv) sshPort() int {
	return e.proxy.tcp.Port
}

func (e *testEnv) Close() {
	if e == nil {
		return
	}
	if e.exed.DBPath != "" {
		os.Remove(e.exed.DBPath)
	}
	if e.exed.Cmd != nil && e.exed.Cmd.Process != nil {
		e.exed.Cmd.Process.Kill()
		e.exed.Cmd.Wait()
	}
	if e.piperd.Cmd != nil && e.piperd.Cmd.Process != nil {
		e.piperd.Cmd.Process.Kill()
		e.piperd.Cmd.Wait()
	}

	fmt.Printf("cleaning up containers\n")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Quiet verbose logging from ssh-pool, invoked by package container.
	if !testing.Verbose() {
		slog.SetLogLoggerLevel(slog.LevelWarn)
	}
	if err := container.CleanupTestContainers(ctx, "e1e-"); err != nil {
		fmt.Printf("container cleanup failed: %v", err)
	}
}

type tcpProxy struct {
	ln  net.Listener
	tcp *net.TCPAddr
	dst atomic.Pointer[net.TCPAddr]
}

func newTCPProxy() (*tcpProxy, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	return &tcpProxy{ln: ln, tcp: tcpAddr}, nil
}

func (p *tcpProxy) serve() error {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			dstAddr := p.dst.Load()
			if dstAddr == nil {
				fmt.Printf("tcpProxy: no destination address set")
				return
			}
			dst, err := net.Dial("tcp", dstAddr.String())
			if err != nil {
				fmt.Printf("tcpProxy: failed to connect to dst %v: %v", dstAddr, err)
				return
			}
			defer c.Close()
			var wg sync.WaitGroup
			wg.Go(func() { io.Copy(dst, c) })
			wg.Go(func() { io.Copy(c, dst) })
			wg.Wait()
		}()
	}
}

func setup() (*testEnv, error) {
	env := &testEnv{
		asciinemaWriters: make(map[string]*expect.AsciinemaWriter),
	}

	// We have a circular dependency around ports.
	// (This is not a problem in production, because we use fixed port numbers.)
	//
	// We need to start exed, which needs to know what port sshpiper is listening on,
	// in order to give correct port numbers out to clients.
	//
	// We need to start sshpiper, which needs to know what exed's piper plugin port is.
	//
	// To work around this, we start a simple TCP proxy first, which will act as the sshpiper port.
	// We then forward traffic from the proxy to the actual sshpiper instance.
	proxy, err := newTCPProxy()
	if err != nil {
		return nil, fmt.Errorf("failed to create tcp proxy: %w", err)
	}
	go proxy.serve()
	env.proxy = proxy
	if *flagVerbosePorts {
		fmt.Printf("proxy: listening on port %v\n", proxy.tcp.Port)
	}

	// Start email server first so we can pass its URL to exed
	es, err := newEmailServer()
	if err != nil {
		return env, err
	}
	env.email = es
	if *flagVerboseEmail {
		fmt.Printf("email: listening on %v\n", es.port)
	}

	// TODO: build piperd concurrently with starting exed for faster startup
	ei, err := startExed(es.port, proxy.tcp.Port)
	if err != nil {
		return env, err
	}
	env.exed = *ei

	pi, err := startPiperd(*ei)
	if err != nil {
		return env, err
	}
	env.piperd = *pi
	if *flagVerbosePorts {
		fmt.Printf("piperd: listening on %v\n", pi.SSHPort)
	}

	// proxy tcp requests to piperd
	env.proxy.dst.Store(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pi.SSHPort})

	return env, nil
}

func startPiperd(ei exedInstance) (*piperdInstance, error) {
	start := time.Now()
	fmt.Println("starting piperd")
	tmpFile, err := os.CreateTemp("", "sshpiperd_test_key_*.pem")
	if err != nil {
		return nil, err
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	// Start piperd process and capture its output
	piperdCmd := exec.Command("go", "run", "-race", "./cmd/sshpiperd",
		"--log-format", "json",
		"--log-level", "debug",
		"--port", "0",
		"--drop-hostkeys-message",
		"--address=0.0.0.0",
		"--server-key-generate-mode", "always",
		"--server-key", tmpFile.Name(),
		"grpc",
		"--endpoint=localhost:"+fmt.Sprint(ei.PiperPluginPort),
		"--insecure",
	)
	piperdCmd.Dir = filepath.Join("..", "sshpiper") // run from sshpiper dir so it finds its go.mod

	// Start piperd process and capture its output
	cmdOut, err := piperdCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	piperdCmd.Stderr = piperdCmd.Stdout

	if err := piperdCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start sshpiperd: %w", err)
	}

	// Parse output to find ports
	var teeMu sync.Mutex
	tee := new(bytes.Buffer)
	sshPortC := make(chan int)
	go func() {
		scan := bufio.NewScanner(cmdOut)
		for scan.Scan() {
			line := scan.Bytes()
			teeMu.Lock()
			tee.Write(line)
			tee.Write([]byte("\n"))
			if *flagVerbosePiperd {
				fmt.Println("piperd:", string(line))
			}
			teeMu.Unlock()
			// Parse JSON log line
			if !json.Valid(line) {
				// TODO: log when non-JSON lines are seen?
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				fmt.Fprintf(os.Stderr, "failed to parse log line: %v\n", err)
				continue
			}
			switch entry["msg"] {
			case "sshpiperd is listening":
				port, ok := entry["port"].(float64)
				if ok {
					go func() { sshPortC <- int(port) }()
				} else {
					fmt.Fprintf(os.Stderr, "failed to get SSH port from log entry: %v\n", entry)
					os.Exit(1)
				}
			}
		}
	}()

	timeout := 15 * time.Second
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	var sshPort int
	for sshPort == 0 {
		select {
		case sshPort = <-sshPortC:
		case <-time.After(timeout):
			teeMu.Lock()
			out := tee.String()
			teeMu.Unlock()
			return nil, fmt.Errorf("timeout waiting for piperd to start. output:\n%s", out)
		}
	}

	instance := &piperdInstance{
		Cmd:     piperdCmd,
		SSHPort: sshPort,
	}

	fmt.Printf("started piperd, elapsed=%v\n", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

func startExed(emailServerPort, piperPort int) (*exedInstance, error) {
	start := time.Now()
	fmt.Println("starting exed")
	dbPath, err := os.CreateTemp("", "exed_test_*.db")
	if err != nil {
		return nil, err
	}
	dbPath.Close()

	// Start exed process and capture its output
	emailServerURL := fmt.Sprintf("http://localhost:%d", emailServerPort)
	exedCmd := exec.Command("go", "run", "-race", "../cmd/exed",
		"-db="+dbPath.Name(),
		"-dev=test",
		"-http=:0",
		"-ssh=:0",
		"-piper-plugin=:0",
		"-piperd-port="+fmt.Sprint(piperPort),
		"-fake-email-server="+emailServerURL,
	)
	exedCmd.Env = append(os.Environ(), "LOG_FORMAT=json", "LOG_LEVEL=debug")
	cmdOut, err := exedCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	exedCmd.Stderr = exedCmd.Stdout

	if err := exedCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start exed: %w", err)
	}

	// Parse output to find ports
	var teeMu sync.Mutex
	tee := new(bytes.Buffer)
	type listen struct {
		typ  string
		port int
	}
	listeningC := make(chan listen)
	startedC := make(chan bool)
	go func() {
		scan := bufio.NewScanner(cmdOut)
		for scan.Scan() {
			line := scan.Bytes()
			teeMu.Lock()
			tee.Write(line)
			tee.Write([]byte("\n"))
			if *flagVerboseExed {
				fmt.Println("exed:", string(line))
			}
			teeMu.Unlock()
			// Parse JSON log line
			if !json.Valid(line) {
				// TODO: log when non-JSON lines are seen?
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				fmt.Fprintf(os.Stderr, "failed to parse log line: %v\n", err)
				continue
			}
			switch entry["msg"] {
			case "listening":
				listeningC <- listen{typ: entry["type"].(string), port: int(entry["port"].(float64))}
				if *flagVerbosePorts {
					fmt.Printf("exed: listening to %v on port %v\n", entry["type"], entry["port"])
				}
			case "server started":
				startedC <- true
			}
		}
	}()

	timeout := 15 * time.Second
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	var sshPort, httpPort, piperPluginPort int
ProcessLogs:
	for {
		select {
		case ln := <-listeningC:
			switch ln.typ {
			case "ssh":
				sshPort = ln.port
			case "http":
				httpPort = ln.port
			case "plugin":
				piperPluginPort = ln.port
			}
		case <-startedC:
			break ProcessLogs
		case <-time.After(timeout):
			teeMu.Lock()
			out := tee.String()
			teeMu.Unlock()
			return nil, fmt.Errorf("timeout waiting for exed to start. Output:\n%s", out)
		}
	}
	if sshPort == 0 || httpPort == 0 || piperPluginPort == 0 {
		return nil, fmt.Errorf("failed to start all required ports")
	}

	instance := &exedInstance{
		DBPath:          dbPath.Name(),
		Cmd:             exedCmd,
		SSHPort:         sshPort,
		HTTPPort:        httpPort,
		PiperPluginPort: piperPluginPort,
	}

	fmt.Printf("started exed, elapsed=%v\n", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

// genSSHKey generates an SSH keypair for a test.
// The private half goes into a file to satisfy ssh,
// and the public half is returned as a string,
// for testing convenience.
func genSSHKey(t *testing.T) (path, publickey string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}

	privKeyPath := filepath.Join(t.TempDir(), "id_ed25519")
	privKeyFile, err := os.OpenFile(privKeyPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("failed to create private key file: %v", err)
	}
	privateKeyBytes, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatalf("Failed to marshal private key: %v", err)
	}
	if err := pem.Encode(privKeyFile, privateKeyBytes); err != nil {
		t.Fatalf("Failed to write private key: %v", err)
	}
	err = privKeyFile.Close()
	if err != nil {
		t.Fatalf("failed to close private key file: %v", err)
	}

	publicKey := privateKey.Public().(ed25519.PublicKey)
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	pubStr := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPublicKey)), "\n")

	return privKeyPath, pubStr
}

const (
	ps1    = "\033[1;36mexe.dev\033[0m \033[37m▶\033[0m "
	banner = "EXE.DEV"
)

type expectPty struct {
	t       *testing.T
	console *expect.Console
}

func (p *expectPty) want(s string) {
	p.t.Helper()
	out, err := p.console.ExpectString(s)
	if err != nil {
		p.t.Fatalf("want %q in output (%v). actual output:\n%s", s, err, out)
	}
}

func (p *expectPty) reject(s string) {
	p.t.Helper()
	p.console.RejectString(s)
}

func (p *expectPty) wantf(msg string, args ...any) {
	p.t.Helper()
	p.want(fmt.Sprintf(msg, args...))
}

func (p *expectPty) wantRe(re string) {
	p.t.Helper()
	out, err := p.console.Expect(
		expect.RegexpPattern(re),
	)
	if err != nil {
		p.t.Fatalf("want %q match in output (%v). actual output:\n%s", re, err, out)
	}
}

func (p *expectPty) send(s string) {
	p.t.Helper()
	if _, err := p.console.Send(s); err != nil {
		p.t.Fatalf("failed to send %q: %v", s, err)
	}
}

func (p *expectPty) sendLine(s string) {
	p.t.Helper()
	p.send(s + "\n")
}

func (p *expectPty) wantEOF() {
	p.t.Helper()
	if out, err := p.console.ExpectEOF(); err != nil {
		p.t.Fatalf("want EOF in output (%v). output: %s", err, out)
	}
}

func (p *expectPty) attach(cmd *exec.Cmd) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.console.Tty(), p.console.Tty(), p.console.Tty()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}
}

func makePty(t *testing.T) *expectPty {
	t.Helper()
	opts := []expect.ConsoleOpt{
		expect.WithDefaultRefreshingTimeout(5 * time.Second),
	}
	if testing.Verbose() {
		opts = append(opts, expect.WithStdout(os.Stdout))
	}

	// Add ASCIIcinema recording if -cinema flag is set
	var cinemaOpts []expect.ConsoleOpt
	if *flagCinema {
		cinemaOpts = cinemaOptsForTest(t)
	}
	opts = append(opts, cinemaOpts...)

	sshConsole, err := expect.NewConsole(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sshConsole.Close() })
	return &expectPty{t: t, console: sshConsole}
}

func cinemaOptsForTest(t *testing.T) []expect.ConsoleOpt {
	testName := t.Name()
	Env.asciinemaMu.Lock()
	defer Env.asciinemaMu.Unlock()

	writer, ok := Env.asciinemaWriters[testName]
	if !ok {
		// TODO: snake case
		filename := strings.ReplaceAll(testName, "/", "_") + ".cast"

		const width = 120
		const height = 40
		var err error
		writer, err = expect.NewAsciinemaWriter(filename, width, height)
		if err != nil {
			t.Fatalf("failed to create ASCIIcinema writer: %v", err)
		}

		Env.asciinemaWriters[testName] = writer
		t.Cleanup(func() {
			writer.Close()
			Env.asciinemaMu.Lock()
			defer Env.asciinemaMu.Unlock()
			delete(Env.asciinemaWriters, testName)
		})
	}

	return []expect.ConsoleOpt{expect.WithAsciinemaWriter(writer)}
}

type emailMessage struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type emailServer struct {
	port     int
	server   *http.Server
	listener net.Listener
	inboxMu  sync.Mutex                   // protects inbox
	inbox    map[string]chan emailMessage // email address -> inbox channel
}

func newEmailServer() (*emailServer, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	es := &emailServer{
		port:     port,
		listener: listener,
		inbox:    make(map[string]chan emailMessage),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /", es.handleSendEmail)

	es.server = &http.Server{Handler: mux}

	go func() {
		if err := es.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "email server error: %v\n", err)
		}
	}()

	return es, nil
}

func (es *emailServer) handleSendEmail(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var email emailMessage
	if err := json.Unmarshal(body, &email); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if email.To == "" {
		http.Error(w, "to field is required", http.StatusBadRequest)
		return
	}

	if *flagVerboseEmail {
		fmt.Printf("email: received message: %+v\n", email)
	}

	es.inboxChannel(email.To) <- email
}

func (es *emailServer) inboxChannel(email string) chan emailMessage {
	es.inboxMu.Lock()
	defer es.inboxMu.Unlock()

	ch, exists := es.inbox[email]
	if !exists {
		ch = make(chan emailMessage, 16)
		es.inbox[email] = ch
	}
	return ch
}

// waitForEmail waits for an email to a specific address with a timeout
func (es *emailServer) waitForEmail(t *testing.T, email string) emailMessage {
	ch := es.inboxChannel(email)
	select {
	case msg := <-ch:
		return msg
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for email to %s", email)
		return emailMessage{}
	}
}

// extractVerificationToken extracts the full verification URL from the email body
func extractVerificationToken(body string) (string, error) {
	// Look for the full verification URL pattern
	re := regexp.MustCompile(`http://[^/]+/verify-email\?token=([a-zA-Z0-9\-_]+)`)
	matches := re.FindStringSubmatch(body)
	if len(matches) < 1 {
		return "", fmt.Errorf("verification URL not found in email body")
	}
	return matches[0], nil // Return the full URL, not just the token
}

func sshToExeDev(t *testing.T, keyFile string) *expectPty {
	return sshWithUsername(t, "", keyFile)
}

func sshToBox(t *testing.T, boxname, keyFile string) *expectPty {
	return sshWithUsername(t, boxname, keyFile)
}

func sshWithUsername(t *testing.T, username, keyFile string) *expectPty {
	pty := makePty(t)
	if username != "" {
		username += "@"
	}
	sshCmd := exec.CommandContext(t.Context(), "ssh",
		"-tt",
		"-p", fmt.Sprint(Env.sshPort()),
		"-o", "IdentityFile="+keyFile,
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",

		"-o", "PreferredAuthentications=publickey",
		"-o", "PubkeyAuthentication=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "ChallengeResponseAuthentication=no",
		"-o", "IdentitiesOnly=yes",

		username+"localhost",
	)
	// fmt.Println("RUNNING", sshCmd)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	pty.attach(sshCmd)
	if err := sshCmd.Start(); err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })

	return pty
}

func clickVerifyLinkInEmail(t *testing.T, emailMsg emailMessage) {
	verifyURL, err := extractVerificationToken(emailMsg.Body)
	if err != nil {
		t.Fatalf("failed to extract verification URL: %v", err)
	}

	// Step 1: GET the verification page (shows confirmation form)
	getResp, err := http.Get(verifyURL)
	if err != nil {
		t.Fatalf("failed to access verification page: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("verification page request failed with status: %d", getResp.StatusCode)
	}

	htmlBody, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("failed to read verification page body: %v", err)
	}
	getResp.Body.Close()

	// Extract token from the hidden input field in the HTML form
	re := regexp.MustCompile(`<input[^>]+name="token"[^>]+value="([a-zA-Z0-9\-_]+)"[^>]*>`)
	matches := re.FindStringSubmatch(string(htmlBody))
	if len(matches) < 2 {
		t.Fatalf("failed to extract token from HTML form: %s", string(htmlBody))
	}
	token := matches[1]

	// Submit the form data as POST request (simulating clicking the confirm button)
	formData := url.Values{"token": {token}}
	postURL := fmt.Sprintf("http://localhost:%d/verify-email", Env.exed.HTTPPort)
	postResp, err := http.PostForm(postURL, formData)
	if err != nil {
		t.Fatalf("failed to submit verification form: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(postResp.Body)
		t.Errorf("email verification form submission returned status: %d, body: %s", postResp.StatusCode, string(body))
	}
	postResp.Body.Close()
}

// boxName creates a unique test-specific box name with e1e prefix for easy cleanup
func boxName(t *testing.T) string {
	t.Helper()
	// Create unique-ish test-specific box names: "e1e-{timestamp}-{testname}"
	// This avoids collisions between test runs and makes cleanup easy
	timestamp := time.Now().Unix() % 10000
	testName := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	return fmt.Sprintf("e1e-%d-%s", timestamp, testName)
}

// registerForExeDev is a convenience command to register for an exe.dev account.
// It returns the open pty after registration, the account email, and the private keyFile.
func registerForExeDev(t *testing.T) (pty *expectPty, keyFile, email string) {
	keyFile, publicKey := genSSHKey(t)
	pty = sshToExeDev(t, keyFile)
	pty.want(banner)

	pty.want("Please enter your email")
	email = t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))

	emailMsg := Env.email.waitForEmail(t, email)
	clickVerifyLinkInEmail(t, emailMsg)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Press any key to continue")
	pty.sendLine("")
	pty.want("commands:") // check that we show help menu on first login
	pty.wantRe("exe\\.dev.*▶")

	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)

	return pty, keyFile, email
}
