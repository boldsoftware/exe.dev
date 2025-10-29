package restarttest

import (
	"bufio"
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
	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

var (
	flagVerboseSlog   = flag.Bool("vslog", false, "enable verbose logging from slogs")
	flagVerbosePiperd = flag.Bool("vpiperd", false, "enable verbose logging from sshpiperd")
	flagVerboseExed   = flag.Bool("vexed", false, "enable verbose logging from exed")
	flagVerboseEmail  = flag.Bool("vemail", false, "enable verbose logging from email server")
	flagVerbosePorts  = flag.Bool("vports", false, "enable verbose logging about ports")
	flagVerbosePty    = flag.Bool("vpty", false, "enable verbose logging from pty connections")
)

const banner = "~~~ EXE.DEV ~~~"

var loggingOnce sync.Once

func initLogging() {
	level := slog.LevelWarn
	if *flagVerboseSlog {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

type restartSuite struct {
	t   *testing.T
	env *testEnv
}

func newRestartSuite(t *testing.T) *restartSuite {
	t.Helper()
	loggingOnce.Do(initLogging)

	ctrHost := ctrhosttest.Detect()
	if os.Getenv("CI") != "" && ctrHost == "" {
		t.Skip("skipping restart tests in CI: no ctr-host accessible")
	}

	env, err := setup(ctrHost)
	if err != nil {
		if env != nil {
			env.Close(nil)
		}
		t.Fatalf("restart test setup failed: %v", err)
	}

	manager, err := env.initContainerManager(ctrHost)
	if err != nil {
		env.Close(nil)
		t.Fatalf("failed to init container manager: %v", err)
	}

	suite := &restartSuite{t: t, env: env}
	t.Cleanup(func() {
		env.Close(manager)
	})
	return suite
}

func TestBoxRecoversAfterCtrHostAndExedRestart(t *testing.T) {
	vouch.For("david")
	testsOnlyRunOnce(t)

	if os.Getenv("CI") == "" {
		t.Skip("skipping restart tests outside CI")
	}

	suite := newRestartSuite(t)
	env := suite.env

	if env.ctrHostAddress == "" {
		t.Skip("CTR_HOST not configured; skipping recovery test")
	}

	pty, _, keyFile, _ := suite.registerForExeDev()
	boxName := newBox(t, pty)
	pty.disconnect()

	const (
		testFile    = "/home/exedev/recovery-check.txt"
		testContent = "recovery verifies disk persistence"
	)
	writeCmd := suite.boxSSHCommand(boxName, keyFile, "bash", "-lc", fmt.Sprintf("cat <<'EOF' > %[1]s\n%[2]s\nEOF", testFile, testContent))
	if out, err := writeCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to write recovery file: %v\n%s", err, out)
	}

	slog.Info("starting ctr-host restart sequence", "address", env.ctrHostAddress, "alias", env.ctrHostAlias)
	env.restartCtrHost(t)
	slog.Info("ctr-host restart initiated", "alias", env.ctrHostAlias, "domain", env.ctrHostDomain)
	waitForCtrHost(t, env, env.ctrHostAlias)

	env.restartExed(t)

	suite.waitForBoxSSH(boxName, keyFile)

	cmd := suite.boxSSHCommand(boxName, keyFile, "whoami")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to reach box after restarts: %v\n%s", err, out)
	}
	if who := strings.TrimSpace(string(out)); who != "exedev" {
		t.Fatalf("unexpected whoami output after restarts: %q", who)
	}

	verifyCmd := suite.boxSSHCommand(boxName, keyFile, "cat", testFile)
	fileOut, err := verifyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read recovery file after restarts: %v\n%s", err, fileOut)
	}
	if got := strings.TrimSpace(string(fileOut)); got != testContent {
		t.Fatalf("unexpected recovery file content after restarts: %q (want %q)", got, testContent)
	}
}

var didRunTest sync.Map

func testsOnlyRunOnce(t *testing.T) {
	prev, _ := didRunTest.Swap(t.Name(), true)
	if didRun, ok := prev.(bool); ok && didRun {
		t.Fatal("restart tests do not support -count > 1")
	}
}

type testEnv struct {
	proxy           *tcpProxy
	exed            exedInstance
	piperd          piperdInstance
	email           *emailServer
	ctrHostAddress  string
	ctrHostAlias    string
	ctrHostInstance string
	ctrHostDomain   string

	exedMu sync.RWMutex
}

type exedInstance struct {
	DBPath          string
	Cmd             *exec.Cmd
	Ctx             context.Context
	SSHPort         int
	HTTPPort        int
	PiperPluginPort int
}

type piperdInstance struct {
	Cmd     *exec.Cmd
	SSHPort int
}

func (e *testEnv) Close(manager *container.NerdctlManager) {
	if manager != nil {
		defer manager.Close()
	}
	e.exedMu.Lock()
	exed := e.exed
	piperd := e.piperd
	e.exed = exedInstance{}
	e.piperd = piperdInstance{}
	e.exedMu.Unlock()

	if exed.Cmd != nil && exed.Cmd.Process != nil {
		_ = exed.Cmd.Process.Kill()
		if exed.Ctx != nil {
			<-exed.Ctx.Done()
		}
	}
	if piperd.Cmd != nil && piperd.Cmd.Process != nil {
		_ = piperd.Cmd.Process.Kill()
		piperd.Cmd.Wait()
	}
	if e.proxy != nil {
		e.proxy.close()
	}
	if exed.DBPath != "" {
		_ = os.Remove(exed.DBPath)
	}
}

func (e *testEnv) initContainerManager(host string) (*container.NerdctlManager, error) {
	manager, err := container.NewNerdctlManager(&container.Config{ContainerdAddresses: []string{host}})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func (e *testEnv) restartExed(t *testing.T) {
	t.Helper()
	e.exedMu.RLock()
	prevExed := e.exed
	prevPiperd := e.piperd
	dbPath := e.exed.DBPath
	e.exedMu.RUnlock()

	if prevExed.Cmd != nil && prevExed.Cmd.Process != nil {
		if err := prevExed.Cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("failed to stop exed: %v", err)
		}
		if prevExed.Ctx != nil {
			<-prevExed.Ctx.Done()
		}
	}
	if prevPiperd.Cmd != nil && prevPiperd.Cmd.Process != nil {
		if err := prevPiperd.Cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("failed to stop piperd: %v", err)
		}
		prevPiperd.Cmd.Wait()
	}

	ei, err := startExed(e.ctrHostAddress, dbPath, e.email.port, e.proxy.tcp.Port)
	if err != nil {
		t.Fatalf("failed to restart exed: %v", err)
	}
	e.exedMu.Lock()
	e.exed = *ei
	pi, err := startPiperd(*ei)
	if err != nil {
		e.exedMu.Unlock()
		t.Fatalf("failed to restart piperd: %v", err)
	}
	e.piperd = *pi
	e.exedMu.Unlock()

	e.proxy.dst.Store(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pi.SSHPort})
}

func (e *testEnv) restartCtrHost(t *testing.T) {
	t.Helper()
	if e.ctrHostAddress == "" {
		t.Skip("CTR_HOST not configured; skipping recovery test")
	}

	if e.ctrHostInstance != "" {
		restartLimaInstance(t, e.ctrHostInstance)
		return
	}

	host := strings.TrimPrefix(e.ctrHostAddress, "ssh://")
	if host == "" || strings.HasPrefix(host, "tcp://") {
		t.Skipf("CTR_HOST %q cannot be restarted automatically", e.ctrHostAddress)
	}

	domain := restartSSHHost(t, host)
	if domain != "" {
		e.ctrHostDomain = domain
	}
}

func (e *testEnv) context(t *testing.T) context.Context {
	ctx, cancel := context.WithCancelCause(t.Context())
	e.exedMu.RLock()
	exedCtx := e.exed.Ctx
	e.exedMu.RUnlock()
	if exedCtx != nil {
		go func() {
			select {
			case <-exedCtx.Done():
				cancel(context.Cause(exedCtx))
			case <-ctx.Done():
			}
		}()
	}
	return ctx
}

func setup(ctrHost string) (*testEnv, error) {
	env := &testEnv{}

	if ctrHost != "" {
		hostAlias := ctrHost
		if strings.Contains(hostAlias, "://") {
			parts := strings.SplitN(hostAlias, "://", 2)
			hostAlias = parts[1]
		}
		if idx := strings.Index(hostAlias, ":"); idx != -1 {
			hostAlias = hostAlias[:idx]
		}
		env.ctrHostAlias = hostAlias
		if strings.HasPrefix(hostAlias, "lima-") {
			env.ctrHostInstance = strings.TrimPrefix(hostAlias, "lima-")
		}
	}
	env.ctrHostAddress = ctrHost

	proxy, err := newTCPProxy()
	if err != nil {
		return nil, err
	}
	go proxy.serve()
	env.proxy = proxy
	if *flagVerbosePorts {
		slog.Info("proxy listening", "port", proxy.tcp.Port)
	}

	emailServer, err := newEmailServer()
	if err != nil {
		return nil, err
	}
	env.email = emailServer

	exedInst, err := startExed(ctrHost, "", emailServer.port, proxy.tcp.Port)
	if err != nil {
		return nil, err
	}

	piperdInst, err := startPiperd(*exedInst)
	if err != nil {
		return nil, err
	}

	env.exedMu.Lock()
	env.exed = *exedInst
	env.piperd = *piperdInst
	env.exedMu.Unlock()
	env.proxy.dst.Store(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: piperdInst.SSHPort})

	return env, nil
}

// --- process helpers -------------------------------------------------------

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
	return &tcpProxy{ln: ln, tcp: ln.Addr().(*net.TCPAddr)}, nil
}

func (p *tcpProxy) serve() error {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			dst := p.dst.Load()
			if dst == nil {
				slog.Warn("proxy has no destination")
				return
			}
			up, err := net.Dial("tcp", dst.String())
			if err != nil {
				slog.Warn("proxy dial failed", "dst", dst, "error", err)
				return
			}
			defer up.Close()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(up, conn)
			}()
			go func() {
				defer wg.Done()
				_, _ = io.Copy(conn, up)
			}()
			wg.Wait()
		}()
	}
}

func (p *tcpProxy) close() {
	_ = p.ln.Close()
}

// --- email -----------------------------------------------------------------

type emailServer struct {
	port   int
	server *http.Server

	mu    sync.Mutex
	inbox map[string]chan emailMessage
}

type emailMessage struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func newEmailServer() (*emailServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	es := &emailServer{
		port:  ln.Addr().(*net.TCPAddr).Port,
		inbox: make(map[string]chan emailMessage),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", es.handleEmail)
	es.server = &http.Server{Handler: mux}
	go func() {
		if err := es.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("email server stopped", "error", err)
		}
	}()
	return es, nil
}

func (es *emailServer) handleEmail(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var msg emailMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if msg.To == "" {
		http.Error(w, "missing to", http.StatusBadRequest)
		return
	}
	if *flagVerboseEmail {
		slog.Info("email received", "to", msg.To, "subject", msg.Subject, "body", msg.Body)
	}
	es.channel(msg.To) <- msg
	w.WriteHeader(http.StatusOK)
}

func (es *emailServer) channel(addr string) chan emailMessage {
	es.mu.Lock()
	defer es.mu.Unlock()
	ch, ok := es.inbox[addr]
	if !ok {
		ch = make(chan emailMessage, 1)
		es.inbox[addr] = ch
	}
	return ch
}

func (es *emailServer) waitForEmail(t *testing.T, addr string) emailMessage {
	select {
	case msg := <-es.channel(addr):
		return msg
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for email to %s", addr)
		return emailMessage{}
	}
}

// --- process launch --------------------------------------------------------

func startExed(ctrHost, existingDBPath string, emailPort, piperdProxyPort int) (*exedInstance, error) {
	start := time.Now()
	dbPath := existingDBPath
	if dbPath == "" {
		tmp, err := os.CreateTemp("", "restart_exed_*.db")
		if err != nil {
			return nil, err
		}
		tmp.Close()
		dbPath = tmp.Name()
	}

	bin, err := os.CreateTemp("", "restart_exed_bin_*")
	if err != nil {
		return nil, err
	}
	bin.Close()
	binPath := bin.Name()

	buildCmd := exec.Command("go", "build", "-o", binPath, "../../cmd/exed")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to build exed: %v\n%s", err, out)
	}

	emailServerURL := fmt.Sprintf("http://localhost:%d", emailPort)
	whoamiPath := filepath.Join("..", "..", "ghuser", "whoami.sqlite3")
	if os.Getenv("CI") != "" {
		whoamiPath = "/root/whoami.sqlite3"
	}

	exedCmd := exec.Command(binPath,
		"-db="+dbPath,
		"-dev=test",
		"-http=:0",
		"-ssh=:0",
		"-piper-plugin=:0",
		"-piperd-port="+fmt.Sprint(piperdProxyPort),
		"-fake-email-server="+emailServerURL,
		"-gh-whoami="+whoamiPath,
	)

	exedCmd.Env = append(os.Environ(),
		"CTR_HOST="+ctrHost,
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
	)

	stdout, err := exedCmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	exedCmd.Stderr = exedCmd.Stdout

	if err := exedCmd.Start(); err != nil {
		return nil, err
	}

	type listen struct {
		typ  string
		port int
	}
	listenC := make(chan listen, 16)
	var logBuf strings.Builder
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			line := scan.Bytes()
			logBuf.Write(line)
			logBuf.WriteByte('\n')
			if *flagVerboseExed {
				slog.Info("exed", "line", string(line))
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}
			if entry["msg"] == "listening" {
				listenC <- listen{typ: entry["type"].(string), port: int(entry["port"].(float64))}
			}
		}
	}()

	var sshPort, httpPort, pluginPort int
	timeout := time.After(30 * time.Second)
	for sshPort == 0 || httpPort == 0 || pluginPort == 0 {
		select {
		case ln := <-listenC:
			switch {
			case ln.typ == "ssh":
				sshPort = ln.port
				slog.Info("exed port captured", "type", ln.typ, "port", ln.port)
			case ln.typ == "http":
				httpPort = ln.port
				slog.Info("exed port captured", "type", ln.typ, "port", ln.port)
			case ln.typ == "plugin":
				pluginPort = ln.port
				slog.Info("exed port captured", "type", ln.typ, "port", ln.port)
			case strings.HasPrefix(ln.typ, "proxy-"):
				// ignore optional proxy listeners
			}
		case <-timeout:
			_ = exedCmd.Process.Kill()
			return nil, fmt.Errorf("exed failed to start; logs:\n%s", logBuf.String())
		}
	}

	cmdCtx, cancel := context.WithCancel(context.Background())
	go func() {
		exedCmd.Wait()
		cancel()
	}()

	slog.Info("started exed", "ssh", sshPort, "http", httpPort, "plugin", pluginPort, "elapsed", time.Since(start))
	return &exedInstance{
		DBPath:          dbPath,
		Cmd:             exedCmd,
		Ctx:             cmdCtx,
		SSHPort:         sshPort,
		HTTPPort:        httpPort,
		PiperPluginPort: pluginPort,
	}, nil
}

func startPiperd(ei exedInstance) (*piperdInstance, error) {
	tmp, err := os.CreateTemp("", "restart_piperd_bin_*")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	binPath := tmp.Name()

	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/sshpiperd")
	buildCmd.Dir = filepath.Join("..", "..", "sshpiper")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to build piperd: %v\n%s", err, out)
	}

	cmd := exec.Command(binPath,
		"--log-format", "json",
		"--log-level", "debug",
		"--port", "0",
		"--drop-hostkeys-message",
		"--address=0.0.0.0",
		"--server-key-generate-mode", "always",
		"--server-key", filepath.Join(os.TempDir(), fmt.Sprintf("restart-piperd-key-%d", time.Now().UnixNano())),
		"grpc",
		"--endpoint=localhost:"+fmt.Sprint(ei.PiperPluginPort),
		"--insecure",
	)
	cmd.Dir = filepath.Join("..", "..", "sshpiper")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	portCh := make(chan int, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			line := scan.Bytes()
			if *flagVerbosePiperd {
				slog.Info("piperd", "line", string(line))
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}
			if entry["msg"] == "sshpiperd is listening" {
				if port, ok := entry["port"].(float64); ok {
					portCh <- int(port)
					return
				}
			}
		}
	}()

	var sshPort int
	select {
	case sshPort = <-portCh:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("timed out waiting for piperd to start")
	}

	slog.Info("started piperd", "sshPort", sshPort)
	return &piperdInstance{Cmd: cmd, SSHPort: sshPort}, nil
}

// --- ctr-host helpers ------------------------------------------------------

func restartLimaInstance(t *testing.T, instance string) {
	stopCmd := exec.CommandContext(t.Context(), "limactl", "stop", "--tty=false", instance)
	if out, err := stopCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to stop lima instance %s: %v\n%s", instance, err, out)
	}
	startCmd := exec.CommandContext(t.Context(), "limactl", "start", "--tty=false", instance)
	if out, err := startCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to start lima instance %s: %v\n%s", instance, err, out)
	}
	waitForSSH(t, instanceHostAlias(instance))
}

func instanceHostAlias(instance string) string {
	if instance == "" {
		return ""
	}
	if strings.HasPrefix(instance, "lima-") {
		return instance
	}
	return "lima-" + instance
}

func restartSSHHost(t *testing.T, alias string) string {
	hostnameCmd := exec.CommandContext(t.Context(),
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		alias,
		"hostname",
	)
	hostnameOut, err := hostnameCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to determine hostname for %s: %v\n%s", alias, err, hostnameOut)
	}
	hostName := strings.TrimSpace(string(hostnameOut))
	if hostName == "" {
		t.Fatalf("empty hostname returned for %s", alias)
	}

	if out, err := exec.CommandContext(t.Context(), "sudo", "virsh", "reboot", hostName).CombinedOutput(); err == nil {
		slog.Info("virsh reboot issued", "host", hostName, "output", string(out))
		return hostName
	}

	rebootCmd := exec.CommandContext(t.Context(),
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		alias,
		"sudo", "reboot",
	)
	if out, err := rebootCmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 255 {
			slog.Info("ssh reboot command disconnected", "alias", alias)
			return hostName
		}
		t.Fatalf("failed to reboot ctr-host %s via ssh: %v\n%s", alias, err, out)
	}
	slog.Info("ssh reboot issued", "alias", alias)
	return hostName
}

func logCtrHostDiagnostics(t *testing.T, env *testEnv, alias string) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	runCmd := func(name string, args ...string) {
		cmd := exec.CommandContext(ctx, name, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Warn("diagnostic command failed", "cmd", cmd.String(), "error", err, "output", string(out))
			return
		}
		slog.Info("diagnostic command output", "cmd", cmd.String(), "output", string(out))
	}

	if alias != "" {
		runCmd("ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=5",
			alias,
			"uptime && hostname && sudo systemctl --no-pager status containerd || true",
		)
	}

	if env != nil && env.ctrHostDomain != "" {
		runCmd("sudo", "virsh", "domifaddr", env.ctrHostDomain, "--source", "lease")
		runCmd("sudo", "virsh", "dominfo", env.ctrHostDomain)
	}
	runCmd("sudo", "virsh", "list", "--all")
}

func waitForCtrHost(t *testing.T, env *testEnv, alias string) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	delay := 2 * time.Second
	const maxDelay = 20 * time.Second
	attempt := 0

	for {
		attempt++
		cmd := exec.CommandContext(ctx,
			"ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=5",
			alias,
			"sudo", "nerdctl", "--namespace", "exe", "ps",
		)
		out, err := cmd.CombinedOutput()
		if err == nil {
			slog.Info("ctr-host reachable", "alias", alias, "attempt", attempt)
			return
		}
		if ctx.Err() != nil {
			logCtrHostDiagnostics(t, env, alias)
			t.Fatalf("context cancelled while waiting for ctr-host %s: %v", alias, ctx.Err())
		}

		slog.Warn("ctr-host not ready yet", "alias", alias, "attempt", attempt, "error", err, "output", string(out))

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			logCtrHostDiagnostics(t, env, alias)
			t.Fatalf("timed out waiting for ctr-host %s to become available", alias)
		}

		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

func waitForSSH(t *testing.T, alias string) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	delay := 2 * time.Second
	const maxDelay = 10 * time.Second
	attempt := 0

	for {
		attempt++
		cmd := exec.CommandContext(ctx,
			"ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=5",
			alias,
			"true",
		)
		if err := cmd.Run(); err == nil {
			return
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			t.Fatalf("timed out waiting for ssh alias %s", alias)
		}

		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

// --- ssh helpers -----------------------------------------------------------

type expectPty struct {
	t        *testing.T
	console  *expect.Console
	prompt   string
	promptRe string
}

func makePty(t *testing.T, name string) *expectPty {
	opts := []expect.ConsoleOpt{
		expect.WithDefaultRefreshingTimeout(45 * time.Second),
	}
	if *flagVerbosePty {
		opts = append(opts, expect.WithStdout(os.Stdout))
	}
	console, err := expect.NewConsole(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { console.Close() })
	return &expectPty{t: t, console: console}
}

func (p *expectPty) attachAndStart(cmd *exec.Cmd) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.console.Tty(), p.console.Tty(), p.console.Tty()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}
	if err := cmd.Start(); err != nil {
		p.t.Fatalf("failed to start %v: %v", cmd, err)
	}
	pty.Setsize(p.console.Tty(), &pty.Winsize{Rows: 120, Cols: 240})
	p.console.Tty().Close()
	p.t.Cleanup(func() { _ = cmd.Wait() })
}

func (s *restartSuite) sshWithUsername(username, keyFile string) *expectPty {
	pty := makePty(s.t, "ssh "+usernameAt(username)+"localhost")
	sshArgs := s.baseSSHArgs(username, keyFile)
	cmd := exec.CommandContext(s.env.context(s.t), "ssh", sshArgs...)
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
	pty.attachAndStart(cmd)
	return pty
}

func (s *restartSuite) sshToExeDev(keyFile string) *expectPty {
	pty := s.sshWithUsername("", keyFile)
	pty.promptRe = regexp.QuoteMeta("\033[1;36mexe.dev\033[0m \033[37m▶\033[0m ")
	return pty
}

func (p *expectPty) want(str string) {
	p.t.Helper()
	if out, err := p.console.ExpectString(str); err != nil {
		p.t.Fatalf("want %q (%v)\nOutput:\n%s", str, err, out)
	}
}

func (p *expectPty) wantRe(re string) {
	p.t.Helper()
	if out, err := p.console.Expect(expect.RegexpPattern(re)); err != nil {
		p.t.Fatalf("want match %q (%v)\nOutput:\n%s", re, err, out)
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
	p.t.Fatalf("prompt not configured")
}

func (p *expectPty) sendLine(line string) {
	p.t.Helper()
	if _, err := p.console.Send(line + "\n"); err != nil {
		p.t.Fatalf("failed to send %q: %v", line, err)
	}
}

func (p *expectPty) disconnect() {
	p.t.Helper()
	p.sendLine("exit")
	if out, err := p.console.ExpectEOF(); err != nil {
		p.t.Fatalf("expect EOF (%v)\nOutput:\n%s", err, out)
	}
}

func usernameAt(username string) string {
	if username == "" {
		return ""
	}
	return username + "@"
}

func (s *restartSuite) baseSSHArgs(username, keyFile string) []string {
	return []string{
		"-F", "/dev/null",
		"-p", fmt.Sprint(s.env.sshPort()),
		"-o", "IdentityFile=" + keyFile,
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "PreferredAuthentications=publickey",
		"-o", "PubkeyAuthentication=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "ChallengeResponseAuthentication=no",
		"-o", "IdentitiesOnly=yes",
		usernameAt(username) + "localhost",
	}
}

func (e *testEnv) sshPort() int {
	return e.proxy.tcp.Port
}

func (e *testEnv) exedHTTPPort() int {
	e.exedMu.RLock()
	defer e.exedMu.RUnlock()
	return e.exed.HTTPPort
}

func (s *restartSuite) registerForExeDev() (pty *expectPty, cookies []*http.Cookie, keyFile, email string) {
	keyFile, publicKey := genSSHKey(s.t)
	pty = s.sshToExeDev(keyFile)
	pty.want(banner)
	pty.want("Please enter your email")

	email = s.t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	pty.wantRe("Pairing code: .*")

	message := s.env.email.waitForEmail(s.t, email)
	cookies = s.verifyEmail(message)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Welcome to EXE.DEV!")
	pty.wantPrompt()

	_ = publicKey // we do not canonicalize in restart tests
	return pty, cookies, keyFile, email
}

func (s *restartSuite) verifyEmail(msg emailMessage) []*http.Cookie {
	link := extractVerificationLink(msg.Body)
	if link == "" {
		s.t.Fatalf("verification link not found in email body: %s", msg.Body)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		s.t.Fatalf("invalid verification link %q: %v", link, err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatalf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(link)
	if err != nil {
		s.t.Fatalf("failed to fetch verification link: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		s.t.Fatalf("failed to read verification page: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("verification page returned %d: %s", resp.StatusCode, string(body))
	}

	hidden := regexp.MustCompile(`<input[^>]+name="([^\"]+)"[^>]+value="([^\"]*)"[^>]*>`)
	form := url.Values{}
	for _, match := range hidden.FindAllStringSubmatch(string(body), -1) {
		name := match[1]
		value := html.UnescapeString(match[2])
		form.Set(name, value)
	}

	action := "/verify-email"
	actionRe := regexp.MustCompile(`<form[^>]+action="([^\"]+)"`)
	if m := actionRe.FindStringSubmatch(string(body)); len(m) == 2 {
		action = m[1]
		if !strings.HasPrefix(action, "/") {
			action = "/" + action
		}
	}

	postURL := fmt.Sprintf("http://localhost:%d%s", s.env.exedHTTPPort(), action)
	resp, err = client.PostForm(postURL, form)
	if err != nil {
		s.t.Fatalf("failed to submit verification form: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		s.t.Fatalf("verification form returned %d: %s", resp.StatusCode, string(body))
	}

	return jar.Cookies(parsed)
}

func extractVerificationLink(body string) string {
	re := regexp.MustCompile(`http://[^/]+/verify-(?:email|device)\?token=[A-Za-z0-9\-_]+`)
	link := re.FindString(body)
	if link == "" {
		return ""
	}
	return link
}

func newBox(t *testing.T, pty *expectPty) string {
	name := fmt.Sprintf("e1e-%d-%s", time.Now().Unix()%100000, strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
	pty.sendLine("new --name=" + name)
	pty.wantRe("Creating .*" + regexp.QuoteMeta(name))
	pty.want("Access with")
	pty.sendLine("list")
	pty.want("Your boxes:")
	pty.wantRe(name + ".*running")
	return name
}

func (s *restartSuite) boxSSHCommand(boxName, keyFile string, args ...string) *exec.Cmd {
	sshArgs := s.baseSSHArgs(boxName, keyFile)
	sshArgs = append(sshArgs, args...)
	cmd := exec.CommandContext(s.env.context(s.t), "ssh", sshArgs...)
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
	return cmd
}

func (s *restartSuite) waitForBoxSSH(boxName, keyFile string) {
	start := time.Now()
	delay := 2 * time.Second
	const maxDelay = 20 * time.Second

	for {
		cmd := s.boxSSHCommand(boxName, keyFile, "true")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		if time.Since(start) > 5*time.Minute {
			s.t.Fatalf("timed out waiting for box ssh: %v\n%s", err, out)
		}
		time.Sleep(delay)
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

func genSSHKey(t *testing.T) (path, public string) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	dir := t.TempDir()
	path = filepath.Join(dir, "id_ed25519")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("failed to create key file: %v", err)
	}
	bytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}
	if err := pem.Encode(f, bytes); err != nil {
		t.Fatalf("failed to write key: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("failed to close key: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("failed to create public key: %v", err)
	}
	return path, strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")
}
