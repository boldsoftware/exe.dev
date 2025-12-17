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

	// ctrHost is like "ssh://lima-exe-ctr-tests"
	host := strings.TrimPrefix(ctrHost, "ssh://")
	if strings.HasPrefix(host, "ssh://") {
		slog.ErrorContext(ctx, "invalid ctrHost", "ctrHost", ctrHost)
	}
	if host == "" {
		return nil, fmt.Errorf("exelet requires remote host; set CTR_HOST environment variable")
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

	// Generate unique resource names for this test run to enable
	// parallel test execution.
	// Each test run gets its own bridge, network, and ZFS dataset.
	bridgeName := "br-exe-" + testRunID
	// Use CGNAT range 100.64.0.0/10 for internal bridges
	// (safe, no conflicts with Lima's 192.168.64.0/24).
	// testRunID is a 4-character hex string (0000-FFFF),
	// map to unique /24 networks.
	// CGNAT /10 gives us 100.64.0.0 through 100.127.255.255.
	// Map 16-bit testRunID to two octets for up to 16384 unique /24 networks.
	testRunIDNum := uint32(0)
	if _, err := fmt.Sscanf(testRunID, "%x", &testRunIDNum); err != nil {
		return nil, fmt.Errorf("can't parse testRunID %q as hex number: %v", testRunID, err)
	}
	// Use both bytes: upper 6 bits for third octet (64-127),
	// lower 8 bits for fourth octet (0-255).
	thirdOctet := ((testRunIDNum >> 8) & 0x3F) + 64 // 64-127
	fourthOctet := testRunIDNum & 0xFF              // 0-255
	networkCIDR := fmt.Sprintf("100.%d.%d.0/24", thirdOctet, fourthOctet)

	zfsDataset := "tank/e1e-" + testRunID

	slog.InfoContext(ctx, "using isolated resources", "bridge", bridgeName, "network", networkCIDR, "dataset", zfsDataset)

	// Create ZFS dataset if it doesn't exist.
	// Check if dataset exists first.
	checkCmd := fmt.Sprintf("sudo zfs list %s >/dev/null 2>&1", zfsDataset)
	if _, err = sshExec(ctx, host, checkCmd); err != nil {
		// Dataset doesn't exist, create it
		slog.InfoContext(ctx, "creating ZFS dataset", "dataset", zfsDataset)
		createCmd := "sudo zfs create " + zfsDataset
		if out, err := sshExec(ctx, host, createCmd); err != nil {
			return nil, fmt.Errorf("failed to create ZFS dataset %s: %w\n%s", zfsDataset, err, out)
		}
	}

	// Clone existing image volumes from tank/sha256:* into
	// tank/e1e-<testRunID>/sha256:*
	// This enables copy-on-write sharing of base images,
	// making tests much faster.
	if err := cloneImageVolumes(ctx, host, zfsDataset, testRunID); err != nil {
		slog.WarnContext(ctx, "failed to clone image volumes (tests will still work but may be slower)", "error", err)
	}

	// Start exelet on remote host via SSH
	// Use proxy port range 30000-40000 for e1e tests to avoid conflicts with dev (10000-20000) and unit tests (20000-30000)
	// URL-encode the network CIDR since it contains slashes
	// Metadata service will bind to the unique bridge IP with DNAT for parallel test support
	encodedNetwork := url.QueryEscape(networkCIDR)

	// Compute unique port range for this test run to avoid
	// port conflicts in parallel test execution.
	// Base range is 30000-40000 (10000 ports).
	// Divide into 1000-port chunks for each test run.
	// testRunIDNum is 0-65535, map to 10 possible chunks
	// (30000-31000, 31000-32000, ..., 39000-40000)
	proxyPortMin := 30000 + (int(testRunIDNum%10) * 1000)
	proxyPortMax := proxyPortMin + 1000

	// Build the command to execute remotely
	// Use unique data-dir per test run to avoid mount conflicts
	// in /data/exelet/storage/mounts
	// Use short-ish paths because there's a Unix socket path limit
	// in the ~107 range, and long test names can run into it, amazingly.
	dataDir := "/d/e-" + testRunID
	coverDir := "/tmp/e1e-exelet-cov" + testRunID

	// Create the data directory and coverage directory.
	if out, err := sshExec(ctx, host, fmt.Sprintf("sudo mkdir -p %s %s", dataDir, coverDir)); err != nil {
		return nil, fmt.Errorf("failed to create data/coverage directory %s: %w\n%s", dataDir, err, out)
	}

	args := []string{
		"sudo",
		"GOCOVERDIR=" + coverDir,
		"LOG_FORMAT=json",
		remoteBinaryPath,
		"--debug",
		"--listen-address", "tcp://0.0.0.0:0",
		"--http-addr", ":0",
		"--data-dir", dataDir,
		"--runtime-address", "cloudhypervisor:///" + dataDir + "/runtime",
		"--storage-manager-address", "zfs:///" + dataDir + "/storage?dataset=" + zfsDataset,
		"--network-manager-address", `"nat:///` + dataDir + `/network?bridge=` + bridgeName + `&network=` + encodedNetwork + `"`,
		"--proxy-port-min", strconv.Itoa(proxyPortMin),
		"--proxy-port-max", strconv.Itoa(proxyPortMax),
		"--resource-manager-interval", "5s",
		"--exed-url", exedProxyURL,
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
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	exeletCmd.Stderr = exeletCmd.Stdout

	if err := exeletCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start remote exelet: %w", err)
	}

	go func() {
		exeletCmd.Wait()
		exeletCancel()
	}()

	// Parse output to find addresses (similar to exed startup)
	var teeMu sync.Mutex
	tee := new(strings.Builder)
	grpcAddrC := make(chan string, 1)
	httpAddrC := make(chan string, 1)
	errorsC := make(chan string, 16)
	exeletLoggerDone := make(chan bool)

	go func() {
		defer close(exeletLoggerDone)
		scan := bufio.NewScanner(cmdOut)
		for scan.Scan() {
			line := scan.Bytes()
			teeMu.Lock()
			tee.Write(line)
			tee.WriteString("\n")
			teeMu.Unlock()

			if logFile != nil {
				fmt.Fprintf(logFile, "%s", line)
			}

			// Parse JSON log line
			if !json.Valid(line) {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}

			// Capture ERROR level logs
			if level, ok := entry["level"].(string); ok && level == "ERROR" {
				select {
				case errorsC <- string(line):
				default:
				}
			}

			// Look for listening messages
			switch entry["msg"] {
			case "listening":
				if addrVal, ok := entry["addr"].(string); ok {
					select {
					case grpcAddrC <- addrVal:
					default:
					}
				}
			case "http server listening":
				if addrVal, ok := entry["addr"].(string); ok {
					select {
					case httpAddrC <- addrVal:
					default:
					}
				}
			}
		}
		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.WarnContext(ctx, "scanning exelet output failed", "error", err)
		}
	}()

	// Wait for exelet to start and extract addresses
	slog.InfoContext(ctx, "waiting for exelet to start on remote host")
	timeout := time.Minute
	if os.Getenv("CI") != "" {
		timeout = 2 * time.Minute
	}

	var grpcAddr, httpAddr string
	timer := time.NewTimer(timeout)
	defer timer.Stop()

WaitLoop:
	for {
		select {
		case grpcAddr = <-grpcAddrC:
			if httpAddr != "" {
				break WaitLoop
			}
		case httpAddr = <-httpAddrC:
			if grpcAddr != "" {
				break WaitLoop
			}
		case <-timer.C:
			// Cleanup on timeout
			exeletCmd.Process.Kill()
			exeletCancel()
			teeMu.Lock()
			lastOutput := tee.String()
			teeMu.Unlock()
			return nil, fmt.Errorf("timeout waiting for exelet to start. Last log output:\n%s", lastOutput)
		}
	}

	// Parse address to replace 0.0.0.0 with actual remote IP
	// grpcAddr is like "tcp://0.0.0.0:45678"
	u, err := url.Parse(grpcAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse exelet address: %w", err)
	}

	// Construct the actual address
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse port from %s: %w", u.Host, err)
	}
	finalAddr := fmt.Sprintf("tcp://%s:%s", host, port)

	_, httpPort, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse http port from %s: %w", httpAddr, err)
	}
	finalHTTPAddr := fmt.Sprintf("http://%s:%s", host, httpPort)

	// Open a client in the background,
	// so that it is ready when we need it.
	// We need it to shut down the exelet if not before.
	clientC := make(chan *client.Client, 1)
	go func() {
		c, err := client.NewClient(finalAddr, client.WithInsecure())
		if err != nil {
			slog.ErrorContext(ctx, "failed to create exelet client", "error", err)
		}
		clientC <- c
	}()

	cause := sync.OnceValue(func() error {
		return context.Cause(exeletCtx)
	})

	readClient := sync.OnceValue(func() *client.Client {
		return <-clientC
	})

	instance := &ExeletInstance{
		Address:          finalAddr,
		HTTPAddress:      finalHTTPAddr,
		Exited:           exeletCtx.Done(),
		Cause:            cause,
		Cmd:              exeletCmd,
		CmdCancel:        exeletCancel,
		DataDir:          dataDir,
		RemoteHost:       host,
		TunnelCmd:        tunnelCmd,
		TunnelCancel:     tunnelCancel,
		BridgeName:       bridgeName,
		ZFSDataset:       zfsDataset,
		CoverDir:         coverDir,
		Errors:           errorsC,
		Client:           readClient,
		testRunID:        testRunID,
		exeletLoggerDone: exeletLoggerDone,
	}

	slog.InfoContext(ctx, "started remote exelet", "elapsed", time.Since(start).Truncate(100*time.Millisecond), "addr", finalAddr, "http_addr", finalHTTPAddr)
	return instance, nil
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

// cloneImageVolumes clones existing image volumes from tank/sha256:*
// into the test dataset. This enables copy-on-write sharing of base images,
// making tests much faster since images don't need to be re-downloaded
// and provisioned for each test run.
func cloneImageVolumes(ctx context.Context, host, zfsDataset, runID string) error {
	// List all ZFS datasets
	out, err := sshExec(ctx, host, "sudo zfs list -H -o name")
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
		if out, err := sshExec(ctx, host, snapCmd); err != nil {
			slog.WarnContext(ctx, "failed to create snapshot for image clone", "src", srcVolume, "error", err, "output", string(out))
			continue
		}

		// Clone the snapshot to the test dataset
		cloneCmd := fmt.Sprintf("sudo zfs clone %s@%s %s", srcVolume, snapName, destVolume)
		if out, err := sshExec(ctx, host, cloneCmd); err != nil {
			slog.WarnContext(ctx, "failed to clone image volume", "src", srcVolume, "dest", destVolume, "error", err, "output", string(out))
			// Clean up the snapshot we just created
			sshExec(ctx, host, fmt.Sprintf("sudo zfs destroy %s@%s 2>/dev/null || true", srcVolume, snapName))
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

// BuildExeletBinary builds exelet locally for Linux and returns path to binary.
// The binary is built with coverage instrumentation via "make exelet-coverage".
func BuildExeletBinary() (string, error) {
	binPath := filepath.Join(os.TempDir(), "exelet-test")

	// Set working directory to project root (parent of e1e directory)
	srcdir, err := exeRootDir()
	if err != nil {
		return "", err
	}

	// The Makefile is not concurrent-safe, so use a lock.
	makefile := filepath.Join(srcdir, "Makefile")
	fd, err := syscall.Open(makefile, syscall.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("failed to open Makefile: %v", err)
	}
	defer syscall.Close(fd)

	fl := syscall.Flock_t{
		Type:   syscall.F_WRLCK,
		Start:  0,
		Len:    0,
		Whence: io.SeekStart,
	}
	for {
		if err := syscall.FcntlFlock(uintptr(fd), syscall.F_SETLKW, &fl); err == nil {
			break
		}
		if err != syscall.EAGAIN {
			return "", fmt.Errorf("failed to acquire file lock: %v", err)
		}
	}

	fl.Type = syscall.F_UNLCK
	defer syscall.FcntlFlock(uintptr(fd), syscall.F_SETLK, &fl)

	// Build exelet with coverage instrumentation
	cmd := exec.Command("make", "exelet-coverage")
	cmd.Dir = srcdir
	cmd.Env = append(cmd.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to build exelet: %w\n%s", err, out)
	}

	// Ensure temp exelet is not present
	if _, err := os.Stat(binPath); err == nil {
		if rErr := os.RemoveAll(binPath); rErr != nil {
			if !os.IsNotExist(rErr) {
				return "", fmt.Errorf("error removing existing exelet from %s: %v", binPath, rErr)
			}
		}
	}

	// Rename to test binary path
	if err := os.Rename(filepath.Join(srcdir, "exeletd"), binPath); err != nil {
		return "", fmt.Errorf("failed to rename exelet to %s: %v", binPath, err)
	}

	return binPath, nil
}
