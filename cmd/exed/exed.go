package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/execore"
	"exe.dev/exedb"
	"exe.dev/logging"
	"exe.dev/stage"
	"exe.dev/ui"
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
	sshAddr := flag.String("ssh", "localhost:2223", "SSH server address")
	pluginAddr := flag.String("piper-plugin", "localhost:2224", "Piper plugin gRPC server address")
	piperdPort := flag.Int("piperd-port", 2222, "sshpiper listening port")
	exeproxServicePort := flag.Int("exeprox-service-port", 2225, "port for gRPC server for exeprox clients")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt), empty to disable")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)
	exeletAddresses := flag.String("exelet-addresses", "", "Comma-separated list of exelet addresses (e.g., 'tcp://host1:8080,tcp://host2:8080')")
	ghWhoAmIPath := flag.String("gh-whoami", "ghuser/whoami.sqlite3", "GitHub user key database path")
	fakeHTTPEmail := flag.String("fake-email-server", "", "HTTP email server URL for sending emails (e.g., http://localhost:8025)")
	// TODO(ian): Remove this unused flag when we are sure
	// no script still uses it.
	flag.String("gateway", "", "unused")
	preflight := flag.Bool("preflight", false, "Run database migrations and exit (for preflight checks)")
	openBrowser := flag.Bool("open", false, "Open web browser to HTTP server (local/test only)")
	profilePath := flag.String("profile", "", "Enable CPU profiling for 30 seconds, saving to /tmp/exed-profile-<timestamp>.prof or specified path")
	startExelet := flag.Bool("start-exelet", false, "Build and start exelet locally or on lima-exe-ctr (local/test only)")
	multiExelet := flag.Bool("multi-exelet", false, "with -start-exelet, also start exelet on lima-exe-ctr-tests; may interact badly with concurrent automated tests")
	enableExeletStorageReplication := flag.Bool("enable-exelet-storage-replication", false, "with -multi-exelet, enable storage replication from exe-ctr to exe-ctr-tests")
	startMetricsd := flag.Bool("start-metricsd", false, "with -start-exelet, also start metricsd locally and configure exelet to send metrics")
	metricsdURLFlag := flag.String("metricsd-url", "", "URL of metricsd server for usage data (e.g. http://localhost:21090); auto-set with -start-metricsd")
	lmtpSocketDefault := "/var/run/exed/lmtp.sock"
	if runtime.GOOS == "darwin" {
		lmtpSocketDefault = filepath.Join(os.TempDir(), "exed-lmtp.sock")
	}
	lmtpSocket := flag.String("lmtp-socket", lmtpSocketDefault, "LMTP socket path; empty to disable")
	flag.Parse()

	// Parse stage
	env, err := stage.Parse(*stageName)
	if err != nil {
		return err
	}

	if *preflight {
		slog.Info("preflight: running migrations", "db", *dbPath)
		rawDB, err := sql.Open("sqlite", *dbPath)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer rawDB.Close()
		if err := exedb.RunMigrations(slog.Default(), rawDB); err != nil {
			return fmt.Errorf("preflight failed: %w", err)
		}
		if !ui.HasIndex() {
			return fmt.Errorf("preflight failed: dashboard UI not built; run: cd ui && pnpm install --frozen-lockfile && pnpm build")
		}
		slog.Info("preflight: ok")
		return nil
	}

	// Disable LMTP if the stage doesn't enable it
	if !env.EnableLMTP {
		*lmtpSocket = ""
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

	// -multi-exelet is only supported on Darwin (Lima VMs)
	if *multiExelet && runtime.GOOS != "darwin" {
		return fmt.Errorf("-multi-exelet is only supported on macOS (requires Lima VMs)")
	}

	// -enable-exelet-storage-replication requires -multi-exelet (needs two hosts)
	if *enableExeletStorageReplication && !*multiExelet {
		return fmt.Errorf("-enable-exelet-storage-replication requires -multi-exelet")
	}

	// -start-metricsd requires -start-exelet
	if *startMetricsd && !*startExelet {
		return fmt.Errorf("-start-metricsd requires -start-exelet")
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

	// Resolve metricsd URL from flag or locally-started instance
	metricsdURL := *metricsdURLFlag

	// Start exelet(s) if requested
	if *startExelet {
		// Start metricsd if requested (must start before exelet so we have the URL)
		if *startMetricsd {
			url, cleanup, err := startMetricsdLocal(*dbPath)
			if err != nil {
				return fmt.Errorf("failed to start metricsd: %w", err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			metricsdURL = url
			slog.Info("metricsd started", "url", metricsdURL)
		}

		if runtime.GOOS == "linux" {
			addr, cleanup, err := startExeletsLocal(env, *httpAddr, metricsdURL)
			if err != nil {
				return fmt.Errorf("failed to start local exelet: %w", err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			slog.Info("exelet started successfully", "address", addr)
			*exeletAddresses = addr
		} else {
			addr, gw, cleanupForwarder, err := startExeletsRemote(env, *httpAddr, *multiExelet, *enableExeletStorageReplication, metricsdURL)
			if err != nil {
				return fmt.Errorf("failed to start exelets: %w", err)
			}
			if cleanupForwarder != nil {
				defer cleanupForwarder()
			}
			slog.Info("exelets started successfully", "addresses", addr, "gateway", gw)
			*exeletAddresses = addr
		}
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

	// Dashboard UI: embedded in the binary at build time.
	// Build with: cd ui && pnpm install --frozen-lockfile && pnpm build
	if !ui.HasIndex() {
		return fmt.Errorf("dashboard UI not built, run: cd ui && pnpm install --frozen-lockfile && pnpm build")
	}
	dashboardUI := ui.DistFS()
	warnIfUIStale()

	server, err := execore.NewServer(execore.ServerConfig{
		Logger:             slog.Default(),
		HTTPAddr:           *httpAddr,
		HTTPSAddr:          *httpsAddr,
		SSHAddr:            *sshAddr,
		PluginAddr:         *pluginAddr,
		ExeproxServicePort: *exeproxServicePort,
		DBPath:             *dbPath,
		FakeEmailServer:    *fakeHTTPEmail,
		PiperdPort:         *piperdPort,
		GHWhoAmIPath:       *ghWhoAmIPath,
		ExeletAddresses:    exeletAddrs,
		Env:                env,
		Billing:            &billing.Manager{},
		MetricsRegistry:    metricsRegistry,
		LMTPSocketPath:     *lmtpSocket,
		MetricsdURL:        metricsdURL,
		DashboardUI:        dashboardUI,
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
	host = normalizeLimaSSHHost(host)
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
// If enableReplication is true, enables storage replication from primary to secondary host.
// Returns a comma-separated list of exelet addresses and gateway address.
func startExeletsRemote(env stage.Env, httpAddr string, multiExelet, enableReplication bool, metricsdURL string) (_, _ string, cleanup func(), retErr error) {
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
		return "", "", nil, fmt.Errorf("failed to build exelet: %w", err)
	}

	ctx := context.Background()

	// Get the gateway address early so we can construct exed URL
	// All Lima VMs on the same Mac share the same gateway, so use primary host.
	gateway, err := sshExec(ctx, limaDevHost, "getent ahostsv4 _gateway | grep _gateway | awk '{ print $1; }'")
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to resolve gateway: %w", err)
	}
	gateway = strings.TrimSpace(gateway)
	if gateway == "" {
		return "", "", nil, fmt.Errorf("gateway address is empty")
	}

	// Verify mDNS resolution for each host. The .local suffix relies on
	// multicast DNS (avahi inside the VM). After a VM restart, avahi may
	// detect a stale mDNS record held by macOS and register with a "-2"
	// suffix, causing all direct TCP connections to the host to fail.
	for _, host := range hosts {
		if err := verifyOrFixMDNS(ctx, host); err != nil {
			return "", "", nil, err
		}
	}

	// Set up a little single-use net.Listener to detect connectivity.
	// We do this because the HTTP server (which is the natural thing to use) isn't started yet.
	tmpLn, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to set up local listener: %w", err)
	}
	tmpPort := tmpLn.Addr().(*net.TCPAddr).Port
	// Test if remote host can reach local port
	// Usually local->ssh_ctr and ssh_ctr->local connectivity works. However, in some
	// environments, such as coding agents that operate in containers, this connectivity
	// does NOT work, and we set up an SSH tunnel for the exelet->exed communication
	// as a band-aid.
	needsTunnel := !testRemoteToLocalConnectivity(ctx, limaDevHost, gateway, tmpPort)
	tmpLn.Close()

	// Determine replication target.
	// EXELET_STORAGE_REPLICATION_TARGET overrides for non-SSH local targets
	// (e.g., "zpool:///backup"). SSH targets still need the socat tunnel
	// managed by -enable-exelet-storage-replication, so the env var is
	// ignored when it contains an ssh:// URL.
	var replicationTarget string
	if envTarget := os.Getenv("EXELET_STORAGE_REPLICATION_TARGET"); envTarget != "" && !strings.HasPrefix(envTarget, "ssh://") {
		replicationTarget = envTarget
		slog.InfoContext(ctx, "using replication target from environment", "target", replicationTarget)
	}
	if replicationTarget == "" && enableReplication {
		sshHost, sshPort, err := getSSHHostPort(limaDevHostTests)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to get SSH config for %s: %w", limaDevHostTests, err)
		}
		forwarderPort, err := getFreePort(gateway)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to allocate port for replication forwarder: %w", err)
		}
		socatCmd, err := startReplicationForwarder(ctx, gateway, forwarderPort, sshHost, sshPort)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to start replication forwarder: %w", err)
		}
		// Return a cleanup function that kills socat on any exit path.
		cleanup = func() {
			if socatCmd.Process != nil {
				socatCmd.Process.Kill()
			}
		}
		defer func() {
			// If we return an error, clean up immediately and clear the
			// cleanup return so the caller doesn't double-kill.
			if retErr != nil && cleanup != nil {
				cleanup()
				cleanup = nil
			}
		}()
		replicationTarget = fmt.Sprintf("ssh://root@%s:%d/tank", gateway, forwarderPort)
		slog.InfoContext(ctx, "replication forwarder started", "target", replicationTarget, "pid", socatCmd.Process.Pid)
	}

	// Transform metricsd URL from localhost/127.0.0.1/0.0.0.0/[::] to gateway so exelet in VM can reach it
	if metricsdURL != "" {
		metricsdURL = strings.Replace(metricsdURL, "://localhost:", "://"+gateway+":", 1)
		metricsdURL = strings.Replace(metricsdURL, "://127.0.0.1:", "://"+gateway+":", 1)
		metricsdURL = strings.Replace(metricsdURL, "://0.0.0.0:", "://"+gateway+":", 1)
		metricsdURL = strings.Replace(metricsdURL, "://[::]:", "://"+gateway+":", 1)
		slog.InfoContext(ctx, "metricsd URL adjusted for VM access", "url", metricsdURL)
	}

	var exeletAddrs []string
	for i, host := range hosts {
		// Only enable replication on the primary host (first one)
		hostReplicationTarget := ""
		if replicationTarget != "" && i == 0 {
			hostReplicationTarget = replicationTarget
		}
		addr, err := startExeletOnHost(ctx, host, binPath, env.LogFormat, env.LogLevel, httpAddr, gateway, needsTunnel, hostReplicationTarget, metricsdURL)
		if err != nil {
			retErr = fmt.Errorf("failed to start exelet on %q: %w", host, err)
			return
		}
		exeletAddrs = append(exeletAddrs, addr)
	}

	slog.InfoContext(ctx, "exelets started", "addresses", exeletAddrs, "gateway", gateway)
	return strings.Join(exeletAddrs, ","), gateway, cleanup, nil
}

// getSSHHostPort queries the effective SSH hostname and port for a host using `ssh -G`.
// Returns the resolved hostname and port. The hostname must resolve to a local
// address (127.0.0.1 or localhost) for the socat forwarder to work correctly.
func getSSHHostPort(host string) (string, int, error) {
	// Use ssh -G to get the effective SSH config for the host
	// Host is like "lima-exe-ctr-tests.local", but SSH config uses "lima-exe-ctr-tests".
	sshHost := strings.TrimSuffix(host, ".local")

	out, err := exec.Command("ssh", "-G", sshHost).Output()
	if err != nil {
		return "", 0, fmt.Errorf("ssh -G failed: %w", err)
	}

	// Parse "hostname ..." from the output
	hostnameRe := regexp.MustCompile(`(?m)^hostname\s+(\S+)`)
	hostMatches := hostnameRe.FindSubmatch(out)
	if hostMatches == nil {
		return "", 0, fmt.Errorf("no hostname found in ssh config for %s", sshHost)
	}
	resolvedHost := string(hostMatches[1])

	// Parse "port NNN" from the output
	portRe := regexp.MustCompile(`(?m)^port\s+(\d+)`)
	portMatches := portRe.FindSubmatch(out)
	if portMatches == nil {
		return "", 0, fmt.Errorf("no port found in ssh config for %s", sshHost)
	}

	port, err := strconv.Atoi(string(portMatches[1]))
	if err != nil {
		return "", 0, fmt.Errorf("invalid port number: %w", err)
	}

	// Validate the hostname is local — socat forwards to this address, so a
	// non-local hostname would connect to the wrong host.
	if resolvedHost != "127.0.0.1" && resolvedHost != "localhost" && resolvedHost != "::1" {
		return "", 0, fmt.Errorf("SSH config for %s resolves to non-local hostname %q; replication forwarder requires a local-port SSH config (e.g., lima)", sshHost, resolvedHost)
	}

	return resolvedHost, port, nil
}

// getFreePort finds an available TCP port on the given bind address.
func getFreePort(bindAddr string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(bindAddr, "0"))
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// startReplicationForwarder starts a socat process on the macOS host that forwards
// connections from gateway:forwarderPort to 127.0.0.1:targetPort. This allows the
// exe-ctr VM to reach exe-ctr-tests via the gateway IP.
//
// Returns the socat process so the caller can manage its lifecycle.
// The process is also bound to ctx and will be killed when ctx is canceled.
func startReplicationForwarder(ctx context.Context, gateway string, forwarderPort int, targetHost string, targetPort int) (*exec.Cmd, error) {
	listenAddr := fmt.Sprintf("TCP-LISTEN:%d,bind=%s,fork,reuseaddr", forwarderPort, gateway)

	// Start socat to forward from gateway:forwarderPort to targetHost:targetPort
	cmd := exec.CommandContext(ctx, "socat",
		listenAddr,
		fmt.Sprintf("TCP:%s:%d", targetHost, targetPort))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start socat: %w", err)
	}

	// Reap the process when it exits to avoid zombies
	go cmd.Wait()

	// Wait for socat to be listening
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", gateway, forwarderPort), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return cmd, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Timeout - kill the process we started
	cmd.Process.Kill()
	return nil, fmt.Errorf("socat did not start listening within timeout")
}

// startExeletOnHost starts exelet on a single host. Returns the exelet address.
// If replicationTarget is non-empty, configures storage replication to that target.
func startExeletOnHost(ctx context.Context, host, binPath, logFormat, logLevel, httpAddr, gateway string, needsTunnel bool, replicationTarget, metricsdURL string) (string, error) {
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

	// Provision replication SSH key and known_hosts if needed (SSH targets only)
	var replicationKnownHosts string
	if replicationTarget != "" && strings.HasPrefix(replicationTarget, "ssh://") {
		if err := ensureReplicationSSHKey(ctx, host); err != nil {
			return "", fmt.Errorf("failed to provision replication SSH key: %w", err)
		}
		knownHostsPath, err := generateReplicationKnownHosts(ctx, host, replicationTarget)
		if err != nil {
			return "", fmt.Errorf("failed to generate replication known_hosts: %w", err)
		}
		replicationKnownHosts = knownHostsPath
		slog.InfoContext(ctx, "replication SSH provisioned", "known_hosts", knownHostsPath)
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
			if err := startExeletProcess(ctx, host, logFormat, logLevel, exedURL, replicationTarget, replicationKnownHosts, metricsdURL); err != nil {
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
	slog.InfoContext(ctx, "starting exeletd on remote host", "exed_url", exedURL, "replication_target", replicationTarget)
	if err := startExeletProcess(ctx, host, logFormat, logLevel, exedURL, replicationTarget, replicationKnownHosts, metricsdURL); err != nil {
		return "", err
	}
	return waitForExeletAddress(host)
}

const replicationSSHKeyPath = "/root/.ssh/replication_ed25519"

// ensureReplicationSSHKey checks that the replication SSH key exists on the
// exelet host. If it doesn't, generates a new ed25519 keypair and installs
// the public key on the replication target host (lima-exe-ctr-tests).
func ensureReplicationSSHKey(ctx context.Context, exeletHost string) error {
	// Check if key already exists
	out, err := sshExec(ctx, exeletHost, fmt.Sprintf("sudo test -f %s && echo exists", replicationSSHKeyPath))
	if err == nil && strings.Contains(out, "exists") {
		return nil
	}

	slog.InfoContext(ctx, "generating replication SSH key", "host", exeletHost)

	// Generate key on the exelet host
	genCmd := fmt.Sprintf(
		`sudo mkdir -p /root/.ssh && sudo ssh-keygen -t ed25519 -f %s -N "" -C "exelet-replication"`,
		replicationSSHKeyPath,
	)
	if out, err := sshExec(ctx, exeletHost, genCmd); err != nil {
		return fmt.Errorf("failed to generate SSH key: %w (output: %s)", err, out)
	}

	// Read the public key
	pubKey, err := sshExec(ctx, exeletHost, fmt.Sprintf("sudo cat %s.pub", replicationSSHKeyPath))
	if err != nil {
		return fmt.Errorf("failed to read public key: %w", err)
	}
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" {
		return fmt.Errorf("generated public key is empty")
	}

	// Install on the replication target host (lima-exe-ctr-tests)
	const targetHost = "lima-exe-ctr-tests.local"
	installCmd := fmt.Sprintf(
		`sudo mkdir -p /root/.ssh && echo '%s' | sudo tee -a /root/.ssh/authorized_keys > /dev/null && sudo chmod 600 /root/.ssh/authorized_keys`,
		pubKey,
	)
	if out, err := sshExec(ctx, targetHost, installCmd); err != nil {
		return fmt.Errorf("failed to install public key on %s: %w (output: %s)", targetHost, err, out)
	}

	slog.InfoContext(ctx, "replication SSH key provisioned", "host", exeletHost, "target", targetHost)
	return nil
}

// generateReplicationKnownHosts scans the SSH host key of the replication target
// and writes it to a known_hosts file on the exelet host.
// The replication target is like "ssh://root@192.168.64.1:12345/tank".
//
// We run ssh-keyscan locally (on the macOS host where socat binds) since:
// 1. ssh-keyscan may not be installed on the exelet VM
// 2. The macOS host can always reach its own gateway IP
// Then we write the result to the exelet VM via sshExec.
func generateReplicationKnownHosts(ctx context.Context, exeletHost, replicationTarget string) (string, error) {
	u, err := url.Parse(replicationTarget)
	if err != nil {
		return "", fmt.Errorf("failed to parse replication target: %w", err)
	}

	// Run ssh-keyscan locally on macOS to scan the socat forwarder's host key.
	// The forwarder binds on gateway:port on the macOS host.
	// Use -T 5 to timeout after 5 seconds if the target is unreachable.
	scanCtx, scanCancel := context.WithTimeout(ctx, 10*time.Second)
	defer scanCancel()
	scanCmd := exec.CommandContext(scanCtx, "ssh-keyscan", "-T", "5", "-p", u.Port(), u.Hostname())
	scanOut, err := scanCmd.Output()
	if err != nil {
		return "", fmt.Errorf("ssh-keyscan failed for %s:%s: %w", u.Hostname(), u.Port(), err)
	}
	if len(scanOut) == 0 {
		return "", fmt.Errorf("ssh-keyscan returned no keys for %s:%s", u.Hostname(), u.Port())
	}

	// Write the known_hosts content to the exelet VM
	knownHostsPath := "/root/.ssh/replication_known_hosts"
	escaped := strings.ReplaceAll(string(scanOut), "'", "'\\''")
	writeCmd := fmt.Sprintf("sudo mkdir -p /root/.ssh && printf '%%s' '%s' | sudo tee %s > /dev/null", escaped, knownHostsPath)
	out, err := sshExec(ctx, exeletHost, writeCmd)
	if err != nil {
		return "", fmt.Errorf("failed to write known_hosts on %s: %w (output: %s)", exeletHost, err, out)
	}

	return knownHostsPath, nil
}

// startExeletProcess starts the exelet process on the remote host via SSH.
// If replicationTarget is non-empty, adds storage replication flags.
// If metricsdURL is non-empty, adds metrics daemon flags.
func startExeletProcess(ctx context.Context, host, logFormat, logLevel, exedURL, replicationTarget, replicationKnownHosts, metricsdURL string) error {
	host = normalizeLimaSSHHost(host)
	baseCmd := fmt.Sprintf(`sudo LOG_FORMAT=%s LOG_LEVEL=%s /tmp/exeletd -D --stage local --data-dir /data/exelet --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" --network-manager-address nat:///data/exelet/network --runtime-address cloudhypervisor:///data/exelet/runtime --listen-address tcp://:9080 --http-addr :9081 --exed-url %s --instance-domain exe.cloud --enable-hugepages --reserved-cpus 0`,
		logFormat, logLevel, exedURL)

	if replicationTarget != "" {
		baseCmd += fmt.Sprintf(` --storage-replication-enabled --storage-replication-target=%s --storage-replication-interval=5m --storage-replication-retention=24`, replicationTarget)
		if strings.HasPrefix(replicationTarget, "ssh://") {
			baseCmd += fmt.Sprintf(` --storage-replication-ssh-key=%s`, replicationSSHKeyPath)
			if replicationKnownHosts != "" {
				baseCmd += fmt.Sprintf(` --storage-replication-known-hosts=%s`, replicationKnownHosts)
			}
		}
	}

	if metricsdURL != "" {
		// Use a short interval for local dev so metrics appear quickly
		baseCmd += fmt.Sprintf(` --metrics-daemon-url=%s --metrics-daemon-interval=10s`, metricsdURL)
	}

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		host,
		baseCmd)
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

// verifyOrFixMDNS checks that a .local hostname resolves via mDNS. If it
// doesn't, it restarts avahi-daemon inside the VM (via SSH) and retries.
// Avahi can pick a suffixed name (e.g. "lima-exe-ctr-2.local") when it sees a
// stale mDNS record held by macOS from a previous VM run. Restarting avahi
// re-probes and usually reclaims the correct name.
func verifyOrFixMDNS(ctx context.Context, host string) error {
	if resolveMDNS(host) {
		return nil
	}

	slog.WarnContext(ctx, "mDNS resolution failed, restarting avahi-daemon", "host", host)
	if out, err := sshExec(ctx, host, "sudo systemctl restart avahi-daemon"); err != nil {
		slog.WarnContext(ctx, "failed to restart avahi-daemon", "host", host, "error", err, "output", out)
	} else {
		// Give avahi a moment to re-probe the network.
		time.Sleep(2 * time.Second)
	}

	if resolveMDNS(host) {
		slog.InfoContext(ctx, "mDNS resolution recovered after avahi restart", "host", host)
		return nil
	}

	return fmt.Errorf("mDNS resolution failed for %s — the VM's avahi-daemon may have registered "+
		"with a suffixed name (e.g. %s-2) due to a stale macOS mDNS cache.\n"+
		"Try: sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder\n"+
		"Then: limactl shell %s -- sudo systemctl restart avahi-daemon",
		host, strings.TrimSuffix(host, ".local"),
		strings.TrimSuffix(strings.TrimPrefix(host, "lima-"), ".local"))
}

// resolveMDNS attempts a quick DNS lookup for host, returning true if it resolves.
func resolveMDNS(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}

// startMetricsdLocal builds and starts metricsd locally, returning the URL and a cleanup function.
func startMetricsdLocal(exeDBPath string) (url string, cleanup func(), retErr error) {
	slog.Info("building metricsd binary")

	binPath := filepath.Join(os.TempDir(), "metricsd")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/metricsd")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("go build metricsd failed: %w\n%s", err, out)
	}

	// Put metrics.duckdb in the same directory as exe.db
	dbPath := filepath.Join(filepath.Dir(exeDBPath), "metrics.duckdb")

	slog.Info("starting metricsd", "binary", binPath, "db", dbPath)

	ctx := context.Background()
	metricsdCmd := exec.CommandContext(ctx, binPath, "-addr", "0.0.0.0:0", "-db", dbPath, "-stage", "local")
	metricsdCmd.Env = os.Environ() // inherit env including LOG_FORMAT

	// Capture stdout/stderr to find the listen address
	stdoutPipe, err := metricsdCmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	metricsdCmd.Stderr = metricsdCmd.Stdout // combine stderr

	if err := metricsdCmd.Start(); err != nil {
		return "", nil, fmt.Errorf("failed to start metricsd: %w", err)
	}

	cleanup = func() {
		if metricsdCmd.Process != nil {
			metricsdCmd.Process.Kill()
		}
		// Don't remove dbPath - metrics.duckdb is persistent
		os.Remove(binPath)
	}
	defer func() {
		if retErr != nil && cleanup != nil {
			cleanup()
			cleanup = nil
		}
	}()

	// Read output to find the listen address
	// Strip ANSI escape codes from a string
	stripANSI := func(s string) string {
		ansiRE := regexp.MustCompile(`\x1b\[[0-9;]*m`)
		return ansiRE.ReplaceAllString(s, "")
	}

	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		// Match both JSON format ("addr":"...") and tint/text format (addr=...)
		addrJSONRE := regexp.MustCompile(`"addr":"([^"]+)"`)
		addrTextRE := regexp.MustCompile(`addr=([^\s]+)`)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Println(line) // echo to stdout
			if strings.Contains(line, "starting metricsd") {
				var addr string
				if matches := addrJSONRE.FindStringSubmatch(line); len(matches) > 1 {
					addr = matches[1]
				} else if matches := addrTextRE.FindStringSubmatch(line); len(matches) > 1 {
					addr = stripANSI(matches[1])
				}
				if addr != "" {
					select {
					case addrCh <- addr:
					default:
					}
				}
			}
		}
	}()

	// Wait for metricsd to report its address
	select {
	case addr := <-addrCh:
		return "http://" + addr, cleanup, nil
	case <-time.After(10 * time.Second):
		return "", nil, fmt.Errorf("timeout waiting for metricsd to start")
	}
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

// normalizeLimaSSHHost converts legacy ".local" Lima host aliases to the
// canonical Lima SSH alias so we always use Lima-managed ssh.config entries.
func normalizeLimaSSHHost(host string) string {
	if strings.HasPrefix(host, "lima-") && strings.HasSuffix(host, ".local") {
		return strings.TrimSuffix(host, ".local")
	}
	return host
}

// sshExec executes a command on remote host via SSH
func sshExec(ctx context.Context, host, command string) (string, error) {
	host = normalizeLimaSSHHost(host)
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
	host = normalizeLimaSSHHost(host)
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

// warnIfUIStale checks whether ui/src/ has files newer than the running binary,
// which means the embedded UI is out of date.
func warnIfUIStale() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exeInfo, err := os.Stat(exePath)
	if err != nil {
		return
	}
	exeTime := exeInfo.ModTime()

	stale := false
	_ = filepath.WalkDir("ui/src", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(exeTime) {
			stale = true
			return fs.SkipAll
		}
		return nil
	})
	if stale {
		slog.Warn("dashboard UI is out of date, rebuild: cd ui && pnpm build && go build -o /tmp/exed-local ./cmd/exed/")
	}
}
