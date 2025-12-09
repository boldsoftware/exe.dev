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
	"slices"
	"strconv"
	"strings"
	"time"

	"exe.dev/execore"
	"exe.dev/logging"
	"exe.dev/stage"
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
	devMode := flag.String("dev", "", `development mode: "" (production), "local" (local containerd), or "test" (test mode)`)
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test", overridden by -dev flag`)
	exeletAddresses := flag.String("exelet-addresses", "", "Comma-separated list of exelet addresses (e.g., 'tcp://host1:8080,tcp://host2:8080')")
	ghWhoAmIPath := flag.String("gh-whoami", "ghuser/whoami.sqlite3", "GitHub user key database path")
	fakeHTTPEmail := flag.String("fake-email-server", "", "HTTP email server URL for sending emails (e.g., http://localhost:8025)")
	// TODO(philip): Once newer shelleys are deployed, we don't need this any more.
	gateway := flag.String("gateway", "exe.dev", "Gateway endpoint for Shelley")
	openBrowser := flag.Bool("open", false, "Open web browser to HTTP server (dev mode only)")
	profilePath := flag.String("profile", "", "Enable CPU profiling for 30 seconds, saving to /tmp/exed-profile-<timestamp>.prof or specified path")
	startExelet := flag.Bool("start-exelet", false, "Build and start exelet on lima-exe-ctr (dev mode only)")
	flag.Parse()

	// Validate dev mode
	if *devMode != "" && *devMode != "local" && *devMode != "test" {
		return fmt.Errorf(`valid dev modes are "", "local", and "test", got: %q`, *devMode)
	}

	// Validate stage
	validStages := []string{"prod", "staging", "local", "test"}
	if !slices.Contains(validStages, *stageName) {
		return fmt.Errorf(`valid stages are %q, got: %q`, validStages, *stageName)
	}

	// Override stage if dev mode is set
	switch *devMode {
	case "local":
		*stageName = "local"
	case "test":
		*stageName = "test"
	}

	// Look up env and synchronize stage back to dev mode
	var env stage.Env
	switch *stageName {
	case "prod":
		env = stage.Prod()
	case "staging":
		env = stage.Staging()
	case "local":
		env = stage.Local()
	case "test":
		env = stage.Test()
	default:
		return fmt.Errorf("unsupported stage: %q", *stageName)
	}

	// Validate -open flag (dev mode only)
	if *openBrowser && *devMode == "" {
		return fmt.Errorf("-open flag is only available in dev mode")
	}

	// Validate -start-exelet flag (dev mode only)
	if *startExelet && *devMode == "" {
		return fmt.Errorf("-start-exelet flag is only available in dev mode")
	}

	// Validate -start-exelet is incompatible with explicit addresses/gateway
	if *startExelet {
		if *exeletAddresses != "" {
			return fmt.Errorf("-start-exelet is incompatible with -exelet-addresses (addresses are auto-determined)")
		}
		if *gateway != "exe.dev" {
			return fmt.Errorf("-start-exelet is incompatible with -gateway (gateway is auto-determined)")
		}
	}

	// Create metrics registry and setup structured logging with metrics
	metricsRegistry := prometheus.NewRegistry()
	logging.SetupLogger(*devMode, metricsRegistry)
	slog.Info("Starting exed server")

	// Start exelet if requested
	if *startExelet {
		addr, gw, err := startExeletRemote(*devMode, *httpAddr)
		if err != nil {
			return fmt.Errorf("failed to start exelet: %w", err)
		}
		slog.Info("exelet started successfully", "address", addr, "gateway", gw)

		// Set the exelet-addresses and gateway
		*exeletAddresses = addr
		*gateway = gw
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

	server, err := execore.NewServer(slog.Default(), *httpAddr, *httpsAddr, *sshAddr, *pluginAddr, *dbPath, *fakeHTTPEmail, *piperdPort, *ghWhoAmIPath, exeletAddrs, env, metricsRegistry)
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

// startExeletRemote builds exelet, uploads to lima-exe-ctr, kills old instances, and starts it.
// Returns the exelet address (tcp://lima-exe-ctr.local:PORT) and gateway address (which
// is the address by which the exelet can dial the exed process).
func startExeletRemote(devMode, httpAddr string) (string, string, error) {
	host := "lima-exe-ctr.local"
	slog.Info("starting remote exelet", "host", host)

	// Build exelet binary
	slog.Info("building exelet binary")
	binPath, err := buildExeletBinary()
	if err != nil {
		return "", "", fmt.Errorf("failed to build exelet: %w", err)
	}

	ctx := context.Background()

	// Stop any existing exelet instances
	// no error handling because pkill fails if there's nothing to kill
	sshExec(ctx, host, "sudo pkill -9 -f exeletd")

	// Upload binary
	remotePath := "/tmp/exeletd"
	slog.InfoContext(ctx, "uploading exelet to remote host", "host", host, "path", remotePath)
	if err := scpUpload(binPath, host, remotePath); err != nil {
		return "", "", fmt.Errorf("failed to upload exelet: %w", err)
	}

	// Make executable
	if _, err := sshExec(ctx, host, "chmod +x "+remotePath); err != nil {
		return "", "", fmt.Errorf("failed to chmod exelet: %w", err)
	}

	// Determine LOG_FORMAT and LOG_LEVEL from environment, with dev mode defaults
	logFormat := os.Getenv("LOG_FORMAT")
	if logFormat == "" {
		if devMode != "" {
			logFormat = "tint"
		} else {
			logFormat = "text"
		}
	}
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "debug"
	}

	// Get the VM IP address early so we can construct exed URL
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", "", fmt.Errorf("failed to lookup ip for %s: %w", host, err)
	}
	var remoteIp string
	for _, ip := range ips {
		if ip.To4() != nil {
			remoteIp = ip.String()
			break
		}
	}
	if remoteIp == "" {
		return "", "", fmt.Errorf("no ipv4 address found for %s", host)
	}

	// Get the gateway address early so we can construct exed URL
	gateway, err := sshExec(ctx, host, "getent ahostsv4 _gateway | grep _gateway | awk '{ print $1; }'")
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
	defer tmpLn.Close()
	tmpPort := tmpLn.Addr().(*net.TCPAddr).Port

	// Test if remote host can reach local port
	// Usually local->ssh_ctr and ssh_ctr->local connectivity works. However, in some
	// environments, such as coding agents that operate in containers, this connectivity
	// does NOT work, and we set up an SSH tunnel for the exelet->exed communication
	// as a band-aid.
	needsTunnel := !testRemoteToLocalConnectivity(ctx, host, gateway, tmpPort)

	// Construct exed URL
	var exedURL string
	if needsTunnel {
		// Parse out what will be the HTTP port from httpAddr.
		_, httpPortStr, _ := net.SplitHostPort(httpAddr)
		httpPort, _ := strconv.Atoi(httpPortStr)
		if httpPort == 0 {
			// Parse failed, or dynamic port. Use gateway approach.
			exedURL := fmt.Sprintf("http://%s%s", gateway, httpAddr)
			slog.InfoContext(ctx, "starting exeletd on remote host", "exed_url", exedURL)
			if err := startExeletProcess(ctx, host, logFormat, logLevel, exedURL); err != nil {
				return "", "", err
			}
			return waitForExeletAndReturnAddress(host, gateway)
		}
		slog.InfoContext(ctx, "remote->local connectivity not available, using SSH reverse tunnel", "http_port", httpPort)

		// Start SSH tunnel and discover remote port
		remotePort, err := startSSHTunnelForExed(host, httpPort)
		if err != nil {
			return "", "", fmt.Errorf("failed to start SSH tunnel: %w", err)
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
		return "", "", err
	}
	return waitForExeletAndReturnAddress(host, gateway)
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
		fmt.Sprintf(`sudo LOG_FORMAT=%s LOG_LEVEL=%s /tmp/exeletd -D --data-dir /data/exelet --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" --network-manager-address nat:///data/exelet/network --runtime-address cloudhypervisor:///data/exelet/runtime --listen-address tcp://:9080 --http-addr :9081 --exed-url %s`,
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

// waitForExeletAndReturnAddress waits for exelet to start and returns its address.
func waitForExeletAndReturnAddress(host, gateway string) (string, string, error) {
	// Wait for exelet to start by aggressively trying to connect to the port
	slog.Info("waiting for exelet to start listening")
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
		return "", "", fmt.Errorf("timeout waiting for exelet to listen on %s:%s", host, exeletPort)
	}

	// Construct the exelet address
	exeletAddr := fmt.Sprintf("tcp://%s:%s", host, exeletPort)

	slog.Info("exelet startup complete", "address", exeletAddr, "gateway", gateway)
	return exeletAddr, gateway, nil
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
