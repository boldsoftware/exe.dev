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

// ReplicationConfig configures storage replication for an exelet.
type ReplicationConfig struct {
	Enabled   bool
	Target    string        // e.g., "file:///path/to/backups"
	Interval  time.Duration // e.g., 10*time.Second for fast tests
	Retention int           // number of snapshots to keep
}

// MetricsConfig configures metrics collection for an exelet.
type MetricsConfig struct {
	DaemonURL string        // e.g., "http://localhost:8090"
	Interval  time.Duration // e.g., 5*time.Second for tests
}

// ExeletInstance describes a single running exelet.
type ExeletInstance struct {
	Address       string                // e.g., "tcp://192.168.5.15:9080"
	HTTPAddress   string                // e.g., "http://192.168.5.15:9081"
	Exited        <-chan struct{}       // closed when Cmd exits
	Cause         func() error          // why context was canceled
	Cmd           *exec.Cmd             // SSH command running exelet
	CmdCancel     context.CancelFunc    // cancel function for exelet context
	DataDir       string                // temp directory for exelet data (local or remote path)
	RemoteHost    string                // VM on which exelet is running
	TunnelCmd1    *exec.Cmd             // SSH tunnel process if using reverse tunnel
	TunnelCancel1 context.CancelFunc    // cancel function for tunnel context
	TunnelCmd2    *exec.Cmd             // second SSH tunnel
	TunnelCancel2 context.CancelFunc    // second cancel function
	BridgeName    string                // bridge name for network isolation
	ZFSDataset    string                // ZFS dataset for storage isolation
	CoverDir      string                // remote directory for Go coverage artifacts (GOCOVERDIR)
	Errors        chan string           // exelet errors are sent on this channel
	Client        func() *client.Client // function returns exelet control client
	ExepipeErrors chan string           // error from remote exepipe

	exepipeCmd        *exec.Cmd // remote exepipe process if there is one
	testRunID         string    // argument to StartExelet
	exeletLoggerDone  chan bool // closed when logging goroutine done
	exepipeLoggerDone chan bool // closed when exepipe logging done
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
// HTTP server is listening on. metadataPort is the same,
// but can be either exed or exeprox.
//
// exepipeInstance describes an exepipe process.
//
// testRunID is a unique string for this invocation.
//
// logFile, if not nil, is a file to write logs to.
// exepipeLogFile, if not nil, is for remote exepipe logs.
//
// logPorts is whether to log port numbers using slog.InfoContext.
//
// replication, if not nil, configures storage replication.
//
// metrics, if not nil, configures metrics collection.
func StartExelet(ctx context.Context, exeletBinary, ctrHost string, exedPort, metadataPort int, exepipe *ExepipeInstance, testRunID string, logFile, exepipeLogFile io.Writer, logPorts bool, replication *ReplicationConfig, metrics *MetricsConfig) (ei *ExeletInstance, err error) {
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
		return startExeletLocal(ctx, exeletBinary, exedPort, metadataPort, exepipe, testRunID, logFile, logPorts, replication, metrics, start)
	}

	// Get the gateway address of the VM.
	gateway, err := resolveGateway(ctx, host)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "resolved default gateway", "addr", gateway)

	exedProxyURL, tunnelCmd1, tunnelCancel1, err := localProxyURL(ctx, host, gateway, exedPort, logPorts)
	if err != nil {
		return nil, err
	}
	if tunnelCmd1 != nil {
		defer func() {
			if err != nil {
				tunnelCancel1()
				tunnelCmd1.Process.Kill()
				tunnelCmd1.Wait()
			}
		}()
	}

	var metadataProxyURL string
	var tunnelCmd2 *exec.Cmd
	var tunnelCancel2 context.CancelFunc
	if metadataPort == exedPort {
		metadataProxyURL = exedProxyURL
	} else {
		metadataProxyURL, tunnelCmd2, tunnelCancel2, err = localProxyURL(ctx, host, gateway, metadataPort, logPorts)
		if err != nil {
			return nil, err
		}
		if tunnelCmd2 != nil {
			defer func() {
				if err != nil {
					tunnelCancel2()
					tunnelCmd2.Process.Kill()
					tunnelCmd2.Wait()
				}
			}()
		}
	}

	// Use test-run-specific binary name to avoid conflicts with
	// parallel test runs. This is the name of the binary
	// on the VM, not on the host running the test.

	remoteExeletPath := "/tmp/exelet-test-" + testRunID

	type remoteBin struct {
		local, remote string
	}
	bins := []remoteBin{
		{
			local:  exeletBinary,
			remote: remoteExeletPath,
		},
	}

	remoteExepipePath := ""
	if exepipe != nil {
		remoteExepipePath = "/tmp/exepipe-onvm-" + testRunID
		bins = append(bins,
			remoteBin{
				local:  exepipe.BinPath,
				remote: remoteExepipePath,
			},
		)
	}

	// Ensure no existing binaries exist for this test run
	// (e.g. on failed re-run).
	for _, bin := range bins {
		if out, err := sshExec(ctx, host, fmt.Sprintf("rm -rf %s", bin.remote)); err != nil {
			return nil, fmt.Errorf("failed to remove existing binary %s: %w\n%s", bin.remote, err, out)
		}

		// Ensure no existing processes exist for this test run only.
		sshExec(ctx, host, fmt.Sprintf("pkill -f %s", bin.remote))

		// Upload binary to remote host with unique name.
		slog.InfoContext(ctx, "uploading binary to remote host", "host", host, "binary", bin.local, "path", bin.remote)
		if out, err := scpUpload(ctx, bin.local, host, bin.remote); err != nil {
			return nil, fmt.Errorf("failed to upload exelet: %w\n%s", err, out)
		}

		// Make binary executable
		if out, err := sshExec(ctx, host, fmt.Sprintf("chmod +x %s", bin.remote)); err != nil {
			return nil, fmt.Errorf("failed to chmod exelet: %w\n%s", err, out)
		}
	}

	var exepipeWaiter *exepipeVMWaiter
	if exepipe != nil {
		exepipeWaiter, err = startExepipeOnVM(ctx, host, remoteExepipePath, exepipeLogFile)
		if err != nil {
			return nil, err
		}
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
		remoteExeletPath,
		"--debug",
		"--stage", "test",
		"--name", host,
		"--listen-address", "tcp://0.0.0.0:0",
		"--http-addr", ":0",
		"--data-dir", res.dataDir,
		"--runtime-address", "cloudhypervisor:///" + res.dataDir + "/runtime",
		"--storage-manager-address", "zfs:///" + res.dataDir + "/storage?dataset=" + res.zfsDataset,
		"--network-manager-address", `"nat:///` + res.dataDir + `/network?bridge=` + res.bridgeName + `&network=` + encodedNetwork + `"`,
		"--proxy-port-min", strconv.Itoa(res.proxyPortMin),
		"--proxy-port-max", strconv.Itoa(res.proxyPortMax),
		"--resource-manager-interval", "5s",
		"--exed-url", exedProxyURL,
		"--metadata-url", metadataProxyURL,
		"--enable-hugepages",
		"--desired-state-sync",
		"--desired-state-sync-interval", "1s",
		"--pktflow-enabled",
		"--pktflow-interval", "2s",
		"--pktflow-sample-rate", "1",
		"--pktflow-mapping-refresh", "500ms",
	}

	if exepipe != nil {
		args = append(args, "--exepipe-address=@exepipe")
	}

	// Add replication flags if configured
	if replication != nil && replication.Enabled {
		args = append(args,
			"--storage-replication-enabled",
			"--storage-replication-target", replication.Target,
			"--storage-replication-interval", replication.Interval.String(),
			"--storage-replication-retention", strconv.Itoa(replication.Retention),
		)
	}

	// Add metrics flags if configured.
	// Rewrite localhost to the gateway address so the remote exelet
	// can reach metricsd running on the test host.
	if metrics != nil && metrics.DaemonURL != "" {
		daemonURL := metrics.DaemonURL
		daemonURL = strings.Replace(daemonURL, "://localhost:", "://"+gateway+":", 1)
		args = append(args,
			"--metrics-daemon-url", daemonURL,
			"--metrics-daemon-interval", metrics.Interval.String(),
		)
	}

	if exepipeWaiter != nil {
		if err := exepipeWaiter.wait(ctx); err != nil {
			return nil, err
		}
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
		TunnelCmd1:       tunnelCmd1,
		TunnelCancel1:    tunnelCancel1,
		TunnelCmd2:       tunnelCmd2,
		TunnelCancel2:    tunnelCancel2,
		BridgeName:       res.bridgeName,
		ZFSDataset:       res.zfsDataset,
		CoverDir:         res.coverDir,
		Errors:           watcher.errorsC,
		Client:           readClient,
		testRunID:        testRunID,
		exeletLoggerDone: watcher.done,
	}
	if exepipeWaiter != nil {
		instance.ExepipeErrors = exepipeWaiter.errors
		instance.exepipeLoggerDone = exepipeWaiter.done
	}

	AddCanonicalization(instance.Address, "EXELET_ADDRESS")
	AddCanonicalization(instance.HTTPAddress, "EXELET_HTTP_ADDRESS")

	slog.InfoContext(ctx, "started remote exelet", "elapsed", time.Since(start).Truncate(100*time.Millisecond), "addr", finalAddr, "http_addr", finalHTTPAddr)
	return instance, nil
}

// exepipeVMWaiter waits for exepipe to start on the VM.
type exepipeVMWaiter struct {
	ch     chan bool
	errors chan string
	done   chan bool
	teeMu  sync.Mutex
	tee    strings.Builder
}

// wait waits for exepipe to start.
func (ew *exepipeVMWaiter) wait(ctx context.Context) error {
	select {
	case <-ew.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Minute):
		ew.teeMu.Lock()
		output := ew.tee.String()
		ew.teeMu.Unlock()
		return fmt.Errorf("timeout waiting for exepipe; output:\n%s", output)
	}
}

// startExepipeOnVM starts an exepipe process on the VM.
func startExepipeOnVM(ctx context.Context, host, remoteExepipePath string, logFile io.Writer) (*exepipeVMWaiter, error) {
	waiter := &exepipeVMWaiter{
		ch:     make(chan bool),
		done:   make(chan bool),
		errors: make(chan string, 16),
	}

	args := []string{
		"sudo",
		"LOG_FORMAT=json",
		"LOG_LEVEL=debug",
		remoteExepipePath,
		"--stage", "test",
		"--addr", "@exepipe",
		"--http-port=", // no metrics
	}

	exepipeCmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		host, strings.Join(args, " "),
	)

	exepipeOut, err := exepipeCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get exepipe stdout pipe: %w", err)
	}
	exepipeCmd.Stderr = exepipeCmd.Stdout

	if err := exepipeCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start remote exepipe: %w", err)
	}

	go func() {
		defer close(waiter.done)

		scan := bufio.NewScanner(exepipeOut)
		for scan.Scan() {
			line := scan.Bytes()

			waiter.teeMu.Lock()
			waiter.tee.Write(line)
			waiter.tee.WriteString("\n")
			waiter.teeMu.Unlock()

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
				case waiter.errors <- string(line):
				default:
				}
			}

			if entry["msg"] == "server started" {
				close(waiter.ch)
			}
		}
		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			waiter.teeMu.Lock()
			output := waiter.tee.String()
			waiter.teeMu.Unlock()
			slog.WarnContext(ctx, "scanning exepipe output failed", "error", err, "output", output)
		}
	}()

	return waiter, nil
}

// startExeletLocal starts exelet locally (for CTR_HOST=localhost).
func startExeletLocal(ctx context.Context, exeletBinary string, exedPort, metadataPort int, exepipe *ExepipeInstance, testRunID string, logFile io.Writer, logPorts bool, replication *ReplicationConfig, metrics *MetricsConfig, start time.Time) (*ExeletInstance, error) {
	// For localhost, exelet can directly reach exed via localhost
	exedProxyURL := fmt.Sprintf("http://localhost:%d", exedPort)
	metadataProxyURL := fmt.Sprintf("http://localhost:%d", metadataPort)
	slog.InfoContext(ctx, "using localhost for exelet->exed", "port", exedPort, "metadataPort", metadataPort)

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

	localCmd := fmt.Sprintf(`sudo GOCOVERDIR=%s LOG_FORMAT=json %s --debug --stage test --name localhost --listen-address tcp://0.0.0.0:0 --http-addr :0 --data-dir %s --runtime-address cloudhypervisor:///%s/runtime --storage-manager-address "zfs:///%s/storage?dataset=%s" --network-manager-address "nat:///%s/network?bridge=%s&network=%s&disable_bandwidth=true" --proxy-port-min %d --proxy-port-max %d --resource-manager-interval 5s --exed-url %s --metadata-url %s --enable-hugepages --desired-state-sync --desired-state-sync-interval 1s --pktflow-enabled --pktflow-interval 2s --pktflow-sample-rate 1 --pktflow-mapping-refresh 500ms`,
		res.coverDir, exeletBinary, res.dataDir, res.dataDir, res.dataDir, res.zfsDataset, res.dataDir, res.bridgeName, encodedNetwork, res.proxyPortMin, res.proxyPortMax, exedProxyURL, metadataProxyURL)

	// Add replication flags if configured
	if replication != nil && replication.Enabled {
		localCmd += fmt.Sprintf(` --storage-replication-enabled --storage-replication-target %s --storage-replication-interval %s --storage-replication-retention %d`,
			replication.Target, replication.Interval.String(), replication.Retention)
	}

	// Add metrics flags if configured
	if metrics != nil && metrics.DaemonURL != "" {
		localCmd += fmt.Sprintf(` --metrics-daemon-url %s --metrics-daemon-interval %s`,
			metrics.DaemonURL, metrics.Interval.String())
	}

	// Add exepipe flag if using.
	if exepipe != nil {
		localCmd += " --exepipe-address" + exepipe.UnixAddr
	}

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
		proxyPortMin: 20000 + (int(testRunIDNum%10) * 1000),
		proxyPortMax: 20000 + (int(testRunIDNum%10) * 1000) + 1000,
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
	remoteExeletPath := "/tmp/exelet-test-" + ei.testRunID
	pkillCmd := "sudo pkill -TERM -f " + remoteExeletPath
	if out, err := sshExec(exeletCtx, ei.RemoteHost, pkillCmd); err != nil {
		slog.WarnContext(exeletCtx, "ssh command to kill exelet failed", "error", err, "output", out)
	}

	// Poll for process exit.
	pgrepCmd := "pgrep -f " + remoteExeletPath
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
	pkillCmd = "sudo pkill -KILL -f " + remoteExeletPath
	sshExec(exeletCtx, ei.RemoteHost, pkillCmd)

	// Stop the exepipe process if there is one.
	if ei.exepipeCmd != nil {
		remoteExepipePath := "/tmp/exepipe-test-" + ei.testRunID
		sshExec(exeletCtx, ei.RemoteHost, "sudo pkill -KILL -f "+remoteExepipePath)
	}

	exeletCancel()

	// Stop the ssh process that started the exelet on the VM.
	ei.CmdCancel()
	ei.Cmd.Process.Kill()
	ei.Cmd.Wait()

	if ei.exepipeCmd != nil {
		ei.exepipeCmd.Process.Kill()
		ei.exepipeCmd.Wait()
	}

	// Close the Errors channel the caller may be using.
	select {
	case <-ei.exeletLoggerDone:
	case <-time.After(10 * time.Second):
	}
	close(ei.Errors)

	if ei.ExepipeErrors != nil {
		select {
		case <-ei.exepipeLoggerDone:
		case <-time.After(10 * time.Second):
		}
		close(ei.ExepipeErrors)
	}

	// Stop the ssh tunnels if there are any..
	if ei.TunnelCancel1 != nil {
		ei.TunnelCancel1()
	}
	if ei.TunnelCmd1 != nil && ei.TunnelCmd1.Process != nil {
		ei.TunnelCmd1.Process.Kill()
		ei.TunnelCmd1.Wait()
	}
	if ei.TunnelCancel2 != nil {
		ei.TunnelCancel2()
	}
	if ei.TunnelCmd2 != nil && ei.TunnelCmd2.Process != nil {
		ei.TunnelCmd2.Process.Kill()
		ei.TunnelCmd2.Wait()
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

	// Promote any new images to the shared cache before destroying the test dataset.
	remoteExecutor := func(ctx context.Context, cmd string) ([]byte, error) {
		return sshExec(ctx, ei.RemoteHost, cmd)
	}
	promoteImageVolumesWithExecutor(cleanupCtx, remoteExecutor, ei.ZFSDataset)

	// Remove the ZFS dataset, including the cloned image volumes.
	slog.InfoContext(cleanupCtx, "removing ZFS dataset", "dataset", ei.ZFSDataset)
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "sudo zfs destroy -r "+ei.ZFSDataset); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to remove ZFS dataset", "dataset", ei.ZFSDataset, "error", err, "output", out)
	}
	// Clean up snapshots we create on source image volumes for cloning.
	cleanupSnapshotsCmd := fmt.Sprintf("sudo zfs list -H -t snapshot -o name | grep '@e1e-%s$' | xargs -r -n1 sudo zfs destroy", ei.testRunID)
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
	if out, err := sshExec(cleanupCtx, ei.RemoteHost, "rm -f "+remoteExeletPath); err != nil {
		slog.ErrorContext(cleanupCtx, "failed to cleanup remote exelet binary", "error", err, "output", out)
	}

	// Remove local binary.
	os.Remove(filepath.Join(os.TempDir(), "exelet-test"))

	return localExeletCoverDir
}

// Exec runs a command on the exelet host (with sudo).
// For remote exelets, it uses SSH. For local exelets, it runs directly.
func (ei *ExeletInstance) Exec(ctx context.Context, command string) ([]byte, error) {
	if ei.RemoteHost == "" {
		return exec.CommandContext(ctx, "bash", "-c", "sudo "+command).CombinedOutput()
	}
	return sshExec(ctx, ei.RemoteHost, "sudo "+command)
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
		// Promote any new images to the shared cache before destroying the test dataset.
		localExecutor := func(ctx context.Context, cmd string) ([]byte, error) {
			return exec.CommandContext(ctx, "bash", "-c", cmd).CombinedOutput()
		}
		promoteImageVolumesWithExecutor(cleanupCtx, localExecutor, ei.ZFSDataset)

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
// It also promotes new images from tank/e1e-XXXX/sha256:* to tank/sha256:*
// during cleanup so subsequent runs can reuse them.
func cloneImageVolumesWithExecutor(ctx context.Context, execute cmdExecutor, zfsDataset, runID string) error {
	// List all ZFS datasets
	out, err := execute(ctx, "sudo zfs list -H -o name")
	if err != nil {
		return nil
	}

	// Filter for tank/sha256:* volumes (the cached base images).
	var volumes []string
	for line := range bytes.SplitSeq(bytes.TrimSpace(out), []byte{'\n'}) {
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

// promoteImageVolumesWithExecutor promotes image volumes from a test dataset to the shared cache.
// This copies tank/e1e-XXXX/sha256:* to tank/sha256:* so subsequent test runs can reuse them.
// Uses ZFS send/receive to create independent copies (not clones) that don't depend on the test dataset.
func promoteImageVolumesWithExecutor(ctx context.Context, execute cmdExecutor, zfsDataset string) {
	// List all datasets under the test dataset
	out, err := execute(ctx, "sudo zfs list -H -o name -r "+zfsDataset)
	if err != nil {
		return
	}

	// Find sha256:* volumes (completed images, not tmp-sha256:*)
	for line := range bytes.SplitSeq(bytes.TrimSpace(out), []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		dsName := string(line)

		// Skip non-sha256 volumes and temp volumes
		if !strings.Contains(dsName, "/sha256:") || strings.Contains(dsName, "/tmp-sha256:") {
			continue
		}

		// Extract the sha256:... part
		parts := strings.SplitN(dsName, "/sha256:", 2)
		if len(parts) != 2 {
			continue
		}
		imageID := "sha256:" + parts[1]
		destVolume := "tank/" + imageID

		// Check if destination already exists
		if _, err := execute(ctx, "sudo zfs list "+destVolume+" 2>/dev/null"); err == nil {
			// Already cached
			continue
		}

		slog.InfoContext(ctx, "promoting image to shared cache", "src", dsName, "dest", destVolume)

		// Use zfs send/receive to create an independent copy
		// Create a temporary snapshot for the send
		snapName := dsName + "@promote"
		if _, err := execute(ctx, "sudo zfs snapshot "+snapName); err != nil {
			slog.WarnContext(ctx, "failed to create snapshot for promotion", "src", dsName, "error", err)
			continue
		}

		// Send/receive to create independent copy
		sendRecvCmd := fmt.Sprintf("sudo zfs send %s | sudo zfs receive %s", snapName, destVolume)
		if _, err := execute(ctx, sendRecvCmd); err != nil {
			slog.WarnContext(ctx, "failed to promote image", "src", dsName, "dest", destVolume, "error", err)
			// Clean up snapshot
			execute(ctx, "sudo zfs destroy "+snapName+" 2>/dev/null || true")
			continue
		}

		// Clean up the promotion snapshot on the source
		execute(ctx, "sudo zfs destroy "+snapName+" 2>/dev/null || true")
		// Clean up the snapshot that zfs receive creates on the destination
		execute(ctx, "sudo zfs destroy "+destVolume+"@promote 2>/dev/null || true")

		slog.InfoContext(ctx, "promoted image to shared cache", "imageID", imageID)
	}
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

// localProxyURL returns a URL that the exelet can use to reach
// the local proxy at the given port. It also returns the SSH tunnel
// command and cancellation function, though they may be nil if not needed.
func localProxyURL(ctx context.Context, host, gateway string, port int, logPorts bool) (string, *exec.Cmd, context.CancelFunc, error) {
	// Test if the VM can reach the local proxy.
	// Usually local->VM and VM->local connectivity works.
	// However, in some environments, such as coding agents that
	// operate in containers, this connectivity does NOT work,
	// and we set up an SSH tunnel for exelet->exed communication
	// as a band-aid.
	hasConnectivity := testRemoteToLocalConnectivity(ctx, host, gateway, port)
	slog.InfoContext(ctx, "test remote->local connectivity", "host", host, "gateway", gateway, "port", port, "reachable", hasConnectivity)

	// Determine the URL the exelet will use to reach exed.
	needsTunnel := !hasConnectivity
	if !needsTunnel {
		return fmt.Sprintf("http://%s:%d", gateway, port), nil, nil, nil
	}

	slog.InfoContext(ctx, "remote->local connectivity not available, using SSH reverse tunnel")

	// Use SSH reverse tunnel:
	// exelet -> SSH tunnel -> TCP proxy -> exed
	remotePort, cmd, cancel, err := startSSHTunnel(ctx, host, port)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to start SSH tunnel: %w", err)
	}

	proxyURL := fmt.Sprintf("http://localhost:%d", remotePort)

	if logPorts {
		slog.InfoContext(ctx, "using SSH tuennl for exelet->exed/exeprox", "remote_port", remotePort, "proxy_port", port)
	}

	return proxyURL, cmd, cancel, nil
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
func BuildExeletBinary(ctx context.Context, testRunID string) (string, error) {
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
	cmd := exec.CommandContext(ctx, "make", "exelet-coverage")
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

	// Move to test binary path. Use rename when possible, fall back to
	// copy+remove for cross-device moves (e.g., project dir and /tmp on
	// different filesystems).
	if err := moveFile(filepath.Join(srcdir, "exeletd"), binPath); err != nil {
		return "", fmt.Errorf("failed to move exelet to %s: %v", binPath, err)
	}

	return binPath, nil
}

// moveFile attempts os.Rename and falls back to copy+remove when src and dst
// are on different filesystems (EXDEV / "invalid cross-device link").
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}

	return os.Remove(src)
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
