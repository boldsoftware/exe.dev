package testinfra

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// ExeletInstance describes a single running exelet.
type ExeletInstance struct {
	Address      string                // e.g., "tcp://192.168.5.15:9080"
	HTTPAddress  string                // e.g., "http://192.168.5.15:9081"
	Exited       <-chan struct{}       // closed when Cmd exits
	Cause        func() error          // why context was canceled
	Cmd          *exec.Cmd             // SSH command running exelet
	CmdCancel    context.CancelFunc    // cancel function for exelet context
	DataDir      string                // temp directory for exelet data (local or remote path)
	RemoteHost   string                // VM on which exelet is running
	TunnelCmd    *exec.Cmd             // SSH tunnel process if using reverse tunnel
	TunnelCancel context.CancelFunc    // cancel function for tunnel context
	BridgeName   string                // bridge name for network isolation
	ZFSDataset   string                // ZFS dataset for storage isolation
	CoverDir     string                // remote directory for Go coverage artifacts (GOCOVERDIR)
	Errors       chan string           // exelet errors are sent on this channel
	Client       func() *client.Client // function returns exelet control client

	testRunID        string    // argument to StartExelet
	exeletLoggerDone chan bool // closed when logging goroutine done
}

// exeletLogWatcher watches exelet output and extracts addresses and errors.
type exeletLogWatcher struct {
	grpcAddrC chan string
	httpAddrC chan string
	errorsC   chan string
	done      chan bool
	teeMu     sync.Mutex
	tee       *bytes.Buffer
}

func newExeletLogWatcher() *exeletLogWatcher {
	return &exeletLogWatcher{
		grpcAddrC: make(chan string, 1),
		httpAddrC: make(chan string, 1),
		errorsC:   make(chan string, 16),
		done:      make(chan bool),
		tee:       new(bytes.Buffer),
	}
}

// start begins watching the output reader in a goroutine.
func (w *exeletLogWatcher) start(ctx context.Context, r io.Reader, logFile io.Writer) {
	go func() {
		defer close(w.done)
		scan := bufio.NewScanner(r)
		for scan.Scan() {
			line := scan.Bytes()
			w.teeMu.Lock()
			w.tee.Write(line)
			w.tee.WriteString("\n")
			w.teeMu.Unlock()

			if logFile != nil {
				fmt.Fprintf(logFile, "%s\n", line)
			}

			if !json.Valid(line) {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}

			if level, ok := entry["level"].(string); ok && level == "ERROR" {
				select {
				case w.errorsC <- string(line):
				default:
				}
			}

			switch entry["msg"] {
			case "listening":
				if addrVal, ok := entry["addr"].(string); ok {
					select {
					case w.grpcAddrC <- addrVal:
					default:
					}
				}
			case "http server listening":
				if addrVal, ok := entry["addr"].(string); ok {
					select {
					case w.httpAddrC <- addrVal:
					default:
					}
				}
			}
		}
		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.WarnContext(ctx, "scanning exelet output failed", "error", err)
		}
	}()
}

// waitForAddresses waits for both gRPC and HTTP addresses with timeout.
func (w *exeletLogWatcher) waitForAddresses(ctx context.Context, cleanup func()) (grpcAddr, httpAddr string, err error) {
	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case grpcAddr = <-w.grpcAddrC:
			if httpAddr != "" {
				return grpcAddr, httpAddr, nil
			}
		case httpAddr = <-w.httpAddrC:
			if grpcAddr != "" {
				return grpcAddr, httpAddr, nil
			}
		case <-timer.C:
			cleanup()
			w.teeMu.Lock()
			lastOutput := w.tee.String()
			w.teeMu.Unlock()
			return "", "", fmt.Errorf("timeout waiting for exelet to start. Last log output:\n%s", lastOutput)
		}
	}
}

// parseAndCreateClient extracts ports from addresses and creates the client.
// host is used to construct the final address (e.g., "localhost" or a remote hostname).
func parseAndCreateClient(ctx context.Context, grpcAddr, httpAddr, host string) (finalAddr, finalHTTPAddr string, readClient func() *client.Client, err error) {
	u, err := url.Parse(grpcAddr)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to parse exelet address: %w", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to parse port from %s: %w", u.Host, err)
	}
	finalAddr = fmt.Sprintf("tcp://%s:%s", host, port)

	_, httpPort, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to parse http port from %s: %w", httpAddr, err)
	}
	finalHTTPAddr = fmt.Sprintf("http://%s:%s", host, httpPort)

	clientC := make(chan *client.Client, 1)
	go func() {
		c, err := client.NewClient(finalAddr, client.WithInsecure())
		if err != nil {
			slog.ErrorContext(ctx, "failed to create exelet client", "error", err)
		}
		clientC <- c
	}()

	readClient = sync.OnceValue(func() *client.Client {
		return <-clientC
	})

	return finalAddr, finalHTTPAddr, readClient, nil
}

// StartExelet starts the exelet process in the VM at ctrHost.
//
// exeletBinary is the path to the exelet binary.
//
// ctrHost is an SSH path in the form ssh://USER@ADDR.
//
// exedPort is the port number on the local host that the exed
// HTTP server is listening on.
//
// testRunID is a unique string for this invocation.
//
// logFile, if not nil, is a file to write logs to.
//
// logPorts is whether to log port numbers using slog.InfoContext.
func StartExelet(ctx context.Context, exeletBinary, ctrHost string, exedPort int, testRunID string, logFile io.Writer, logPorts bool) (ei *ExeletInstance, err error) {
	start := time.Now()
	slog.InfoContext(ctx, "starting exelet", "ID", testRunID)

	// ctrHost is like "ssh://lima-exe-ctr-tests" or "localhost"
	host := strings.TrimPrefix(ctrHost, "ssh://")
	if strings.HasPrefix(host, "ssh://") {
		slog.ErrorContext(ctx, "invalid ctrHost", "ctrHost", ctrHost)
	}
	if host == "" {
		return nil, fmt.Errorf("exelet requires remote host; set CTR_HOST environment variable")
	}

	// For localhost, run exelet directly without SSH
	if host == "localhost" {
		return startExeletLocal(ctx, exeletBinary, exedPort, testRunID, logFile, logPorts, start)
	}

	// Get the gateway address of the VM.
	gateway, err := resolveGateway(ctx, host)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "resolved default gateway", "addr", gateway)

	// Test if the VM can reach the local proxy.
	// Usually local->VM and VM->local connectivity works.
	// However, in some environments, such as coding agents that
	// operate in containers, this connectivity does NOT work,
	// and we set up an SSH tunnel for exelet->exed communication
	// as a band-aid.
	hasConnectivity := testRemoteToLocalConnectivity(ctx, host, gateway, exedPort)
	slog.InfoContext(ctx, "test remote->local connectivity", "host", host, "gateway", gateway, "port", exedPort, "reachable", hasConnectivity)

	// Determine the URL the exelet will use to reach exed.
	needsTunnel := !hasConnectivity
	var exedProxyURL string
	var tunnelCmd *exec.Cmd
	var tunnelCancel context.CancelFunc
	if !needsTunnel {
		exedProxyURL = fmt.Sprintf("http://%s:%d", gateway, exedPort)
	} else {
		slog.InfoContext(ctx, "remote->local connectivity not available, using SSH reverse tunnel")
		// Use SSH reverse tunnel:
		// exelet -> SSH tunnel -> TCP proxy -> exed
		remotePort, cmd, cancel, err := startSSHTunnel(ctx, host, exedPort)
		if err != nil {
			return nil, fmt.Errorf("failed to start SSH tunnel: %w", err)
		}

		defer func() {
			if err != nil {
				cancel()
				cmd.Process.Kill()
				cmd.Wait()
			}
		}()

		tunnelCmd = cmd
		tunnelCancel = cancel

		exedProxyURL = fmt.Sprintf("http://localhost:%d", remotePort)

		if logPorts {
			slog.InfoContext(ctx, "using SSH tuennl for exelet->exed", "remote_port", remotePort, "proxy_port", exedPort)
		}
	}

	// Use test-run-specific binary name to avoid conflicts with
	// parallel test runs. This is the name of the exelet binary
	// on the VM, not on the host running the test.

	remoteBinaryPath := "/tmp/exelet-test-" + testRunID

	// Ensure no existing binaries exist for this test run
	// (e.g. on failed re-run).
	if out, err := sshExec(ctx, host, fmt.Sprintf("rm -rf %s", remoteBinaryPath)); err != nil {
		return nil, fmt.Errorf("failed to remove existing exelet: %w\n%s", err, out)
	}

	// Ensure no existing processes exist for this test run only.
	sshExec(ctx, host, fmt.Sprintf("pkill -f %s", remoteBinaryPath))

	// Upload binary to remote host with unique name.
	slog.InfoContext(ctx, "uploading exelet to remote host", "host", host, "path", remoteBinaryPath)
	if out, err := scpUpload(ctx, exeletBinary, host, remoteBinaryPath); err != nil {
		return nil, fmt.Errorf("failed to upload exelet: %w\n%s", err, out)
	}

	// Make binary executable
	if out, err := sshExec(ctx, host, fmt.Sprintf("chmod +x %s", remoteBinaryPath)); err != nil {
		return nil, fmt.Errorf("failed to chmod exelet: %w\n%s", err, out)
	}

	// Compute unique resource names for this test run
	res, err := computeExeletResources(testRunID)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "using isolated resources", "bridge", res.bridgeName, "network", res.networkCIDR, "dataset", res.zfsDataset)

	// Setup ZFS dataset and directories
	if err := res.setup(ctx, sshExecutor(host), testRunID); err != nil {
		return nil, err
	}

	// URL-encode the network CIDR since it contains slashes
	encodedNetwork := url.QueryEscape(res.networkCIDR)

	args := []string{
		"sudo",
		"GOCOVERDIR=" + res.coverDir,
		"LOG_FORMAT=json",
		remoteBinaryPath,
		"--debug",
		"--stage", "test",
		"--listen-address", "tcp://0.0.0.0:0",
		"--http-addr", ":0",
		"--data-dir", res.dataDir,
		"--runtime-address", "cloudhypervisor:///" + res.dataDir + "/runtime",
		"--storage-manager-address", "zfs:///" + res.dataDir + "/storage?dataset=" + res.zfsDataset,
		"--network-manager-address", `"nat:///` + res.dataDir + `/network?bridge=` + res.bridgeName + `&network=` + encodedNetwork + `"`,
		"--proxy-port-min", strconv.Itoa(res.proxyPortMin),
		"--proxy-port-max", strconv.Itoa(res.proxyPortMax),
		"--resource-manager-interval", "5s",
		"--idle-threshold", "10m",
		"--exed-url", exedProxyURL,
		"--enable-hugepages",
	}
	slog.DebugContext(ctx, "starting exelet", "cmd", args)

	exeletCtx, exeletCancel := context.WithCancel(ctx)

	// Start exelet via SSH (similar to how exed is started locally)
	exeletCmd := exec.CommandContext(exeletCtx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		host, strings.Join(args, " "))

	cmdOut, err := exeletCmd.StdoutPipe()
	if err != nil {
		exeletCancel()
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	exeletCmd.Stderr = exeletCmd.Stdout

	if err := exeletCmd.Start(); err != nil {
		exeletCancel()
		return nil, fmt.Errorf("failed to start remote exelet: %w", err)
	}

	go func() {
		exeletCmd.Wait()
		exeletCancel()
	}()

	// Parse output to find addresses
	watcher := newExeletLogWatcher()
	watcher.start(ctx, cmdOut, logFile)

	// Wait for exelet to start and extract addresses
	slog.InfoContext(ctx, "waiting for exelet to start on remote host")
	grpcAddr, httpAddr, err := watcher.waitForAddresses(ctx, func() {
		exeletCmd.Process.Kill()
		exeletCancel()
	})
	if err != nil {
		return nil, err
	}

	// Parse addresses and create client
	finalAddr, finalHTTPAddr, readClient, err := parseAndCreateClient(ctx, grpcAddr, httpAddr, host)
	if err != nil {
		return nil, err
	}

	cause := sync.OnceValue(func() error {
		return context.Cause(exeletCtx)
	})

	instance := &ExeletInstance{
		Address:          finalAddr,
		HTTPAddress:      finalHTTPAddr,
		Exited:           exeletCtx.Done(),
		Cause:            cause,
		Cmd:              exeletCmd,
		CmdCancel:        exeletCancel,
		DataDir:          res.dataDir,
		RemoteHost:       host,
		TunnelCmd:        tunnelCmd,
		TunnelCancel:     tunnelCancel,
		BridgeName:       res.bridgeName,
		ZFSDataset:       res.zfsDataset,
		CoverDir:         res.coverDir,
		Errors:           watcher.errorsC,
		Client:           readClient,
		testRunID:        testRunID,
		exeletLoggerDone: watcher.done,
	}

	AddCanonicalization(instance.Address, "EXELET_ADDRESS")
	AddCanonicalization(instance.HTTPAddress, "EXELET_HTTP_ADDRESS")

	slog.InfoContext(ctx, "started remote exelet", "elapsed", time.Since(start).Truncate(100*time.Millisecond), "addr", finalAddr, "http_addr", finalHTTPAddr)
	return instance, nil
}

// startExeletLocal starts exelet locally (for CTR_HOST=localhost).
func startExeletLocal(ctx context.Context, exeletBinary string, exedPort int, testRunID string, logFile io.Writer, logPorts bool, start time.Time) (*ExeletInstance, error) {
	// For localhost, exelet can directly reach exed via localhost
	exedProxyURL := fmt.Sprintf("http://localhost:%d", exedPort)
	slog.InfoContext(ctx, "using localhost for exelet->exed", "port", exedPort)

	// Compute unique resource names for this test run
	res, err := computeExeletResources(testRunID)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "using isolated resources", "bridge", res.bridgeName, "network", res.networkCIDR, "dataset", res.zfsDataset)

	// Kill any existing processes for this test run only
	exec.Command("sudo", "pkill", "-f", "exelet-test").Run()

	// Setup ZFS dataset and directories
	if err := res.setup(ctx, localExecutor(), testRunID); err != nil {
		return nil, err
	}

	encodedNetwork := url.QueryEscape(res.networkCIDR)

	localCmd := fmt.Sprintf(`sudo GOCOVERDIR=%s LOG_FORMAT=json %s --debug --stage test --listen-address tcp://0.0.0.0:0 --http-addr :0 --data-dir %s --runtime-address cloudhypervisor:///%s/runtime --storage-manager-address "zfs:///%s/storage?dataset=%s" --network-manager-address "nat:///%s/network?bridge=%s&network=%s&disable_bandwidth=true" --proxy-port-min %d --proxy-port-max %d --resource-manager-interval 5s --idle-threshold 10m --exed-url %s --enable-hugepages`,
		res.coverDir, exeletBinary, res.dataDir, res.dataDir, res.dataDir, res.zfsDataset, res.dataDir, res.bridgeName, encodedNetwork, res.proxyPortMin, res.proxyPortMax, exedProxyURL)

	// Start exelet directly
	exeletCtx, exeletCancel := context.WithCancel(ctx)
	exeletCmd := exec.CommandContext(exeletCtx, "bash", "-c", localCmd)

	cmdOut, err := exeletCmd.StdoutPipe()
	if err != nil {
		exeletCancel()
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	exeletCmd.Stderr = exeletCmd.Stdout

	if err := exeletCmd.Start(); err != nil {
		exeletCancel()
		return nil, fmt.Errorf("failed to start local exelet: %w", err)
	}

	go func() {
		exeletCmd.Wait()
		exeletCancel()
	}()

	// Parse output to find addresses
	watcher := newExeletLogWatcher()
	watcher.start(ctx, cmdOut, logFile)

	slog.InfoContext(ctx, "waiting for exelet to start on localhost")
	grpcAddr, httpAddr, err := watcher.waitForAddresses(ctx, func() {
		exeletCmd.Process.Kill()
		exeletCancel()
	})
	if err != nil {
		return nil, err
	}

	// Parse addresses and create client
	finalAddr, finalHTTPAddr, readClient, err := parseAndCreateClient(ctx, grpcAddr, httpAddr, "localhost")
	if err != nil {
		return nil, err
	}

	cause := sync.OnceValue(func() error {
		return context.Cause(exeletCtx)
	})

	instance := &ExeletInstance{
		Address:          finalAddr,
		HTTPAddress:      finalHTTPAddr,
		Exited:           exeletCtx.Done(),
		Cause:            cause,
		Cmd:              exeletCmd,
		CmdCancel:        exeletCancel,
		DataDir:          res.dataDir,
		RemoteHost:       "", // Empty for localhost
		BridgeName:       res.bridgeName,
		ZFSDataset:       res.zfsDataset,
		CoverDir:         res.coverDir,
		Errors:           watcher.errorsC,
		Client:           readClient,
		testRunID:        testRunID,
		exeletLoggerDone: watcher.done,
	}

	slog.InfoContext(ctx, "started local exelet", "elapsed", time.Since(start).Truncate(100*time.Millisecond), "addr", finalAddr, "http_addr", finalHTTPAddr)
	return instance, nil
}

// cmdExecutor abstracts command execution for local vs remote (SSH) execution.
type cmdExecutor func(ctx context.Context, command string) ([]byte, error)

// localExecutor returns a cmdExecutor for local command execution.
func localExecutor() cmdExecutor {
	return func(ctx context.Context, command string) ([]byte, error) {
		return exec.CommandContext(ctx, "bash", "-c", command).CombinedOutput()
	}
}

// sshExecutor returns a cmdExecutor for remote SSH command execution.
func sshExecutor(host string) cmdExecutor {
	return func(ctx context.Context, command string) ([]byte, error) {
		return sshExec(ctx, host, command)
	}
}

// exeletResources holds the computed resource names for a test run.
type exeletResources struct {
	bridgeName   string
	networkCIDR  string
	zfsDataset   string
	proxyPortMin int
	proxyPortMax int
	dataDir      string
	coverDir     string
}

// computeExeletResources computes unique resource names for a test run.
func computeExeletResources(testRunID string) (*exeletResources, error) {
	testRunIDNum := uint32(0)
	if _, err := fmt.Sscanf(testRunID, "%x", &testRunIDNum); err != nil {
		return nil, fmt.Errorf("can't parse testRunID %q as hex number: %v", testRunID, err)
	}

	// Use CGNAT range 100.64.0.0/10 for internal bridges.
	// Map 16-bit testRunID to two octets for unique /24 networks.
	thirdOctet := ((testRunIDNum >> 8) & 0x3F) + 64 // 64-127
	fourthOctet := testRunIDNum & 0xFF              // 0-255

	return &exeletResources{
		bridgeName:   "br-exe-" + testRunID,
		networkCIDR:  fmt.Sprintf("100.%d.%d.0/24", thirdOctet, fourthOctet),
		zfsDataset:   "tank/e1e-" + testRunID,
		proxyPortMin: 30000 + (int(testRunIDNum%10) * 1000),
		proxyPortMax: 30000 + (int(testRunIDNum%10) * 1000) + 1000,
		dataDir:      "/d/e-" + testRunID,
		coverDir:     "/tmp/e1e-exelet-cov-" + testRunID,
	}, nil
}

// setup creates ZFS dataset and directories using the given executor.
func (r *exeletResources) setup(ctx context.Context, execute cmdExecutor, testRunID string) error {
	// Create ZFS dataset if it doesn't exist
	if _, err := execute(ctx, fmt.Sprintf("sudo zfs list %s >/dev/null 2>&1", r.zfsDataset)); err != nil {
		slog.InfoContext(ctx, "creating ZFS dataset", "dataset", r.zfsDataset)
		if out, err := execute(ctx, "sudo zfs create "+r.zfsDataset); err != nil {
			return fmt.Errorf("failed to create ZFS dataset %s: %w\n%s", r.zfsDataset, err, out)
		}
	}

	// Clone existing image volumes for test isolation
	if err := cloneImageVolumesWithExecutor(ctx, execute, r.zfsDataset, testRunID); err != nil {
		slog.WarnContext(ctx, "failed to clone image volumes (tests will still work but may be slower)", "error", err)
	}

	// Create the data directory and coverage directory
	if out, err := execute(ctx, fmt.Sprintf("sudo mkdir -p %s %s", r.dataDir, r.coverDir)); err != nil {
		return fmt.Errorf("failed to create data/coverage directory %s: %w\n%s", r.dataDir, err, out)
	}

	return nil
}

// Stop stops the exelet process running in the VM.
// This returns the local directory containing remote coverage files,
// or the empty string if there weren't any.
// This does not return an error; errors are just logged.
func (ei *ExeletInstance) Stop(ctx context.Context) string {
	client := ei.Client()
	defer client.Close()

	slog.InfoContext(ctx, "stopping exelet", "ID", ei.testRunID)

	// Clean up the instances before killing the exelet.
	cleanupTestInstances(ctx, client)

	// Handle localhost vs remote differently
	if ei.RemoteHost == "" {
		return ei.stopLocal(ctx)
	}

	// Terminate the exelet with SIGTERM.
	// Let it write out coverage data.
	exeletCtx, exeletCancel := context.WithTimeout(ctx, 10*time.Second)
	slog.InfoContext(ctx, "sending SIGTERM to exelet", "ID", ei.testRunID)
	remoteBinaryPath := "/tmp/exelet-test-" + ei.testRunID
	pkillCmd := "sudo pkill -TERM -f " + remoteBinaryPath
	if out, err := sshExec(exeletCtx, ei.RemoteHost, pkillCmd); err != nil {
		slog.WarnContext(exeletCtx, "ssh command to kill exelet failed", "error", err, "output", out)
	}

	// Poll for process exit.
	pgrepCmd := "pgrep -f " + remoteBinaryPath
	for range 50 {
		// pgrep returns exit code 1 if no process matched.
		if out, err := sshExec(exeletCtx, ei.RemoteHost, pgrepCmd); err != nil {
			slog.DebugContext(exeletCtx, "ssh pgrep command expected to fail", "error", err, "output", out)
			// Process is gone.
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Force kill the exelet process in case it is still running.
	pkillCmd = "sudo pkill -KILL -f " + remoteBinaryPath
	sshExec(exeletCtx, ei.RemoteHost, pkillCmd)

	exeletCancel()

	// Stop the ssh process that started the exelet on the VM.
	ei.CmdCancel()
	ei.Cmd.Process.Kill()
	ei.Cmd.Wait()

	// Close the Errors channel the caller may be using.
	select {
	case <-ei.exeletLoggerDone:
	case <-time.After(10 * time.Second):
	}
	close(ei.Errors)

	// Stop the ssh tunnel if there is one.
	if ei.TunnelCancel != nil {
		ei.TunnelCancel()
	}
	if ei.TunnelCmd != nil && ei.TunnelCmd.Process != nil {
		ei.TunnelCmd.Process.Kill()
		ei.TunnelCmd.Wait()
	}

	// Download the exelet coverage data.
	localExeletCoverDir, err := os.MkdirTemp("", "ele-exelet-cov-local-")
	if err != nil {
		slog.ErrorContext(ctx, "failed to create local exelet coverage dir", "error", err)
	} else {
		// Download the remote coverage directory.
		if err := scpDownloadDir(ctx, ei.RemoteHost, ei.CoverDir+"/*", localExeletCoverDir); err != nil {
			slog.ErrorContext(ctx, "failed to download exelet coverage", "error", err)
			localExeletCoverDir = "" // don't use on failure
		} else {
			slog.InfoContext(ctx, "downloaded exelet coverage", "local_dir", localExeletCoverDir)
		}
	}

	// Remote cleanup. Use a fresh context with enough time.
	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer cleanupCancel()

	// Remove the bridge.
	slog.InfoContext(cleanupCtx, "removing bridge", "bridge", ei.BridgeName)
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "sudo ip link delete "+ei.BridgeName+"-0"); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to remove bridge", "bridge", ei.BridgeName, "error", err, "output", out)
	}

	// Remove the ZFS dataset, including the cloned image volumes.
	slog.InfoContext(cleanupCtx, "removing ZFS dataset", "dataset", ei.ZFSDataset)
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "sudo zfs destroy -r "+ei.ZFSDataset); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to remove ZFS dataset", "dataset", ei.ZFSDataset, "error", err, "output", out)
	}
	// Clean up snapshots we create on source image volumes for cloning.
	cleanupSnapshotsCmd := fmt.Sprintf("sudo zfs list -H -t snapshot -o name | grep '@ele-%s$' | xargs -r -n1 sudo zfs destroy", ei.testRunID)
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, cleanupSnapshotsCmd); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to cleanup image snapshots", "error", err, "output", out)
	}

	// Remove the data directory.
	slog.InfoContext(cleanupCtx, "removing data directory", "dataDir", ei.DataDir)
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "sudo rm -rf "+ei.DataDir); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to remove data directory", "dataDir", ei.DataDir, "error", err, "output", out)
	}

	// Remove remote coverage directory.
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "sudo rm -rf "+ei.CoverDir); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to remove remote coverage directory", "error", err, "output", out)
	}

	// Remove remote binary.
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "rm -f "+remoteBinaryPath); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to cleanup remote exelet binary", "error", err, "output", out)
	}

	// Remove local binary.
	os.Remove(filepath.Join(os.TempDir(), "exelet-test"))

	return localExeletCoverDir
}

// stopLocal stops exelet running locally (for CTR_HOST=localhost).
func (ei *ExeletInstance) stopLocal(ctx context.Context) string {
	slog.InfoContext(ctx, "stopping local exelet", "ID", ei.testRunID)

	// Kill exelet immediately - no graceful shutdown needed for tests
	// The exelet runs under sudo, so we need sudo pkill to kill it
	slog.InfoContext(ctx, "killing local exelet via pkill")
	exec.Command("sudo", "pkill", "-9", "-f", "exelet-test").Run()
	if ei.Cmd != nil {
		done := make(chan struct{})
		go func() {
			ei.Cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.WarnContext(ctx, "local exelet did not exit after SIGKILL, giving up")
		}
	}

	if ei.CmdCancel != nil {
		ei.CmdCancel()
	}

	// Close the Errors channel
	select {
	case <-ei.exeletLoggerDone:
	case <-time.After(10 * time.Second):
	}
	close(ei.Errors)

	// Coverage is already local for localhost mode
	localExeletCoverDir := ei.CoverDir

	// Local cleanup
	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer cleanupCancel()

	// Remove bridge
	if ei.BridgeName != "" {
		slog.InfoContext(cleanupCtx, "removing bridge", "bridge", ei.BridgeName)
		if out, err := exec.CommandContext(cleanupCtx, "sudo", "ip", "link", "delete", ei.BridgeName+"-0").CombinedOutput(); err != nil {
			slog.ErrorContext(cleanupCtx, "failed to remove bridge", "bridge", ei.BridgeName, "error", err, "output", string(out))
		}
	}

	// Remove ZFS dataset
	if ei.ZFSDataset != "" {
		slog.InfoContext(cleanupCtx, "removing ZFS dataset", "dataset", ei.ZFSDataset)
		if out, err := exec.CommandContext(cleanupCtx, "sudo", "zfs", "destroy", "-r", ei.ZFSDataset).CombinedOutput(); err != nil {
			slog.ErrorContext(cleanupCtx, "failed to remove ZFS dataset", "dataset", ei.ZFSDataset, "error", err, "output", string(out))
		}
		// Clean up snapshots
		cleanupSnapshotsCmd := fmt.Sprintf("sudo zfs list -H -t snapshot -o name | grep '@e1e-%s$' | xargs -r -n1 sudo zfs destroy", ei.testRunID)
		if out, err := exec.CommandContext(cleanupCtx, "bash", "-c", cleanupSnapshotsCmd).CombinedOutput(); err != nil {
			slog.ErrorContext(cleanupCtx, "failed to cleanup image snapshots", "error", err, "output", string(out))
		}
	}

	// Remove data directory
	if ei.DataDir != "" {
		slog.InfoContext(cleanupCtx, "removing data directory", "dataDir", ei.DataDir)
		if out, err := exec.CommandContext(cleanupCtx, "sudo", "rm", "-rf", ei.DataDir).CombinedOutput(); err != nil {
			slog.ErrorContext(cleanupCtx, "failed to remove data directory", "dataDir", ei.DataDir, "error", err, "output", string(out))
		}
	}

	// Remove local binary
	os.Remove(filepath.Join(os.TempDir(), "exelet-test"))

	return localExeletCoverDir
}

// cloneImageVolumesWithExecutor clones existing image volumes from tank/sha256:*
// into the test dataset. This enables copy-on-write sharing of base images,
// making tests much faster since images don't need to be re-downloaded
// and provisioned for each test run.
//
// TODO: After test run completes, promote new images from tank/e1e-XXXX/sha256:*
// to tank/sha256:* so subsequent runs can reuse them. Currently the shared cache
// at tank/sha256:* must be seeded manually.
func cloneImageVolumesWithExecutor(ctx context.Context, execute cmdExecutor, zfsDataset, runID string) error {
	// List all ZFS datasets
	out, err := execute(ctx, "sudo zfs list -H -o name")
	if err != nil {
		return nil
	}

	// Filter for tank/sha256:* volumes (the cached base images).
	var volumes []string
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("tank/sha256:")) {
			volumes = append(volumes, string(line))
		}
	}

	if len(volumes) == 0 {
		return nil
	}

	slog.InfoContext(ctx, "cloning image volumes for test isolation", "count", len(volumes), "dataset", zfsDataset)

	for _, srcVolume := range volumes {
		// Extract the sha256:... part from tank/sha256:...
		// and create tank/e1e-<runID>/sha256:...
		imageID := strings.TrimPrefix(srcVolume, "tank/")
		destVolume := zfsDataset + "/" + imageID
		snapName := "e1e-" + runID

		// Create a snapshot of the source volume
		snapCmd := fmt.Sprintf("sudo zfs snapshot %s@%s", srcVolume, snapName)
		if out, err := execute(ctx, snapCmd); err != nil {
			slog.WarnContext(ctx, "failed to create snapshot for image clone", "src", srcVolume, "error", err, "output", string(out))
			continue
		}

		// Clone the snapshot to the test dataset
		cloneCmd := fmt.Sprintf("sudo zfs clone %s@%s %s", srcVolume, snapName, destVolume)
		if out, err := execute(ctx, cloneCmd); err != nil {
			slog.WarnContext(ctx, "failed to clone image volume", "src", srcVolume, "dest", destVolume, "error", err, "output", string(out))
			// Clean up the snapshot we just created
			execute(ctx, fmt.Sprintf("sudo zfs destroy %s@%s 2>/dev/null || true", srcVolume, snapName))
			continue
		}

		slog.DebugContext(ctx, "cloned image volume", "src", srcVolume, "dest", destVolume)
	}

	return nil
}

// resolveGateway returns the gateway address of the VM.
// This is the address that the VM uses to connect to the local host system.
func resolveGateway(ctx context.Context, host string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "UserKnownHostsFile=/dev/null",
		host,
		"getent ahostsv4 _gateway",
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("gateway resolution failed: %v\n%s", err, ee.Stderr)
		}
		return "", fmt.Errorf("gateway resolution failed: %v", err)
	}
	if len(out) == 0 {
		return "", errors.New("gateway resolution did not return any addresses")
	}
	addr := string(bytes.Fields(out)[0])
	return addr, nil
}

// testRemoteToLocalConnectivity reports whether the remote host can
// reach the local port via gateway.
func testRemoteToLocalConnectivity(ctx context.Context, host, gateway string, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	testCmd := fmt.Sprintf("timeout 2 nc -z %s %d 2>/dev/null", gateway, port)
	out, err := sshExec(ctx, host, testCmd)
	if err != nil {
		slog.DebugContext(ctx, "no remote to local connectivity", "error", err, "output", out)
		return false
	}
	return true
}

// startSSHTunnel establishes an SSH reverse tunnel
// and returns the dynamically allocated remote port,
// and information about the SSH process.
func startSSHTunnel(ctx context.Context, host string, localPort int) (remotePort int, tunnelCmd *exec.Cmd, cancel context.CancelFunc, err error) {
	ctx, cancel = context.WithCancel(ctx)

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

	stderrPipe, err := tunnelCmd.StderrPipe()
	if err != nil {
		cancel()
		return 0, nil, nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := tunnelCmd.Start(); err != nil {
		cancel()
		return 0, nil, nil, fmt.Errorf("failed to start SSH tunnel: %w", err)
	}

	// Parse stderr for "Allocated port X for remote forward".
	portC := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
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
		if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.ErrorContext(ctx, "error scanning SSH tunnel output", "error", err)
		}
	}()

	select {
	case remotePort = <-portC:
		slog.InfoContext(ctx, "SSH tunnel established", "remote_port", remotePort, "local_port", localPort)
		return remotePort, tunnelCmd, cancel, nil
	case <-time.After(5 * time.Second):
		tunnelCmd.Process.Kill()
		cancel()
		return 0, nil, nil, fmt.Errorf("timeout waiting for SSH tunnel allocate port")
	}
}

// cleanupTestInstances removes instances on the exelet.
// This is best effort only.
func cleanupTestInstances(ctx context.Context, exeletClient *client.Client) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	stream, err := exeletClient.ListInstances(ctx, &api.ListInstancesRequest{})
	if err != nil {
		slog.ErrorContext(ctx, "failed to list instances", "error", err)
		return
	}

	var instancesToDelete []string
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.ErrorContext(ctx, "error receiving instance list", "error", err)
			break
		}
		instancesToDelete = append(instancesToDelete, resp.Instance.ID)
	}

	for _, id := range instancesToDelete {
		slog.InfoContext(ctx, "deleting test instance", "id", id)
		if _, err := exeletClient.DeleteInstance(ctx, &api.DeleteInstanceRequest{ID: id}); err != nil {
			slog.ErrorContext(ctx, "failed to delete instance", "id", id, "error", err)
		}
	}
}

// sshExec executes a command on remote host and returns combined output.
func sshExec(ctx context.Context, host, command string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		host, command)
	return cmd.CombinedOutput()
}

// scpUpload uploads a file to remote host and returns combined output.
func scpUpload(ctx context.Context, localPath, host, remotePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", // hides "Permanently added" spam, but still shows real errors
		localPath, host+":"+remotePath)
	return cmd.CombinedOutput()
}

// scpDownloadDir downloads a directory tree from a remote host.
func scpDownloadDir(ctx context.Context, host, remotePath, localPath string) error {
	cmd := exec.CommandContext(ctx, "scp",
		"-r",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		host+":"+remotePath, localPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scp failed: %v\n%s", err, out)
	}
	return nil
}

// buildExeletBinaryMu ensures exclusivity within a single process.
// We use a file lock for exclusivity between processes.
var buildExeletBinaryMu sync.Mutex

// BuildExeletBinary builds exelet locally for Linux and returns path to binary.
// The binary is built with coverage instrumentation via "make exelet-coverage".
func BuildExeletBinary(testRunID string) (string, error) {
	binPath := filepath.Join(os.TempDir(), "exelet-test-"+testRunID)

	// Set working directory to project root (parent of e1e directory)
	srcdir, err := exeRootDir()
	if err != nil {
		return "", err
	}

	// The Makefile is not concurrent-safe, so use a lock.
	buildExeletBinaryMu.Lock()
	defer buildExeletBinaryMu.Unlock()

	cleanup, err := flock(filepath.Join(srcdir, "Makefile"))
	if err != nil {
		return "", fmt.Errorf("failed to acquire lock on Makefile: %v", err)
	}
	defer cleanup()

	// Build exelet with coverage instrumentation.
	cmd := exec.Command("make", "exelet-coverage")
	cmd.Dir = srcdir
	cmd.Env = append(cmd.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to build exelet: %w\n%s", err, out)
	}

	// Ensure temp exelet is not present.
	if _, err := os.Stat(binPath); err == nil {
		if rErr := os.RemoveAll(binPath); rErr != nil {
			if !os.IsNotExist(rErr) {
				return "", fmt.Errorf("error removing existing exelet from %s: %v", binPath, rErr)
			}
		}
	}

	// Rename to test binary path.
	if err := os.Rename(filepath.Join(srcdir, "exeletd"), binPath); err != nil {
		return "", fmt.Errorf("failed to rename exelet to %s: %v", binPath, err)
	}

	return binPath, nil
}

// flock acquires an exclusive advisory lock on filename.
// It returns a function that releases the lock.
func flock(filename string) (func(), error) {
	fd, err := syscall.Open(filename, syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	fl := syscall.Flock_t{
		Type:   syscall.F_WRLCK,
		Start:  0,
		Len:    0,
		Whence: io.SeekStart,
	}
	for {
		err := syscall.FcntlFlock(uintptr(fd), syscall.F_SETLKW, &fl)
		if err == nil {
			break
		}
		if err != syscall.EINTR {
			syscall.Close(fd)
			return nil, fmt.Errorf("failed to acquire file lock: %v", err)
		}
	}

	cleanup := func() {
		fl.Type = syscall.F_UNLCK
		syscall.FcntlFlock(uintptr(fd), syscall.F_SETLK, &fl)
		syscall.Close(fd)
	}

	return cleanup, nil
}
