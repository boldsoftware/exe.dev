package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"exe.dev"
	"exe.dev/ctrhosttest"
	"exe.dev/logging"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := flag.String("http", ":8080", "HTTP server address")
	sshAddr := flag.String("ssh", ":2223", "SSH server address")
	pluginAddr := flag.String("piper-plugin", ":2224", "Piper plugin gRPC server address")
	piperdPort := flag.Int("piperd-port", 2222, "sshpiper listening port")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt)")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	devMode := flag.String("dev", "", `development mode: "" (production), "local" (local containerd), or "test" (test mode)`)
	containerdAddresses := flag.String("containerd-addresses", "", "Comma-separated list of containerd addresses (e.g., 'ssh://host1,ssh://host2')")
	ghWhoAmIPath := flag.String("gh-whoami", "ghuser/whoami.sqlite3", "GitHub user key database path")
	fakeHTTPEmail := flag.String("fake-email-server", "", "HTTP email server URL for sending emails (e.g., http://localhost:8025)")
	openBrowser := flag.Bool("open", false, "Open web browser to HTTP server (dev mode only)")
	profilePath := flag.String("profile", "", "Enable CPU profiling for 30 seconds, saving to /tmp/exed-profile-<timestamp>.prof or specified path")
	flag.Parse()

	// Validate dev mode
	if *devMode != "" && *devMode != "local" && *devMode != "test" {
		return fmt.Errorf(`valid dev modes are "", "local", and "test", got: %q`, *devMode)
	}

	// Validate -open flag (dev mode only)
	if *openBrowser && *devMode == "" {
		return fmt.Errorf("-open flag is only available in dev mode")
	}

	// Setup structured logging
	logging.SetupLogger(*devMode)
	slog.Info("Starting exed server")

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

	// Parse containerd addresses
	var addresses []string

	if *containerdAddresses != "" {
		// Explicit containerd addresses specified via flag
		addresses = strings.Split(*containerdAddresses, ",")
		for i, h := range addresses {
			addresses[i] = strings.TrimSpace(h)
		}
	} else if *devMode == "local" || *devMode == "test" {
		ctrHost := ctrhosttest.Detect()
		if ctrHost == "" {
			return fmt.Errorf("dev mode: could not detect ctr-host")
		}
		addresses = []string{ctrHost}
	}

	if len(addresses) == 0 {
		slog.Warn("No containerd addresses specified, container functionality will be disabled",
			"suggestion", "Use -containerd-addresses flag, or set CTR_HOST env var")
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

	server, err := exe.NewServer(slog.Default(), *httpAddr, *httpsAddr, *sshAddr, *pluginAddr, *dbPath, *devMode, *fakeHTTPEmail, *piperdPort, *ghWhoAmIPath, addresses)
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
