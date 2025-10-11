// This file provides shared infrastructure for the e2e tests.

package e1e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"exe.dev/container"
	"exe.dev/ctrhosttest"
	"exe.dev/vouch"
	"github.com/Netflix/go-expect"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	ansiterm "github.com/veops/go-ansiterm"
	"golang.org/x/crypto/ssh"
)

var (
	flagVerbosePiperd = flag.Bool("vpiperd", false, "enable verbose logging from sshpiperd")
	flagVerboseExed   = flag.Bool("vexed", false, "enable verbose logging from exed")
	flagVerbosePorts  = flag.Bool("vports", false, "enable verbose logging about ports")
	flagVerboseEmail  = flag.Bool("vemail", false, "enable verbose logging from email server")
	flagVerbosePty    = flag.Bool("vpty", false, "enable verbose logging from pty connections")
	flagVerboseSlog   = flag.Bool("vslog", false, "enable verbose logging from slogs")
	flagCinema        = flag.Bool("cinema", true, "enable ASCIIcinema recordings")
)

func TestMain(m *testing.M) {
	vouch.For("josh")
	flag.Parse()

	if testing.Short() {
		// ain't nothing short about these tests
		fmt.Println("skipping tests in short mode")
		return
	}

	err := initLogging()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logging: %v\n", err)
		os.Exit(1)
	}

	if testing.Verbose() && !*flagVerbosePiperd && !*flagVerboseExed && !*flagVerbosePorts && !*flagVerboseEmail && !*flagVerbosePty && !*flagVerboseSlog {
		fmt.Print(`
════════
-v requested, but the e1e tests generate lots of output, and they run in parallel.
Having "-v" enable extra logging is overwhelming.

For debug info, use -run to scope to a single test, and add some/all of these flags:

-vpiperd  print sshpiperd logs
-vexed    print exed logs
-vports   print port mappings
-vemail   print email server logs
-vpty     print pty (ssh) logs
-vslog    print e1e test binary slogs

Flags must be added AFTER the paths, e.g., go test -v -count 1 -run TestHTTPProxyBasic ./e1e/... -vexed
════════

`)
	}

	ctrHost := ctrhosttest.Detect()
	// Skip tests in CI if there is no ctr-host
	if os.Getenv("CI") != "" && ctrHost == "" {
		fmt.Printf("skipping tests in CI: no ctr-host accessible\n")
		return
	}

	env, err := setup(ctrHost)
	if err != nil {
		slog.Error("test setup failed", "error", err)
		env.Close(nil)
		os.Exit(1)
	}

	// prepare container manager early, for faster cleanup
	containerManagerC := make(chan *container.NerdctlManager, 1)
	go func() {
		manager, err := env.initContainerManager(ctrHost)
		containerManagerC <- manager // unblock regardless
		if err != nil {
			slog.Error("failed to init container manager", "error", err)
			return
		}
	}()

	Env = env
	slog.Info("running tests")
	code := m.Run()
	env.Close(<-containerManagerC)

	for _, f := range logFiles {
		if f == nil {
			continue
		}
		err := f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to close log file %v: %v\n", f.Name(), err)
		}
	}
	os.Exit(code)
}

var logFiles = map[string]*os.File{
	"sshpiperd": nil,
	"exed":      nil,
	"e1e":       nil,
}

func logFileFor(name string) *os.File {
	f, ok := logFiles[name]
	if !ok || f == nil {
		return os.Stdout
	}
	return f
}

func initLogging() error {
	e1eLogDir := os.Getenv("E1E_LOG_DIR")
	if e1eLogDir == "" {
		level := slog.LevelWarn
		if *flagVerboseSlog {
			level = slog.LevelDebug
		}
		// Default: Plain text logging to stdout
		handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
		slog.SetDefault(slog.New(handler))
		return nil
	}
	// Log to files. (We're probably in CI.)
	if err := os.MkdirAll(e1eLogDir, 0o700); err != nil {
		return fmt.Errorf("failed to create E1E_LOG_DIR %s: %w", e1eLogDir, err)
	}
	for name := range logFiles {
		logPath := filepath.Join(e1eLogDir, name+".log")
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("failed to open log file %s: %w", logPath, err)
		}
		logFiles[name] = logFile
	}
	handler := slog.NewJSONHandler(logFiles["e1e"], &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	// auto-enable all verbose flags except:
	// - pty, which is accessible via the .cast files
	// - slog, which is already verbose by setting log level to debug above
	*flagVerbosePiperd = true
	*flagVerboseExed = true
	*flagVerbosePorts = true
	*flagVerboseEmail = true
	return nil
}

var Env *testEnv

type testEnv struct {
	proxy  *tcpProxy
	exed   exedInstance
	piperd piperdInstance
	email  *emailServer

	asciinemaMu      sync.Mutex // protects asciinemaWriters
	asciinemaWriters map[string]*expect.AsciinemaWriter

	canonicalizeMu sync.Mutex
	canonicalize   map[string]string // maps non-deterministic strings to deterministic ones
}

type exedInstance struct {
	DBPath          string
	Cmd             *exec.Cmd
	Ctx             context.Context // cancelled when Cmd exits
	SSHPort         int             // direct SSH port, not via sshpiper
	HTTPPort        int
	PiperPluginPort int
	CoverDir        string // directory for Go coverage artifacts (GOCOVERDIR)
	ExtraPorts      []int  // additional proxy ports
}

type piperdInstance struct {
	Cmd     *exec.Cmd
	SSHPort int
}

func (e *testEnv) sshPort() int {
	return e.proxy.tcp.Port
}

func (e *testEnv) initContainerManager(host string) (*container.NerdctlManager, error) {
	config := &container.Config{ContainerdAddresses: []string{host}}
	manager, err := container.NewNerdctlManager(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create container manager: %w", err)
	}

	// Prepare RovolFS on the test host
	ctx := context.Background()
	if err := manager.PrepareRovol(ctx, host); err != nil {
		return nil, fmt.Errorf("failed to prepare RovolFS: %w", err)
	}

	return manager, nil
}

func (t *testEnv) addCanonicalization(in any, canon string) {
	t.canonicalizeMu.Lock()
	defer t.canonicalizeMu.Unlock()
	key := fmt.Sprint(in)
	val, ok := t.canonicalize[key]
	if ok {
		if val != canon {
			panic(fmt.Sprintf("conflicting canonicalization for %q: %q vs %q", key, val, canon))
		}
		return
	}
	t.canonicalize[key] = canon
}

func (t *testEnv) canonicalizeString(s string) string {
	t.canonicalizeMu.Lock()
	defer t.canonicalizeMu.Unlock()
	kv := make([]string, 0, len(t.canonicalize)*2)
	for k, v := range t.canonicalize {
		kv = append(kv, k, v)
	}
	s = strings.NewReplacer(kv...).Replace(s)
	// now canonicalize some other stuff using regexps :/
	s = regexp.MustCompile(`\(boldsoftware/exeuntu@sha256:[a-f0-9]{8}\)`).ReplaceAllString(s, `(boldsoftware/exeuntu@sha256:IMAGE_HASH)`)
	s = regexp.MustCompile(`Ready in [0-9.]+s!`).ReplaceAllString(s, `Ready in ELAPSED_TIME!`)
	s = regexp.MustCompile(`(?m)^.*?@localhost: Permission denied`).ReplaceAllString(s, `USER@localhost: Permission denied`)
	s = strings.ReplaceAll(s, "Press Enter to close this connection.\n", "Press Enter to close this connection.")
	return s
}

func (e *testEnv) context(t *testing.T) context.Context {
	// Merge t.Context() and e.exed.Ctx.
	c, cancel := context.WithCancelCause(t.Context())
	go func() {
		select {
		case <-e.exed.Ctx.Done():
			cancel(context.Cause(e.exed.Ctx))
		case <-c.Done():
		}
	}()
	return c
}

func (e *testEnv) Close(containerManager *container.NerdctlManager) {
	if e == nil {
		return
	}
	if e.exed.DBPath != "" {
		os.Remove(e.exed.DBPath)
	}
	if e.exed.Cmd != nil && e.exed.Cmd.Process != nil {
		e.exed.Cmd.Process.Kill()
		<-e.exed.Ctx.Done()
	}
	if e.piperd.Cmd != nil && e.piperd.Cmd.Process != nil {
		e.piperd.Cmd.Process.Kill()
		e.piperd.Cmd.Wait()
	}
	if containerManager != nil {
		defer containerManager.Close()
		slog.Info("cleaning up containers")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := container.CleanupTestContainers(ctx, containerManager, "e1e-"); err != nil {
			slog.Error("container cleanup failed", "error", err)
		}
	}
	e.proxy.close()

	// CoverDir should always be non-empty, but maybe if exed failed to start?
	// Avoid duplicate/confusing errors by just skipping in this case.
	if e.exed.CoverDir != "" {
		// Extract "legacy" text format Go coverage profile to standard location
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i", e.exed.CoverDir, "-o", "e1e.cover")
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Error("failed to write exed coverage profile", "cmd", cmd.String(), "error", err, "output", string(out))
		}
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
			defer c.Close()
			dstAddr := p.dst.Load()
			if dstAddr == nil {
				slog.Error("tcpProxy: no destination address set")
				return
			}
			dst, err := net.Dial("tcp", dstAddr.String())
			if err != nil {
				slog.Error("tcpProxy: failed to connect to dst", "address", dstAddr, "error", err)
				return
			}
			var wg sync.WaitGroup
			wg.Go(func() { io.Copy(dst, c) })
			wg.Go(func() { io.Copy(c, dst) })
			wg.Wait()
		}()
	}
}

func (p *tcpProxy) close() {
	if p.ln != nil {
		p.ln.Close()
	}
}

func setup(ctrHost string) (*testEnv, error) {
	env := &testEnv{
		asciinemaWriters: make(map[string]*expect.AsciinemaWriter),
		canonicalize:     make(map[string]string),
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
	env.addCanonicalization(proxy.tcp.Port, "SSH_PORT")
	if *flagVerbosePorts {
		slog.Info("proxy listening", "port", proxy.tcp.Port)
	}

	// Start email server first so we can pass its URL to exed
	es, err := newEmailServer()
	if err != nil {
		return env, err
	}
	env.email = es
	env.addCanonicalization(es.port, "EMAIL_SERVER_PORT")
	if *flagVerboseEmail {
		slog.Info("email server listening", "port", es.port)
	}

	// TODO: build piperd concurrently with starting exed for faster startup
	// Pass "0,0" to let the proxy listeners allocate their own port numbers
	ei, err := startExed(ctrHost, es.port, proxy.tcp.Port, []int{0, 0})
	if err != nil {
		return env, err
	}
	env.exed = *ei
	env.addCanonicalization(ei.SSHPort, "EXED_SSH_PORT")
	env.addCanonicalization(ei.HTTPPort, "EXED_HTTP_PORT")
	env.addCanonicalization(ei.PiperPluginPort, "EXED_PIPER_PLUGIN_PORT")

	pi, err := startPiperd(*ei)
	if err != nil {
		return env, err
	}
	env.piperd = *pi
	env.addCanonicalization(pi.SSHPort, "PIPERD_PORT")
	if *flagVerbosePorts {
		slog.Info("piperd listening", "port", pi.SSHPort)
	}

	// proxy tcp requests to piperd
	env.proxy.dst.Store(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pi.SSHPort})

	return env, nil
}

func startPiperd(ei exedInstance) (*piperdInstance, error) {
	start := time.Now()
	slog.Info("starting piperd")
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
				fmt.Fprintln(logFileFor("sshpiperd"), string(line))
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

	slog.Info("started piperd", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
	return instance, nil
}

func startExed(ctrHost string, emailServerPort, piperPort int, extraProxyPorts []int) (*exedInstance, error) {
	start := time.Now()
	slog.Info("starting exed")
	shm := "/dev/shm"
	if st, err := os.Stat(shm); err != nil || !st.IsDir() {
		shm = ""
	}
	dbPath, err := os.CreateTemp(shm, "exed_test_*.db")
	if err != nil {
		return nil, err
	}
	dbPath.Close()

	bin, err := os.CreateTemp("", "exed_test_bin_*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	bin.Close()
	binPath := bin.Name()
	coverDir, err := os.MkdirTemp("", "e1e-exed-cov-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create coverage dir: %w", err)
	}

	buildCmd := exec.Command("go", "build", "-race", "-cover", "-covermode=atomic", "-coverpkg=exe.dev/...", "-o", binPath, "../cmd/exed")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to build exed: %w\n%s", err, out)
	}

	emailServerURL := fmt.Sprintf("http://localhost:%d", emailServerPort)
	whoamiPath := "../ghuser/whoami.sqlite3"
	if os.Getenv("CI") != "" {
		whoamiPath = "/root/whoami.sqlite3"
	}
	exedCmd := exec.Command(binPath,
		"-db="+dbPath.Name(),
		"-dev=test",
		"-http=:0",
		"-ssh=:0",
		"-piper-plugin=:0",
		"-piperd-port="+fmt.Sprint(piperPort),
		"-fake-email-server="+emailServerURL,
		"-gh-whoami="+whoamiPath,
	)
	// Convert extra proxy ports to comma-delimited string
	extraPortsStr := ""
	if len(extraProxyPorts) > 0 {
		portStrs := make([]string, len(extraProxyPorts))
		for i, port := range extraProxyPorts {
			portStrs[i] = fmt.Sprintf("%d", port)
		}
		extraPortsStr = strings.Join(portStrs, ",")
	}

	exedCmd.Env = append(
		os.Environ(),
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
		"CTR_HOST="+ctrHost,
		"GOCOVERDIR="+coverDir,
		"TEST_PROXY_PORTS="+extraPortsStr,
	)
	if os.Getenv("CI") != "" {
		exedCmd.Env = append(exedCmd.Env, "GITHUB_TOKEN=fake-but-not-empty")
	}
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
		seenPanic := false
		scan := bufio.NewScanner(cmdOut)
		for scan.Scan() {
			line := scan.Bytes()
			teeMu.Lock()
			tee.Write(line)
			tee.WriteString("\n")
			teeMu.Unlock()
			if *flagVerboseExed {
				fmt.Fprintln(logFileFor("exed"), string(line))
			}
			if seenPanic {
				fmt.Println(string(line))
			}
			// Parse JSON log line.
			if !json.Valid(line) {
				// Invalid JSON could be a stray fmt.Printf...or a panic.
				// If it's a panic, dup all output to stdout.
				if bytes.Contains(line, []byte("panic:")) {
					seenPanic = true
					// Dump what we have so far.
					// From here on out, we'll print as we go.
					teeMu.Lock()
					fmt.Print(tee.String())
					teeMu.Unlock()
				}
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
					slog.Info("exed listening", "type", entry["type"], "port", entry["port"])
				}
			case "server started":
				startedC <- true
			}
		}
	}()

	timeout := 30 * time.Second
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	var sshPort, httpPort, piperPluginPort int
	var proxyPorts []int
	expectedProxyPorts := len(extraProxyPorts)
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
			default:
				// Check if it's a proxy listener (type: "proxy-XXXX")
				if strings.HasPrefix(ln.typ, "proxy-") {
					proxyPorts = append(proxyPorts, ln.port)
					if *flagVerbosePorts {
						slog.Info("captured proxy port", "type", ln.typ, "port", ln.port)
					}
				}
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
	if len(proxyPorts) != expectedProxyPorts {
		return nil, fmt.Errorf("expected %d proxy ports, got %d", expectedProxyPorts, len(proxyPorts))
	}

	cmdCtx, cancel := context.WithCancel(context.Background())
	go func() {
		exedCmd.Wait()
		cancel()
	}()

	instance := &exedInstance{
		DBPath:          dbPath.Name(),
		Cmd:             exedCmd,
		Ctx:             cmdCtx,
		SSHPort:         sshPort,
		HTTPPort:        httpPort,
		PiperPluginPort: piperPluginPort,
		CoverDir:        coverDir,
		ExtraPorts:      proxyPorts,
	}

	slog.Info("started exed", "elapsed", time.Since(start).Truncate(100*time.Millisecond))
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
	Env.addCanonicalization(pubStr, "SSH_PUBKEY")
	return privKeyPath, pubStr
}

const (
	banner = "~~~ EXE.DEV ~~~"
)

type expectPty struct {
	t        *testing.T
	prompt   string
	promptRe string
	console  *expect.Console
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

func (p *expectPty) wantPrompt() {
	p.t.Helper()
	if p.promptRe != "" {
		p.wantRe(p.promptRe)
		return
	}
	if p.prompt != "" {
		p.want(p.prompt)
		return
	}
	p.t.Fatalf("expectPty: no prompt or promptRe set")
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

func (p *expectPty) disconnect() {
	p.t.Helper()
	p.sendLine("exit")
	p.wantEOF()
}

func (p *expectPty) wantEOF() {
	p.t.Helper()
	if out, err := p.console.ExpectEOF(); err != nil {
		p.t.Fatalf("want EOF in output (%v). output: %s", err, out)
	}
}

// attachAndStart attaches the pty to the given command and starts it.
func (p *expectPty) attachAndStart(cmd *exec.Cmd) {
	// Configure and attach.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.console.Tty(), p.console.Tty(), p.console.Tty()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}

	// Start the command.
	if err := cmd.Start(); err != nil {
		p.t.Fatalf("failed to start %v: %v", cmd, err)
	}
	pty.Setsize(p.console.Tty(), &pty.Winsize{Rows: 120, Cols: 240})
	// sshCmd now owns the PTY; close our reference.
	// Without this, linux hangs on disconnect waiting for EOF.
	p.console.Tty().Close()
	p.t.Cleanup(func() { _ = cmd.Wait() })
}

func makePty(t *testing.T, name string) *expectPty {
	t.Helper()
	opts := []expect.ConsoleOpt{
		// TODO: reduce this timeout.
		// josh increased it on sep 15 because performance regressions in box startup made it necessary to avoid flakiness.
		expect.WithDefaultRefreshingTimeout(45 * time.Second),
	}
	if *flagVerbosePty {
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

	// Write marker to asciinema recording when new PTY is created
	if *flagCinema && sshConsole.IsRecording() {
		box := fmt.Sprintf("\n\n●\r\n● %s\r\n●\r\n\n", name)
		sshConsole.WriteAsciinemaMarker(box)
	}

	return &expectPty{t: t, console: sshConsole}
}

func cinemaOptsForTest(t *testing.T) []expect.ConsoleOpt {
	testName := t.Name()
	Env.asciinemaMu.Lock()
	defer Env.asciinemaMu.Unlock()

	writer, ok := Env.asciinemaWriters[testName]
	if !ok {
		// TODO: snake case
		baseName := strings.ReplaceAll(testName, "/", "_")
		castFile := baseName + ".cast"

		const width = 120
		const height = 32
		var err error
		writer, err = expect.NewAsciinemaWriter(castFile, width, height)
		if err != nil {
			t.Fatalf("failed to create ASCIIcinema writer: %v", err)
		}

		Env.asciinemaWriters[testName] = writer
		t.Cleanup(func() {
			if t.Failed() {
				// Don't overwrite existing golden files on failure.
				// It's annoying to have to clean them up.
				return
			}
			writer.Close()
			_, skip := skipGolden.Load(t.Name())
			if !skip {
				if err := writeAsciinemaToText(castFile, baseName); err != nil {
					fmt.Fprintf(os.Stderr, "failed to write asciinema->text file: %v\n", err)
				}
			}
			Env.asciinemaMu.Lock()
			defer Env.asciinemaMu.Unlock()
			delete(Env.asciinemaWriters, testName)
		})
	}

	return []expect.ConsoleOpt{expect.WithAsciinemaWriter(writer)}
}

func writeAsciinemaToText(castFile, baseName string) error {
	// Convert asciinema -> text
	castData, err := os.ReadFile(castFile)
	if err != nil {
		return fmt.Errorf("failed to read cast file %s: %w", castFile, err)
	}

	text, err := asciinemaToText(castData)
	if err != nil {
		return fmt.Errorf("failed to convert %s to text: %v\n", castFile, err)
	}
	text = Env.canonicalizeString(text)

	textFile := filepath.Join("golden", baseName+".txt")
	if err := os.WriteFile(textFile, []byte(text), 0o600); err != nil {
		return fmt.Errorf("failed to write text file %s: %w", textFile, err)
	}

	return nil
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
		slog.Info("email received", "to", email.To, "subject", email.Subject, "body", email.Body)
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
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for email to %s", email)
		return emailMessage{}
	}
}

// extractVerificationToken extracts the full verification URL from the email body
func extractVerificationToken(body string) (string, error) {
	// Look for the full verification URL pattern
	re := regexp.MustCompile(`http://[^/]+/verify-(email|device)\?token=([a-zA-Z0-9\-_]+)`)
	matches := re.FindStringSubmatch(body)
	if len(matches) < 1 {
		return "", fmt.Errorf("verification URL not found in email body: %s", body)
	}
	return matches[0], nil // Return the full URL, not just the token
}

func sshToExeDev(t *testing.T, keyFile string) *expectPty {
	pty := sshWithUsername(t, "", keyFile)
	pty.prompt = "\033[1;36mexe.dev\033[0m \033[37m▶\033[0m "
	return pty
}

func runExeDevSSHCommand(t *testing.T, keyFile string, args ...string) ([]byte, error) {
	sshArgs := baseSSHArgs("", keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	out, err := sshCmd.CombinedOutput()
	if strings.Contains(string(out), "\r") {
		t.Errorf("ssh output contains \\r, did REPL formatting sneak through? raw output:\n%q", string(out))
	}
	if ansi.Strip(string(out)) != string(out) {
		t.Errorf("ssh output contains ANSI escape codes, did REPL formatting sneak through? raw output:\n%q", string(out))
	}
	return out, err
}

func boxSSHCommand(t *testing.T, boxname, keyFile string, args ...string) *exec.Cmd {
	sshArgs := baseSSHArgs(boxname, keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	return sshCmd
}

func sshToBox(t *testing.T, boxname, keyFile string) *expectPty {
	pty := sshWithUsername(t, boxname, keyFile)
	pty.promptRe = regexp.QuoteMeta(boxname) + ".*" + regexp.QuoteMeta("$")
	return pty
}

func usernameAt(username string) string {
	if username == "" {
		return ""
	}
	return username + "@"
}

func baseSSHArgs(username, keyFile string) []string {
	return []string{
		"-F", "/dev/null",
		"-p", fmt.Sprint(Env.sshPort()),
		"-o", "IdentityFile=" + keyFile,
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

		usernameAt(username) + "localhost",
	}
}

func sshWithUsername(t *testing.T, username, keyFile string) *expectPty {
	pty := makePty(t, "ssh "+usernameAt(username)+"localhost")
	sshArgs := baseSSHArgs(username, keyFile)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	// fmt.Println("RUNNING", sshCmd)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	pty.attachAndStart(sshCmd)
	return pty
}

func clickVerifyLinkInEmail(t *testing.T, emailMsg emailMessage) []*http.Cookie {
	verifyURL, err := extractVerificationToken(emailMsg.Body)
	if err != nil {
		t.Fatalf("failed to extract verification URL: %v", err)
	}

	parsedVerifyURL, err := url.Parse(verifyURL)
	if err != nil {
		t.Fatalf("failed to parse verification URL %q: %v", verifyURL, err)
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

	bodyStr := string(htmlBody)

	codeRe := regexp.MustCompile(`class="code-value">([0-9]{6})<`)
	codeMatches := codeRe.FindStringSubmatch(bodyStr)
	if len(codeMatches) >= 1 {
		pairingCode := codeMatches[1]
		Env.addCanonicalization(pairingCode, "EMAIL_VERIFICATION_CODE")
	}

	// Extract hidden inputs so we can POST the same form fields back
	hiddenRe := regexp.MustCompile(`<input[^>]+name="([^"]+)"[^>]+value="([^"]*)"[^>]*>`)
	formData := url.Values{}
	for _, match := range hiddenRe.FindAllStringSubmatch(bodyStr, -1) {
		name := match[1]
		value := html.UnescapeString(match[2])
		formData.Set(name, value)
	}

	token := formData.Get("token")
	if token == "" {
		t.Fatalf("failed to extract token from HTML form: %s", bodyStr)
	}
	Env.addCanonicalization(token, "EMAIL_VERIFICATION_TOKEN")

	// Determine form action (defaults to /verify-email if not found)
	actionRe := regexp.MustCompile(`<form[^>]+action="([^"]+)"`)
	actionMatch := actionRe.FindStringSubmatch(bodyStr)
	actionPath := "/verify-email"
	if len(actionMatch) >= 2 {
		actionPath = actionMatch[1]
	}
	if !strings.HasPrefix(actionPath, "/") {
		actionPath = "/" + actionPath
	}

	// Create HTTP client with cookie jar to capture authentication cookies
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postURL := fmt.Sprintf("http://localhost:%d%s", Env.exed.HTTPPort, actionPath)
	postResp, err := client.PostForm(postURL, formData)
	if err != nil {
		t.Fatalf("failed to submit verification form: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(postResp.Body)
		t.Errorf("verification form submission returned status: %d, body: %s", postResp.StatusCode, string(body))
	}
	postResp.Body.Close()

	// Extract cookies from the response
	cookies := jar.Cookies(parsedVerifyURL)
	if len(cookies) == 0 {
		parsedPostURL, _ := url.Parse(postURL)
		cookies = jar.Cookies(parsedPostURL)
	}

	return cookies
}

// boxName creates a unique test-specific box name with e1e prefix for easy cleanup
func boxName(t *testing.T) string {
	t.Helper()
	// Create unique-ish test-specific box names: "e1e-{timestamp}-{testname}"
	// This avoids collisions between test runs and makes cleanup easy
	timestamp := fmt.Sprintf("%05d", time.Now().Unix()%100_000)
	testName := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	// Sanitize to allowed charset [a-z0-9-] to satisfy isValidBoxName
	testName = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(testName, "-")
	// Collapse multiple hyphens and trim
	testName = regexp.MustCompile(`-+`).ReplaceAllString(testName, "-")
	testName = strings.Trim(testName, "-")
	Env.addCanonicalization(timestamp, "BOX_TIMESTAMP")
	return fmt.Sprintf("e1e-%s-%s", timestamp, testName)
}

// registerForExeDev is a convenience command to register for an exe.dev account.
// It returns the open pty after registration, authentication cookies for HTTP access,
// the private keyFile, and the account email.
// It is the caller's responsibility to call pty.disconnect() when done.
func registerForExeDev(t *testing.T) (pty *expectPty, cookies []*http.Cookie, keyFile, email string) {
	keyFile, publicKey := genSSHKey(t)
	pty = sshToExeDev(t, keyFile)
	pty.want(banner)

	pty.want("Please enter your email")
	email = t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	pty.wantRe("Pairing code: .*[0-9]{6}.*")

	emailMsg := Env.email.waitForEmail(t, email)
	cookies = clickVerifyLinkInEmail(t, emailMsg)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Press any key to continue")
	pty.sendLine("")
	pty.want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.wantPrompt()

	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()

	t.Logf("INFO: exed is running on http://localhost:%d 'your' e-mail is %s", Env.exed.HTTPPort, email)
	t.Logf("INFO: connect to this exed/sshpiper with:\nssh -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d -i %s localhost\n",
		Env.piperd.SSHPort, keyFile)

	return pty, cookies, keyFile, email
}

// getProxyAuthCookies gets proxy-specific authentication cookies for accessing a boxName subdomain.
// This follows the auth redirect flow to get exe-proxy-auth cookies.
func getProxyAuthCookies(t *testing.T, boxName string, baseCookies []*http.Cookie) []*http.Cookie {
	t.Helper()

	// Create HTTP client with the base auth cookies
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		// Don't follow redirects automatically so we can capture cookies
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Set base cookies for the main domain
	mainURL := fmt.Sprintf("http://localhost:%d", Env.exed.HTTPPort)
	parsedMainURL, _ := url.Parse(mainURL)
	jar.SetCookies(parsedMainURL, baseCookies)

	// Follow the auth redirect flow for the subdomain
	subdomainHost := fmt.Sprintf("%s.localhost", boxName)
	authURL := fmt.Sprintf("%s/auth?redirect=%%2F&return_host=%s", mainURL, url.QueryEscape(subdomainHost))

	// Make request to auth endpoint
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("failed to access auth endpoint: %v", err)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Handle redirect if needed
	if resp.StatusCode == http.StatusTemporaryRedirect {
		location := resp.Header.Get("Location")
		// Make the location absolute if it's relative
		if !strings.HasPrefix(location, "http") {
			location = mainURL + location
		}

		resp, err = client.Get(location)
		if err != nil {
			t.Fatalf("failed to follow redirect: %v", err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()

	}

	// Extract the confirm URL from the HTML response
	confirmRe := regexp.MustCompile(`href="(/auth/confirm\?[^"]+action=confirm)"`)
	matches := confirmRe.FindStringSubmatch(string(body))
	if len(matches) < 2 {
		t.Fatalf("failed to extract confirm URL from auth response: %s", string(body))
	}
	confirmPath := matches[1]
	// Decode HTML entities
	confirmPath = strings.ReplaceAll(confirmPath, "&amp;", "&")
	confirmURL := fmt.Sprintf("%s%s", mainURL, confirmPath)

	// Follow the confirm link to complete authentication
	confirmResp, err := client.Get(confirmURL)
	if err != nil {
		t.Fatalf("failed to confirm auth: %v", err)
	}

	// Handle final redirect if it happens
	if confirmResp.StatusCode == http.StatusTemporaryRedirect {
		_ = confirmResp.Header.Get("Location")
		// Don't follow this redirect - it might be to the subdomain which we can't resolve
	}

	confirmResp.Body.Close()

	// Extract cookies for both main domain and subdomain
	mainParsedURL, _ := url.Parse(mainURL)
	mainCookies := jar.Cookies(mainParsedURL)

	subdomainURL := fmt.Sprintf("http://%s", subdomainHost)
	parsedSubdomainURL, _ := url.Parse(subdomainURL)
	proxyCookies := jar.Cookies(parsedSubdomainURL)

	// If no proxy-specific cookies, create proxy cookies from main cookies
	if len(proxyCookies) == 0 {

		// Create proxy-auth cookies based on the main exe-auth cookie
		var proxyAuthCookies []*http.Cookie
		for _, cookie := range mainCookies {
			if cookie.Name == "exe-auth" {
				// Create an exe-proxy-auth cookie for the subdomain
				proxyAuthCookie := &http.Cookie{
					Name:  "exe-proxy-auth",
					Value: cookie.Value,
					Path:  "/",
				}
				proxyAuthCookies = append(proxyAuthCookies, proxyAuthCookie)

			}
		}
		return proxyAuthCookies
	}

	return proxyCookies
}

// BoxOpts holds optional parameters for newBox.
type BoxOpts struct {
	Image   string
	Command string
}

// newBox requests a new box from the open repl pty.
func newBox(t *testing.T, pty *expectPty, opts ...BoxOpts) string {
	boxName := boxName(t)
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

	pty.sendLine(cmdLine)
	pty.reject("Sorry")
	pty.wantRe("Creating .*" + boxNameRe)
	// break onto two lines because ANSI codes
	pty.want("Access with")
	pty.wantf("ssh -p %v %v@localhost", Env.sshPort(), boxName)

	// Confirm it is there.
	pty.sendLine("list")
	pty.want("boxes")
	pty.wantRe(boxNameRe + ".*running.*\n")
	return boxName
}

func asciinemaToText(castData []byte) (string, error) {
	// asciinema has a size header, but we ignore it.
	// this isn't safe in general, but it makes sense for us, in our context.
	// width and height should both be generous for consistency and to avoid losing scrollback.
	screen := ansiterm.NewScreen(1024, 16384)
	stream := ansiterm.InitByteStream(screen, false)
	stream.Attach(screen)

	// discard header
	_, castLines, ok := bytes.Cut(castData, []byte("\n"))
	if !ok {
		return "", fmt.Errorf("failed to cut header from cast data")
	}
	dec := json.NewDecoder(bytes.NewReader(castLines))
NextLine:
	for {
		var ev []any
		err := dec.Decode(&ev)
		switch {
		case errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF):
			break NextLine
		case err != nil:
			return "", fmt.Errorf("failed to decode event: %w", err)
		}
		if len(ev) != 3 {
			continue
		}
		if typ, _ := ev[1].(string); typ == "o" {
			if data, _ := ev[2].(string); data != "" {
				stream.Feed([]byte(data))
			}
		}
	}

	lines := screen.Display()
	for lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Some ptys like to use a bunch of trailing spaces followed by a series of \b,
	// in order to "clear" the line.
	// This varies by OS, because of course it does.
	// Canonicalize by trimming all trailing spaces.
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	outText := strings.Join(lines, "\n") + "\n"
	return outText, nil
}

var (
	didRunTest sync.Map // map[string]bool
	skipGolden sync.Map // map[string]bool
)

func e1eTestsOnlyRunOnce(t *testing.T) {
	prev, _ := didRunTest.Swap(t.Name(), true)
	if didRun, ok := prev.(bool); ok && didRun {
		t.Fatal("e1e tests don't work with -count > 1. use a bash loop. if this makes you sad, talk to josh.")
	}
}

// noGolden marks the test as not wanting golden file updates.
// We use this for tests that satisfy both of these conditions:
//   - are hard to get stable output out of
//   - whose golden output isn't interesting/useful
func noGolden(t *testing.T) {
	skipGolden.Store(t.Name(), true)
}
