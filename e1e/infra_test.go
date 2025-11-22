// This file provides shared infrastructure for the e2e tests.

package e1e

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"exe.dev/ctrhosttest"
	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
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
	flagVerboseExelet = flag.Bool("vexelet", false, "enable verbose logging from exelet")
	flagVerbosePorts  = flag.Bool("vports", false, "enable verbose logging about ports")
	flagVerboseEmail  = flag.Bool("vemail", false, "enable verbose logging from email server")
	flagVerbosePty    = flag.Bool("vpty", false, "enable verbose logging from pty connections")
	flagVerboseSlog   = flag.Bool("vslog", false, "enable verbose logging from slogs")
	flagCinema        = flag.Bool("cinema", true, "enable ASCIIcinema recordings")

	// testRunID is a random identifier for this test invocation.
	// A single container host is often shared across test and dev runs.
	// We use this ID to understand which boxes were created specifically by this test run.
	testRunID string
)

func TestMain(m *testing.M) {
	vouch.For("josh")
	flag.Parse()

	// Generate unique test run ID to avoid box name collisions
	testRunID = fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)

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

	if testing.Verbose() && !*flagVerbosePiperd && !*flagVerboseExed && !*flagVerboseExelet && !*flagVerbosePorts && !*flagVerboseEmail && !*flagVerbosePty && !*flagVerboseSlog {
		fmt.Print(`
════════
-v requested, but the e1e tests generate lots of output, and they run in parallel.
Having "-v" enable extra logging is overwhelming.

For debug info, use -run to scope to a single test, and add some/all of these flags:

-vpiperd  print sshpiperd logs
-vexed    print exed logs
-vexelet  print exelet logs
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
		fmt.Fprintf(os.Stderr, "failed to setup test environment: %v\n", err)
		slog.Error("test setup failed", "error", err)
		if closeErr := env.Close(nil); closeErr != nil {
			slog.Error("cleanup failed", "error", closeErr)
		}
		os.Stderr.Sync()
		os.Exit(1)
	}

	// prepare exelet client early, for faster cleanup
	exeletClientC := make(chan *client.Client, 1)
	go func() {
		c, err := env.initExeletClient()
		exeletClientC <- c // unblock regardless
		if err != nil {
			slog.Error("failed to init exelet client", "error", err)
			return
		}
	}()

	Env = env
	slog.Info("running tests")
	code := m.Run()
	if err := env.Close(<-exeletClientC); err != nil {
		slog.Error("test cleanup failed", "error", err)
		fmt.Fprintf(os.Stderr, "\n\nERROR: %v\n\n", err)
		if code == 0 {
			code = 1
		}
	}
	close(env.exedSlogErrC)
	for line := range env.exedSlogErrC {
		// TODO(philip): TestNewWithPrompt triggers this, because Shelley talks
		// to the gateway and even though it's supposed to use "predictable" model, we get an error.
		// This is an unrelated bug uncovered when I was trying to change how the
		// plumbing works for the llm gateway, so I'm punting on fixing that bug and making
		// the test infra ever so slightly less picky about error logs.
		// Note that the change that exposed this was: "ctrhosttest: fix ResolveDefaultGateway to parse CTR_HOST SSH URLs"
		// which leads me to believe that the Shelley gateway URL was wrong previously, and Shelley
		// was silently swallowing an error, and now it's managing to talk to exed.
		if strings.Contains(line, "\"msg\":\"llmgateway.httpError\"") {
			continue
		}
		code = 1
		fmt.Fprintf(os.Stderr, "\n\nexed emitted ERROR log during e1e run:\n%s\n\n", line)
	}
	close(env.exeletSlogErrC)
	for line := range env.exeletSlogErrC {
		code = 1
		fmt.Fprintf(os.Stderr, "\n\nexelet emitted ERROR log during e1e run:\n%s\n\n", line)
	}
	if env.exedGuidLogC != nil {
		close(env.exedGuidLogC)
		for line := range env.exedGuidLogC {
			fmt.Fprintf(os.Stderr, "\n\nexed log with guid during e1e run:\n%s\n\n", line)
		}
	}

	for _, f := range logFiles {
		if f == nil {
			continue
		}
		err := f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to close log file %v: %v\n", f.Name(), err)
		}
	}
	os.Stderr.Sync()
	os.Exit(code)
}

var logFiles = map[string]*os.File{
	"sshpiperd": nil,
	"exed":      nil,
	"exelet":    nil,
	"e1e":       nil,
}

var guidRegex = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

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
	sshProxy       *tcpProxy
	exedHTTPProxy  *tcpProxy
	exed           exedInstance
	piperd         piperdInstance
	exelet         exeletInstance
	email          *emailServer
	exedSlogErrC   chan string // receives exed ERROR log lines
	exeletSlogErrC chan string // receives exelet ERROR log lines
	exedGuidLogC   chan string // receives exed log lines with guid attribute

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

type exeletInstance struct {
	Address      string             // e.g., "tcp://192.168.5.15:9080"
	Ctx          context.Context    // cancelled when Cmd exits
	DataDir      string             // temp directory for exelet data (local or remote path)
	RemoteHost   string             // SSH host if running remotely (e.g., "lima-exe-ctr-tests")
	TunnelCmd    *exec.Cmd          // SSH tunnel process if using reverse tunnel
	TunnelCancel context.CancelFunc // cancel function for tunnel context
}

func (e *testEnv) sshPort() int {
	return e.sshProxy.tcp.Port
}

// parseSSHHost extracts hostname from ssh:// URL
func parseSSHHost(ctrHost string) string {
	return strings.TrimPrefix(ctrHost, "ssh://")
}

// buildExeletBinary builds exelet locally for Linux and returns path to binary
func buildExeletBinary() (string, error) {
	binPath := filepath.Join(os.TempDir(), "exelet-test")
	// run make to make sure to build dependencies (e.g. rovol and kernel that are embedded)
	cmd := exec.Command("make", "exelet")

	// Set working directory to project root (parent of e1e directory)
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	buildDir := filepath.Dir(wd)
	cmd.Dir = buildDir

	// Cross-compile for Linux with current machine architecture
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH)

	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to build exelet: %w\n%s\n", err, out)
	}
	// rename to test binary
	if err := os.MkdirAll(filepath.Dir(binPath), 0o770); err != nil {
		return "", fmt.Errorf("failed to create binpath parent: %w", err)
	}
	if err := os.Rename(filepath.Join(buildDir, "exeletd"), binPath); err != nil {
		return "", fmt.Errorf("failed to rename exelet to %s: %w", binPath, err)
	}
	return binPath, nil
}

// sshExec executes a command on remote host and returns combined output
func sshExec(ctx context.Context, host, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		host, command)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scpUpload uploads a file to remote host
func scpUpload(localPath, host, remotePath string) error {
	cmd := exec.Command("scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		localPath, host+":"+remotePath)
	return cmd.Run()
}

func (e *testEnv) initExeletClient() (*client.Client, error) {
	c, err := client.NewClient(e.exelet.Address, client.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("failed to create exelet client: %w", err)
	}
	return c, nil
}

// CleanupTestInstances removes instances.
// Designed for cleaning up test instances; best effort only.
func CleanupTestInstances(ctx context.Context, exeletClient *client.Client) error {
	if exeletClient == nil {
		return nil
	}

	stream, err := exeletClient.ListInstances(ctx, &api.ListInstancesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	var instancesToDelete []string
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Error("error receiving instance list", "error", err)
			break
		}
		instancesToDelete = append(instancesToDelete, resp.Instance.ID)
	}

	for _, id := range instancesToDelete {
		slog.Info("deleting test instance", "id", id)
		if _, err := exeletClient.DeleteInstance(ctx, &api.DeleteInstanceRequest{ID: id}); err != nil {
			slog.Error("failed to delete instance", "id", id, "error", err)
		}
	}

	return nil
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
	// Build replacements and sort by key length descending to avoid
	// substring collisions. Replace longest keys first.
	pairs := make([][2]string, 0, len(t.canonicalize))
	for k, v := range t.canonicalize {
		pairs = append(pairs, [2]string{k, v})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		// primary: length of key (desc)
		if lenA, lenB := len(a[0]), len(b[0]); lenA != lenB {
			return cmp.Compare(lenB, lenA)
		}
		// secondary: key lexicographic (asc) for determinism
		return cmp.Compare(a[0], b[0])
	})
	kv := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		kv = append(kv, p[0], p[1])
	}
	s = strings.NewReplacer(kv...).Replace(s)
	// now canonicalize some other stuff using regexps :/
	s = regexp.MustCompile(`\(boldsoftware/exeuntu@sha256:[a-f0-9]{8}\)`).ReplaceAllString(s, `(boldsoftware/exeuntu@sha256:IMAGE_HASH)`)
	s = regexp.MustCompile(`Ready in [0-9.]+s!`).ReplaceAllString(s, `Ready in ELAPSED_TIME!`)
	s = regexp.MustCompile(`(?m)^.*?@localhost: Permission denied`).ReplaceAllString(s, `USER@localhost: Permission denied`)
	s = strings.ReplaceAll(s, "Press Enter to close this connection.\n", "Press Enter to close this connection.")
	// Canonicalize share tokens (26-character alphanumeric tokens that appear in share URLs or standalone)
	s = regexp.MustCompile(`(share=|\s)([A-Z0-9]{26})\b`).ReplaceAllString(s, `${1}SHARE_TOKEN`)
	// Canonicalize invitation timestamps
	s = regexp.MustCompile(`\(invited [^)]+\)`).ReplaceAllString(s, `(invited INVITE_AGE)`)
	// Canonicalize share link creation timestamps (e.g., "created now" or "created 1 second ago")
	s = regexp.MustCompile(`\(created [^,]+,`).ReplaceAllString(s, `(created SHARE_AGE,`)
	// Canonicalize random MOTD hints shown when SSHing to a box
	s = regexp.MustCompile(`(?s)(For support and documentation, "ssh exe\.dev" or visit https://exe\.dev/\n)\n(.+?)\n(exedev@)`).ReplaceAllString(s, "$1\nMOTD HINT\n\n$3")
	// Canonicalize shelley.backup timestamps (format: YYYYMMDD-HHMMSS)
	s = regexp.MustCompile(`shelley\.backup\.\d{8}-\d{6}`).ReplaceAllString(s, `shelley.backup.TIMESTAMP`)
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

func (e *testEnv) Close(exeletClient *client.Client) error {
	if e == nil {
		return nil
	}

	// Check that all boxes have been cleaned up before killing exed
	var checkErr error
	if e.exed.Cmd != nil && e.exed.Cmd.Process != nil {
		if err := e.checkBoxesCleanedUp(); err != nil {
			slog.Error("boxes not cleaned up", "error", err)
			checkErr = err
			// Continue with cleanup even if check failed
		}
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
	// Cleanup exelet (remote or local)
	if e.exelet.RemoteHost != "" {
		// Stop SSH tunnel if running
		if e.exelet.TunnelCancel != nil {
			e.exelet.TunnelCancel()
		}
		if e.exelet.TunnelCmd != nil && e.exelet.TunnelCmd.Process != nil {
			e.exelet.TunnelCmd.Process.Kill()
			e.exelet.TunnelCmd.Wait()
		}

		// Remote cleanup
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// remove instances
		if exeletClient != nil {
			defer exeletClient.Close()
			slog.Info("cleaning up instances")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := CleanupTestInstances(ctx, exeletClient); err != nil {
				slog.Error("instance cleanup failed", "error", err)
			}
		}

		// Remove remote binary
		if _, err := sshExec(ctx, e.exelet.RemoteHost, fmt.Sprintf("rm -f /tmp/exelet-test /tmp/exelet-test-%s.log", testRunID)); err != nil {
			slog.Error("failed to cleanup remote exelet binary", "error", err)
		}

		// Remove local binary
		os.Remove(filepath.Join(os.TempDir(), "exelet-test"))
	}

	// stop proxies
	e.sshProxy.close()
	if e.exedHTTPProxy != nil {
		e.exedHTTPProxy.close()
	}

	// CoverDir should always be non-empty, but maybe if exed failed to start?
	// Avoid duplicate/confusing errors by just skipping in this case.
	if e.exed.CoverDir != "" {
		// Extract "legacy" text format Go coverage profile to standard location
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i", e.exed.CoverDir, "-o", "e1e.cover")
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Error("failed to write exed coverage profile", "cmd", cmd.String(), "error", err, "output", string(out))
		}
	}
	return checkErr
}

func (e *testEnv) checkBoxesCleanedUp() error {
	url := fmt.Sprintf("http://localhost:%d/debug/boxes?format=json", e.exed.HTTPPort)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to check boxes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code checking boxes: %d", resp.StatusCode)
	}

	var boxes []struct {
		Host   string `json:"host"`
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&boxes); err != nil {
		return fmt.Errorf("failed to decode boxes JSON: %w", err)
	}

	// Log all boxes for debugging
	if len(boxes) > 0 {
		var allBoxNames []string
		for _, box := range boxes {
			allBoxNames = append(allBoxNames, box.Name)
		}
		slog.Info("boxes at cleanup", "boxes", allBoxNames)
	}

	// Only check boxes from this specific test run
	boxPrefix := fmt.Sprintf("e1e-%s-", testRunID)
	var e1eBoxes []string
	for _, box := range boxes {
		if strings.HasPrefix(box.Name, boxPrefix) {
			e1eBoxes = append(e1eBoxes, box.Name)
		}
	}

	if len(e1eBoxes) > 0 {
		return fmt.Errorf("e1e boxes not cleaned up: %v", e1eBoxes)
	}

	return nil
}

type tcpProxy struct {
	name string
	ln   net.Listener
	tcp  *net.TCPAddr
	dst  atomic.Pointer[net.TCPAddr]
}

func newTCPProxy(name string) (*tcpProxy, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	return &tcpProxy{name: name, ln: ln, tcp: tcpAddr}, nil
}

func (p *tcpProxy) serve() error {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			// Block the dialer until destination address is set.
			// We shouldn't have to do this, but live with it for now.
			// TODO: figure out why we're seeing connections before setDestPort is called, and stop doing that.
			var dstAddr *net.TCPAddr
			// Poll. Why not. Cheap enough, and simpler than a condvar.
			for {
				dstAddr = p.dst.Load()
				if dstAddr != nil {
					break
				}
				slog.Info("tcpProxy: waiting for destination address", "name", p.name, "listener_addr", p.ln.Addr())
				time.Sleep(50 * time.Millisecond)
			}
			dst, err := net.Dial("tcp", dstAddr.String())
			if err != nil {
				slog.Error("tcpProxy: failed to connect to dst", "name", p.name, "address", dstAddr, "error", err)
				return
			}
			var wg sync.WaitGroup
			wg.Go(func() { io.Copy(dst, c) })
			wg.Go(func() { io.Copy(c, dst) })
			wg.Wait()
		}()
	}
}

func (p *tcpProxy) setDestPort(port int) {
	p.dst.Store(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
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
		exedSlogErrC:     make(chan string, 16),
		exeletSlogErrC:   make(chan string, 16),
		exedGuidLogC:     make(chan string, 128),
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
	sshProxy, err := newTCPProxy("sshProxy")
	if err != nil {
		return nil, fmt.Errorf("failed to create ssh proxy: %w", err)
	}
	go sshProxy.serve()
	env.sshProxy = sshProxy
	env.addCanonicalization(sshProxy.tcp.Port, "SSH_PORT")
	if *flagVerbosePorts {
		slog.Info("ssh proxy listening", "port", sshProxy.tcp.Port)
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

	// resolve the gateway to pass to exed
	gateway := resolveGateway(ctrHost)
	if gateway == "" {
		return env, fmt.Errorf("unable to resolve default gateway for %q", ctrHost)
	}
	slog.Info("resolved default gateway", "addr", gateway)

	// We have a circular dependency: exelet needs to know exed's HTTP port,
	// but exed needs to know exelet's address. Use the same proxy trick as for sshpiper.
	// Start a TCP proxy for exed HTTP that we can give to exelet immediately.
	exedHTTPProxy, err := newTCPProxy("exedHTTPProxy")
	if err != nil {
		return env, fmt.Errorf("failed to create exed HTTP proxy: %w", err)
	}
	go exedHTTPProxy.serve()
	env.exedHTTPProxy = exedHTTPProxy
	if *flagVerbosePorts {
		slog.Info("exed HTTP proxy listening", "port", exedHTTPProxy.tcp.Port)
	}

	// Test if remote host can reach local proxy
	// Usually local->ssh_ctr and ssh_ctr->local connectivity works. However, in some
	// environments, such as coding agents that operate in containers, this connectivity
	// does NOT work, and we set up an SSH tunnel for the exelet->exed communication
	// as a band-aid.
	host := parseSSHHost(ctrHost)
	hasConnectivity := testRemoteToLocalConnectivity(host, gateway, exedHTTPProxy.tcp.Port)
	slog.Info("tested remote->local connectivity", "host", host, "gateway", gateway, "port", exedHTTPProxy.tcp.Port, "reachable", hasConnectivity)
	needsTunnel := !hasConnectivity

	// Determine the exedURL for exelet
	var exedProxyURL string
	var tunnelCmd *exec.Cmd
	var tunnelCancel context.CancelFunc

	if needsTunnel {
		slog.Info("remote->local connectivity not available, using SSH reverse tunnel")
		// Use SSH reverse tunnel: exelet -> SSH tunnel -> TCP proxy -> exed
		remotePort, cmd, cancel, err := startSSHTunnel(host, exedHTTPProxy.tcp.Port)
		if err != nil {
			return env, fmt.Errorf("failed to start SSH tunnel: %w", err)
		}
		tunnelCmd = cmd
		tunnelCancel = cancel
		exedProxyURL = fmt.Sprintf("http://localhost:%d", remotePort)
		if *flagVerbosePorts {
			slog.Info("using SSH tunnel for exelet->exed", "remote_port", remotePort, "proxy_port", exedHTTPProxy.tcp.Port)
		}
	} else {
		// Use direct gateway access via TCP proxy
		exedProxyURL = fmt.Sprintf("http://%s:%d", gateway, exedHTTPProxy.tcp.Port)
	}

	// Start exelet with the proxy port
	exelet, err := startExelet(ctrHost, gateway, exedProxyURL, env.exeletSlogErrC)
	if err != nil {
		if tunnelCancel != nil {
			tunnelCancel()
		}
		return env, err
	}
	exelet.TunnelCmd = tunnelCmd
	exelet.TunnelCancel = tunnelCancel
	env.exelet = *exelet
	env.addCanonicalization(exelet.Address, "EXELET_ADDRESS")

	// TODO: build piperd concurrently with starting exed for faster startup
	// Pass "0,0" to let the proxy listeners allocate their own port numbers
	ei, err := startExed(ctrHost, es.port, sshProxy.tcp.Port, []int{0, 0}, exelet.Address, gateway, env.exedSlogErrC, env.exedGuidLogC)
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

	// proxy SSH requests to piperd
	env.sshProxy.setDestPort(pi.SSHPort)

	// Now that exed is running, point the HTTP proxy to the real exed HTTP port
	env.exedHTTPProxy.setDestPort(ei.HTTPPort)

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
	piperdCmd.Dir = filepath.Join("..", "deps", "sshpiper") // run from sshpiper dir so it finds its go.mod

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

	timeout := time.Minute
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

// testRemoteToLocalConnectivity checks if the remote host can reach the local port via gateway.
// Returns true if connectivity works, false otherwise.
func testRemoteToLocalConnectivity(host, gateway string, port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Try to connect from remote host to gateway:port
	testCmd := fmt.Sprintf("timeout 2 nc -z %s %d 2>/dev/null", gateway, port)
	_, err := sshExec(ctx, host, testCmd)
	return err == nil
}

// startSSHTunnel establishes an SSH reverse tunnel and returns the dynamically allocated remote port.
// Uses -v flag to capture SSH debug output showing the allocated port.
func startSSHTunnel(host string, localPort int) (remotePort int, tunnelCmd *exec.Cmd, cancel context.CancelFunc, err error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Start SSH tunnel with -v to capture allocated port
	tunnelCmd = exec.CommandContext(ctx, "ssh",
		"-v", // verbose to see allocated port
		"-N", // no command
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=30",
		"-o", "ExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("0:localhost:%d", localPort),
		host,
	)

	// Capture stderr to parse allocated port
	stderrPipe, err := tunnelCmd.StderrPipe()
	if err != nil {
		cancel()
		return 0, nil, nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := tunnelCmd.Start(); err != nil {
		cancel()
		return 0, nil, nil, fmt.Errorf("failed to start SSH tunnel: %w", err)
	}

	// Parse stderr for "Allocated port X for remote forward"
	scanner := bufio.NewScanner(stderrPipe)
	portC := make(chan int, 1)
	go func() {
		re := regexp.MustCompile(`Allocated port (\d+) for remote forward`)
		for scanner.Scan() {
			line := scanner.Text()
			if matches := re.FindStringSubmatch(line); len(matches) > 1 {
				if port, err := strconv.Atoi(matches[1]); err == nil {
					portC <- port
					return
				}
			}
		}
	}()

	// Wait for port allocation (with timeout)
	select {
	case remotePort = <-portC:
		slog.Info("SSH tunnel established", "remote_port", remotePort, "local_port", localPort)
		return remotePort, tunnelCmd, cancel, nil
	case <-time.After(5 * time.Second):
		tunnelCmd.Process.Kill()
		cancel()
		return 0, nil, nil, fmt.Errorf("timeout waiting for SSH tunnel to allocate port")
	}
}

func startExelet(ctrHost, gateway, exedURL string, exeletSlogErrC chan string) (*exeletInstance, error) {
	start := time.Now()
	slog.Info("starting exelet", "exedURL", exedURL)

	// ctrHost is like "ssh://lima-exe-ctr-tests"
	host := parseSSHHost(ctrHost)
	if strings.HasPrefix(host, "ssh://") {
		slog.Error("invalid ctrHost: %s", "x", ctrHost)
	}
	if host == "" {
		return nil, fmt.Errorf("exelet requires remote host (ctrHost)")
	}

	// Build exelet binary locally
	slog.Info("building exelet binary")
	binPath, err := buildExeletBinary()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	// Ensure no existing binaries exist (e.g. on failed re-run)
	if _, err := sshExec(ctx, host, "rm -rf /tmp/exelet-test"); err != nil {
		return nil, fmt.Errorf("failed to remove existing exelet: %w", err)
	}

	// Ensure no existing processes exist
	sshExec(ctx, host, "pkill -f exelet-test")

	// Upload binary to remote host
	slog.Info("uploading exelet to remote host", "host", host)
	if err := scpUpload(binPath, host, "/tmp/exelet-test"); err != nil {
		return nil, fmt.Errorf("failed to upload exelet: %w", err)
	}

	// Make binary executable
	if _, err := sshExec(ctx, host, "chmod +x /tmp/exelet-test"); err != nil {
		return nil, fmt.Errorf("failed to chmod exelet: %w", err)
	}

	// Create remote data directory
	dataDir, err := sshExec(ctx, host, "mktemp -d /tmp/exelet_test_data_XXXXX")
	if err != nil {
		return nil, fmt.Errorf("failed to create remote data dir: %w", err)
	}
	dataDir = strings.TrimSpace(dataDir)

	// Start exelet on remote host in background with sudo (needs privileges for bridge setup)
	// Use a wrapper to ensure we can capture the PID even if the process fails immediately
	// Use proxy port range 30000-40000 for e1e tests to avoid conflicts with dev (10000-20000) and unit tests (20000-30000)
	startCmd := fmt.Sprintf(`sudo LOG_FORMAT=json nohup /tmp/exelet-test --debug --listen-address tcp://0.0.0.0:0 --http-addr :0 --data-dir /data/exelet --runtime-address cloudhypervisor:///data/exelet/runtime --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" --network-manager-address nat:///data/exelet/network --proxy-port-min 30000 --proxy-port-max 40000 --exed-url %s > /tmp/exelet-test-%s.log 2>&1 &`,
		exedURL, testRunID)
	out, err := sshExec(ctx, host, startCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start remote exelet: %s (%w)\n", string(out), err)
	}

	// Wait for exelet to start and extract address from logs
	slog.Info("waiting for exelet to start on remote host")
	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	startTime := time.Now()
	var addr string
	for addr == "" && time.Since(startTime) < timeout {
		time.Sleep(500 * time.Millisecond)

		// Tail the log file
		logOut, err := sshExec(ctx, host, fmt.Sprintf("cat /tmp/exelet-test-%s.log 2>/dev/null || true", testRunID))
		if err != nil {
			continue
		}

		if *flagVerboseExelet {
			fmt.Fprintln(logFileFor("exelet"), logOut)
		}

		// Parse each line looking for "listening" message and ERROR logs
		scanner := bufio.NewScanner(strings.NewReader(logOut))
		for scanner.Scan() {
			line := scanner.Bytes()
			if !json.Valid(line) {
				slog.Warn("log line not valid json", "error", err, "line", string(line))
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				slog.Warn("failed to parse exelet log line", "error", err, "line", string(line))
				continue
			}
			// Capture ERROR level logs
			if level, ok := entry["level"].(string); ok && level == "ERROR" {
				lineStr := string(line)
				select {
				case exeletSlogErrC <- lineStr:
				default:
				}
			}
			if entry["msg"] == "listening" {
				if addrVal, ok := entry["addr"].(string); ok {
					addr = addrVal
					break
				}
			}
		}
	}

	if addr == "" {
		// Cleanup on failure
		sshExec(ctx, host, "sudo pkill -9 -f exelet-test")
		logOut, _ := sshExec(ctx, host, fmt.Sprintf("cat /tmp/exelet-test-%s.log 2>/dev/null || true", testRunID))
		return nil, fmt.Errorf("timeout waiting for exelet to start. Last log output:\n%s", logOut)
	}

	// Parse address to replace 0.0.0.0 with actual remote IP
	// addr is like "tcp://0.0.0.0:45678"
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse exelet address: %w", err)
	}

	// Construct the actual address
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse port from %s: %w", u.Host, err)
	}
	finalAddr := fmt.Sprintf("tcp://%s:%s", host, port)

	instance := &exeletInstance{
		Address:    finalAddr,
		DataDir:    dataDir,
		RemoteHost: host,
	}

	slog.Info("started remote exelet", "elapsed", time.Since(start).Truncate(100*time.Millisecond), "addr", finalAddr)
	return instance, nil
}

func startExed(ctrHost string, emailServerPort, piperPort int, extraProxyPorts []int, exeletAddr, gateway string, exedSlogErrC, exedGuidLogC chan string) (*exedInstance, error) {
	start := time.Now()
	slog.Info("starting exed")
	// Choose binary: use PREBUILT_EXED if provided, otherwise build a temp binary.
	var binPath string
	if prebuilt := os.Getenv("PREBUILT_EXED"); prebuilt != "" {
		st, err := os.Stat(prebuilt)
		if err != nil {
			return nil, fmt.Errorf("PREBUILT_EXED not usable: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("PREBUILT_EXED points to a directory, need a file: %s", prebuilt)
		}
		binPath = prebuilt
	} else {
		bin, err := os.CreateTemp("", "exed_test_bin_*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		bin.Close()
		binPath = bin.Name()
		buildCmd := exec.Command("go", "build", "-race", "-cover", "-covermode=atomic", "-coverpkg=exe.dev/...", "-o", binPath, "../cmd/exed")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to build exed: %w\n%s", err, out)
		}
	}

	shm := "/dev/shm"
	if st, err := os.Stat(shm); err != nil || !st.IsDir() {
		shm = ""
	}
	dbPath, err := os.CreateTemp(shm, "exed_test_*.db")
	if err != nil {
		return nil, err
	}
	dbPath.Close()

	coverDir, err := os.MkdirTemp("", "e1e-exed-cov-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create coverage dir: %w", err)
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
		"-exelet-addresses="+exeletAddr,
		"-gateway="+gateway,
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
			lineStr := string(line)
			if *flagVerboseExed {
				fmt.Fprintln(logFileFor("exed"), lineStr)
			}
			if seenPanic {
				fmt.Println(lineStr)
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
				if guidRegex.MatchString(lineStr) {
					select {
					case exedGuidLogC <- lineStr:
					default:
					}
				}
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				fmt.Fprintf(os.Stderr, "failed to parse log line: %v\n", err)
				continue
			}
			if fmt.Sprint(entry["level"]) == "ERROR" {
				select {
				case exedSlogErrC <- lineStr:
				default:
				}
			}
			if guid, _ := entry["guid"].(string); guid != "" {
				select {
				case exedGuidLogC <- lineStr:
				default:
				}
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

	timeout := time.Minute
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
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
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
	banner       = "~~~ EXE.DEV ~~~"
	exeDevPrompt = "\033[1;36mexe.dev\033[0m \033[37m▶\033[0m "
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

func (p *expectPty) deleteBox(boxName string) {
	p.t.Helper()
	p.sendLine("rm " + boxName)
	p.want("Deleting")
	p.reject("internal error")
	p.want("success")
	p.wantPrompt()
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
		expect.WithDefaultRefreshingTimeout(time.Minute),
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
	pty.prompt = exeDevPrompt
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

func runParseExeDevJSON[T any](t *testing.T, keyFile string, args ...string) T {
	t.Helper()
	var result T
	out, err := runExeDevSSHCommand(t, keyFile, args...)
	if err != nil {
		t.Fatalf("failed to run command: %v\n%s", err, out)
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse command output as JSON: %v\n%s", err, out)
	}
	return result
}

func boxSSHCommand(t *testing.T, boxname, keyFile string, args ...string) *exec.Cmd {
	sshArgs := baseSSHArgs(boxname, keyFile)
	sshArgs = append(sshArgs, args...)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	return sshCmd
}

// waitForSSH blocks until SSH is responsive on the given box.
func waitForSSH(t *testing.T, boxName, keyFile string) {
	// Wait for SSH to be responsive (systemd may take time to initialize).
	var err error
	for range 150 {
		err = boxSSHCommand(t, boxName, keyFile, "true").Run()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("box ssh did not come up, last error: %v", err)
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

var boxCounter atomic.Int32

// boxName creates a unique test-specific box name with e1e prefix for easy cleanup
func boxName(t *testing.T) string {
	t.Helper()
	// Create a unique test-specific box name: "e1e-{runid}-{counter}-{testname}"
	// runid provides cross-process uniqueness.
	// counter covers within-process uniqueness.
	// e1e prefix and testname are for debuggability.
	counter := fmt.Sprintf("%04x", boxCounter.Add(1))
	testName := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	// Sanitize to allowed charset [a-z0-9-] to satisfy isValidBoxName
	testName = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(testName, "-")
	// Collapse multiple hyphens and trim
	testName = regexp.MustCompile(`-+`).ReplaceAllString(testName, "-")
	testName = strings.Trim(testName, "-")
	boxName := fmt.Sprintf("e1e-%s-%s-%s", testRunID, counter, testName)
	Env.addCanonicalization(boxName, "BOX_NAME")
	return boxName
}

func registerForExeDevWithEmail(t *testing.T, email string) (pty *expectPty, cookies []*http.Cookie, keyFile, returnedEmail string) {
	keyFile, publicKey := genSSHKey(t)
	pty = sshToExeDev(t, keyFile)
	pty.want(banner)

	pty.want("Please enter your email")
	pty.sendLine(email)
	returnedEmail = email
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	pty.wantRe("Pairing code: .*[0-9]{6}.*")

	emailMsg := Env.email.waitForEmail(t, email)
	cookies = clickVerifyLinkInEmail(t, emailMsg)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.wantPrompt()

	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()

	return pty, cookies, keyFile, returnedEmail
}

// registerForExeDev is a convenience command to register for an exe.dev account.
// It returns the open pty after registration, authentication cookies for HTTP access,
// the private keyFile, and the account email.
// It is the caller's responsibility to call pty.disconnect() when done.
func registerForExeDev(t *testing.T) (pty *expectPty, cookies []*http.Cookie, keyFile, email string) {
	name := t.Name()
	name = strings.ReplaceAll(name, "/", ".")
	email = name + "@example.com"
	return registerForExeDevWithEmail(t, email)
}

func setCookiesForJar(t testing.TB, jar *cookiejar.Jar, rawURL string, cookies []*http.Cookie) {
	t.Helper()
	if len(cookies) == 0 || jar == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse URL %q: %v", rawURL, err)
	}
	cloned := make([]*http.Cookie, len(cookies))
	for i, c := range cookies {
		cCopy := *c
		cloned[i] = &cCopy
	}
	jar.SetCookies(u, cloned)
}

func noRedirectClient(jar http.CookieJar) *http.Client {
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
	// Calls to action
	pty.want("App")
	pty.want("http://")
	pty.want("SSH")
	pty.wantf("ssh -p %v %v@localhost", Env.sshPort(), boxName)

	// Confirm it is there.
	pty.sendLine("ls")
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
	for len(lines) > 0 && lines[len(lines)-1] == "" {
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
// We use this for tests that satisfy one or both of these conditions:
//   - are hard to get stable output out of (but prefer to use canonicalization if at all possible)
//   - whose golden output isn't interesting/useful
func noGolden(t *testing.T) {
	skipGolden.Store(t.Name(), true)
}

// resolveGateway uses SSH to the given ctrhost to resolve its _gateway hostname.
// If we're SSH'ing laptop->lima-exe-ctr, this gives us the address that lima-exe-ctr
// can talk to laptop on.
func resolveGateway(ctrhost string) string {
	host := parseSSHHost(ctrhost)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "UserKnownHostsFile=/dev/null",
		host, "getent ahostsv4 _gateway 2>/dev/null | awk '{print $1; exit}'",
	)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
