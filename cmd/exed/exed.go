package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"exe.dev/execore"
	"exe.dev/logging"
	"exe.dev/stage"
	"exe.dev/version"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := flag.String("http", ":8080", "HTTP server address, empty to disable")
	sshAddr := flag.String("ssh", ":2223", "SSH server address")
	pluginAddr := flag.String("piper-plugin", ":2224", "Piper plugin gRPC server address")
	piperdPort := flag.Int("piperd-port", 2222, "sshpiper listening port")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt), empty to disable")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)
	exeletAddresses := flag.String("exelet-addresses", "", "Comma-separated list of exelet addresses (e.g., 'tcp://host1:8080,tcp://host2:8080')")
	ghWhoAmIPath := flag.String("gh-whoami", "ghuser/whoami.sqlite3", "GitHub user key database path")
	fakeHTTPEmail := flag.String("fake-email-server", "", "HTTP email server URL for sending emails (e.g., http://localhost:8025)")
	// TODO(ian): Remove this unused flag when we are sure
	// no script still uses it.
	flag.String("gateway", "", "unused")
	openBrowser := flag.Bool("open", false, "Open web browser to HTTP server (local/test only)")
	profilePath := flag.String("profile", "", "Enable CPU profiling for 30 seconds, saving to /tmp/exed-profile-<timestamp>.prof or specified path")
	startExelet := flag.Bool("start-exelet", false, "Build and start exelet on lima-exe-ctr (local/test only)")
	multiExelet := flag.Bool("multi-exelet", false, "with -start-exelet, also start exelet on lima-exe-ctr-tests; may interact badly with concurrent automated tests")
	flag.Parse()

	// Parse stage
	env, err := stage.Parse(*stageName)
	if err != nil {
		return err
	}

	// Validate -open flag (local/test only)
	if *openBrowser && !env.WebDev {
		return fmt.Errorf("-open flag is only available in webdev-enabled stages")
	}

	// Validate -start-exelet flag (local/test only)
	if *startExelet && !env.ReplDev {
		return fmt.Errorf("-start-exelet flag is only available in repldev-enabled stages")
	}

	// -multi-exelet requires -start-exelet
	if *multiExelet && !*startExelet {
		return fmt.Errorf("-multi-exelet requires -start-exelet")
	}

	// Validate -start-exelet is incompatible with explicit addresses
	if *startExelet {
		if *exeletAddresses != "" {
			return fmt.Errorf("-start-exelet is incompatible with -exelet-addresses (addresses are auto-determined)")
		}
	}

	// Create metrics registry and setup structured logging with metrics
	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metricsRegistry.MustRegister(prometheus.NewGoCollector())
	version.RegisterBuildInfo(metricsRegistry)
	logging.SetupLogger(env, metricsRegistry, &logging.ResourceAttrs{
		ServiceVersion: version.BuildVersion(),
		DeploymentEnv:  *stageName,
	})
	slog.Info("Starting exed server")

	// Start exelet(s) if requested
	if *startExelet {
		addr, gw, err := startExeletsRemote(env, *httpAddr, *multiExelet)
		if err != nil {
			return fmt.Errorf("failed to start exelets: %w", err)
		}
		slog.Info("exelets started successfully", "addresses", addr, "gateway", gw)

		// Set the exelet-addresses
		*exeletAddresses = addr
	}

	// Start CPU profiling if requested
	var enableProfiling bool
	var profPath string

	if *profilePath != "" {
		enableProfiling = true
		profPath = *profilePath
	} else {
		// Check for empty string flag (user passed -profile without value)
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "profile" {
				enableProfiling = true
			}
		})
	}

	if enableProfiling {
		if profPath == "" {
			profPath = filepath.Join("/tmp", fmt.Sprintf("exed-profile-%d.prof", time.Now().Unix()))
		}

		f, err := os.Create(profPath)
		if err != nil {
			return fmt.Errorf("failed to create profile file: %w", err)
		}

		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return fmt.Errorf("failed to start CPU profile: %w", err)
		}

		go func() {
			time.Sleep(30 * time.Second)
			pprof.StopCPUProfile()
			f.Close()
			slog.Info("CPU profile written", "path", profPath)
		}()

		slog.Info("CPU profiling started for 30 seconds", "path", profPath)
	}

	// Parse exelet addresses
	var exeletAddrs []string
	if *exeletAddresses != "" {
		exeletAddrs = strings.Split(*exeletAddresses, ",")
		for i, addr := range exeletAddrs {
			exeletAddrs[i] = strings.TrimSpace(addr)
		}
	}
	if len(exeletAddrs) == 0 {
		slog.Debug("No exelet addresses specified, VM functionality will be disabled",
			"suggestion", "Use -exelet-addresses flag to enable VM-based instances")
	}

	switch strings.ToLower(*dbPath) {
	case "tmp":
		f, err := os.CreateTemp("", "exe.db")
		if err != nil {
			return fmt.Errorf("failed to create temp db file: %w", err)
		}
		*dbPath = f.Name()
		slog.Info("created temporary exe.db", "path", *dbPath)
	}

	if env.StripeAPIKey == "" {
		return fmt.Errorf("STRIPE_API_KEY environment variable is required")
	}

	server, err := execore.NewServer(execore.ServerConfig{
		Logger:          slog.Default(),
		HTTPAddr:        *httpAddr,
		HTTPSAddr:       *httpsAddr,
		SSHAddr:         *sshAddr,
		PluginAddr:      *pluginAddr,
		DBPath:          *dbPath,
		FakeEmailServer: *fakeHTTPEmail,
		PiperdPort:      *piperdPort,
		GHWhoAmIPath:    *ghWhoAmIPath,
		ExeletAddresses: exeletAddrs,
		Env:             env,
		MetricsRegistry: metricsRegistry,
	})
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Open browser if requested (dev mode only)
	if *openBrowser {
		go func() {
			// Wait a moment for the server to start
			time.Sleep(500 * time.Millisecond)
			url := fmt.Sprintf("http://localhost%s", *httpAddr)
			if err := openBrowserURL(url); err != nil {
				slog.Warn("failed to open browser", "error", err)
			} else {
				slog.Info("opened browser", "url", url)
			}
		}()
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil // unreachable
}

func openBrowserURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// startSSHTunnelForExed establishes an SSH reverse tunnel and returns the dynamically allocated remote port.
// Uses -v flag to capture SSH debug output showing the allocated port.
func startSSHTunnelForExed(host string, localPort int) (int, error) {
	// Start SSH tunnel with -v to capture allocated port
	tunnelCmd := exec.Command("ssh",
		"-v",  // verbose to see allocated port
		"-Nf", // no command, background
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("0:localhost:%d", localPort),
		host,
	)

	// Capture stderr to parse allocated port
	stderrPipe, err := tunnelCmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := tunnelCmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start SSH tunnel: %w", err)
	}

	// Parse stderr for "Allocated port X for remote forward"
	re := regexp.MustCompile(`Allocated port (\d+) for remote forward`)
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		line := scanner.Text()
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil {
				// Wait for command to finish backgrounding
				tunnelCmd.Wait()
				return port, nil
			}
		}
	}

	return 0, fmt.Errorf("failed to parse allocated port from SSH output")
}

// testRemoteToLocalConnectivity checks if the remote host can reach the local port via gateway.
// Returns true if connectivity works, false otherwise.
func testRemoteToLocalConnectivity(ctx context.Context, host, gateway string, port int) bool {
	// Try to connect from remote host to gateway:port
	testCmd := fmt.Sprintf("timeout 2 nc -z %s %d 2>/dev/null", gateway, port)
	_, err := sshExec(ctx, host, testCmd)
	return err == nil
}

// startExeletsRemote builds exelet, uploads to lima dev host(s), kills old instances, and starts them.
// If multiExelet is true, also starts on lima-exe-ctr-tests.
// Returns a comma-separated list of exelet addresses and gateway address.
func startExeletsRemote(env stage.Env, httpAddr string, multiExelet bool) (string, string, error) {
	const (
		// Primary, normal dev lima VM
		limaDevHost = "lima-exe-ctr.local"
		// Secondary lima VM, normally reserved for automated tests
		limaDevHostTests = "lima-exe-ctr-tests.local"
	)

	hosts := []string{limaDevHost}
	if multiExelet {
		hosts = append(hosts, limaDevHostTests)
	}
	slog.Info("starting remote exelets", "hosts", hosts)

	// Build exelet binary
	slog.Info("building exelet binary")
	binPath, err := buildExeletBinary()
	if err != nil {
		return "", "", fmt.Errorf("failed to build exelet: %w", err)
	}

	ctx := context.Background()

	// Get the gateway address early so we can construct exed URL
	// All Lima VMs on the same Mac share the same gateway, so use primary host.
	gateway, err := sshExec(ctx, limaDevHost, "getent ahostsv4 _gateway | grep _gateway | awk '{ print $1; }'")
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve gateway: %w", err)
	}
	gateway = strings.TrimSpace(gateway)
	if gateway == "" {
		return "", "", fmt.Errorf("gateway address is empty")
	}

	// Set up a little single-use net.Listener to detect connectivity.
	// We do this because the HTTP server (which is the natural thing to use) isn't started yet.
	tmpLn, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", "", fmt.Errorf("failed to set up local listener: %w", err)
	}
	tmpPort := tmpLn.Addr().(*net.TCPAddr).Port
	// Test if remote host can reach local port
	// Usually local->ssh_ctr and ssh_ctr->local connectivity works. However, in some
	// environments, such as coding agents that operate in containers, this connectivity
	// does NOT work, and we set up an SSH tunnel for the exelet->exed communication
	// as a band-aid.
	needsTunnel := !testRemoteToLocalConnectivity(ctx, limaDevHost, gateway, tmpPort)
	tmpLn.Close()

	var exeletAddrs []string
	for _, host := range hosts {
		addr, err := startExeletOnHost(ctx, host, binPath, env.LogFormat, env.LogLevel, httpAddr, gateway, needsTunnel)
		if err != nil {
			return "", "", fmt.Errorf("failed to start exelet on %q: %w", host, err)
		}
		exeletAddrs = append(exeletAddrs, addr)
	}

	slog.InfoContext(ctx, "exelets started", "addresses", exeletAddrs, "gateway", gateway)
	return strings.Join(exeletAddrs, ","), gateway, nil
}

// startExeletOnHost starts exelet on a single host. Returns the exelet address.
func startExeletOnHost(ctx context.Context, host, binPath, logFormat, logLevel, httpAddr, gateway string, needsTunnel bool) (string, error) {
	slog.InfoContext(ctx, "starting remote exelet", "host", host)

	// Stop any existing exelet instances
	// no error handling because pkill fails if there's nothing to kill
	sshExec(ctx, host, "sudo pkill -9 -f exeletd")

	// Upload binary
	remotePath := "/tmp/exeletd"
	slog.InfoContext(ctx, "uploading exelet to remote host", "host", host, "path", remotePath)
	if err := scpUpload(binPath, host, remotePath); err != nil {
		return "", fmt.Errorf("failed to upload exelet: %w", err)
	}

	// Make executable
	if _, err := sshExec(ctx, host, "chmod +x "+remotePath); err != nil {
		return "", fmt.Errorf("failed to chmod exelet: %w", err)
	}

	// Construct exed URL
	var exedURL string
	if needsTunnel {
		// Parse out what will be the HTTP port from httpAddr.
		_, httpPortStr, _ := net.SplitHostPort(httpAddr)
		httpPort, _ := strconv.Atoi(httpPortStr)
		if httpPort == 0 {
			// Parse failed, or dynamic port. Use gateway approach.
			exedURL = fmt.Sprintf("http://%s%s", gateway, httpAddr)
			slog.InfoContext(ctx, "starting exeletd on remote host", "exed_url", exedURL)
			if err := startExeletProcess(ctx, host, logFormat, logLevel, exedURL); err != nil {
				return "", err
			}
			return waitForExeletAddress(host)
		}
		slog.InfoContext(ctx, "remote->local connectivity not available, using SSH reverse tunnel", "http_port", httpPort)

		// Start SSH tunnel and discover remote port
		remotePort, err := startSSHTunnelForExed(host, httpPort)
		if err != nil {
			return "", fmt.Errorf("failed to start SSH tunnel: %w", err)
		}

		slog.InfoContext(ctx, "SSH tunnel established", "remote_port", remotePort, "local_port", httpPort)
		exedURL = fmt.Sprintf("http://localhost:%d", remotePort)
	} else {
		// Use direct gateway access (traditional approach)
		exedURL = fmt.Sprintf("http://%s%s", gateway, httpAddr)
	}

	// Start exelet via SSH - the SSH command will keep running
	slog.InfoContext(ctx, "starting exeletd on remote host", "exed_url", exedURL)
	if err := startExeletProcess(ctx, host, logFormat, logLevel, exedURL); err != nil {
		return "", err
	}
	return waitForExeletAddress(host)
}

// startExeletProcess starts the exelet process on the remote host via SSH.
func startExeletProcess(ctx context.Context, host, logFormat, logLevel, exedURL string) error {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		host,
		fmt.Sprintf(`sudo LOG_FORMAT=%s LOG_LEVEL=%s /tmp/exeletd -D --stage local --data-dir /data/exelet --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" --network-manager-address nat:///data/exelet/network --runtime-address cloudhypervisor:///data/exelet/runtime --listen-address tcp://:9080 --http-addr :9081 --exed-url %s --instance-domain exe.cloud --enable-hugepages`,
			logFormat, logLevel, exedURL))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start exeletd: %w", err)
	}

	// Wait for the process in a separate goroutine
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.ErrorContext(ctx, "exeletd ssh process exited with error", "error", err)
		} else {
			slog.InfoContext(ctx, "exeletd ssh process exited normally")
		}
	}()

	return nil
}

// waitForExeletAddress waits for exelet to start and returns its address.
func waitForExeletAddress(host string) (string, error) {
	// Wait for exelet to start by aggressively trying to connect to the port
	slog.Info("waiting for exelet to start listening", "host", host)
	exeletPort := "9080"
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)
	connected := false
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", host+":"+exeletPort, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			connected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !connected {
		return "", fmt.Errorf("timeout waiting for exelet to listen on %s:%s", host, exeletPort)
	}

	// Construct the exelet address
	exeletAddr := fmt.Sprintf("tcp://%s:%s", host, exeletPort)

	slog.Info("exelet startup complete", "address", exeletAddr)
	return exeletAddr, nil
}

// buildExeletBinary builds exelet for Linux and returns path to the binary
func buildExeletBinary() (string, error) {
	binPath := filepath.Join(os.TempDir(), "exeletd")

	// Run make to build exelet (includes dependencies like rovol and kernel)
	cmd := exec.Command("make", "exelet")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH)

	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("make exelet failed: %w\n%s", err, out)
	}

	// Rename the built binary to temp location
	if err := os.Rename("exeletd", binPath); err != nil {
		return "", fmt.Errorf("failed to rename exeletd to %s: %w", binPath, err)
	}

	return binPath, nil
}

// sshExec executes a command on remote host via SSH
func sshExec(ctx context.Context, host, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		host, command)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scpUpload uploads a file to remote host via SCP
func scpUpload(localPath, host, remotePath string) error {
	cmd := exec.Command("scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		localPath, host+":"+remotePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp failed: %w\n%s", err, out)
	}
	return nil
}
