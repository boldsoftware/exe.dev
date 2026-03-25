package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"exe.dev/stage"
)

// startExeletsLocal bootstraps the local environment and starts an exelet
// directly on this machine. This is used on Linux hosts (e.g. exe.dev VMs)
// where the exelet runs alongside exed.
func startExeletsLocal(env stage.Env, httpAddr, metricsdURL string) (addr string, cleanup func(), retErr error) {
	slog.Info("starting local exelet on Linux")

	// Use an existing exeletd binary if available, otherwise build one.
	// Building at runtime with `go run` + `make exelet` runs two Go compilers
	// concurrently and OOMs on small VMs with no swap.
	binPath, err := findOrBuildExeletBinary()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get exelet binary: %w", err)
	}

	// Bootstrap: ensure ZFS, hugepages, cloud-hypervisor, etc.
	if err := bootstrapLocalhost(); err != nil {
		return "", nil, fmt.Errorf("bootstrap failed: %w", err)
	}

	// Ensure /data/exelet exists
	if out, err := exec.Command("sudo", "mkdir", "-p", "/data/exelet").CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("failed to create /data/exelet: %w\n%s", err, out)
	}

	// Kill any existing exelet
	exec.Command("sudo", "pkill", "-9", "-f", "exeletd").Run()

	// Construct exed URL - exelet reaches exed via localhost
	_, httpPort, _ := strings.Cut(httpAddr, ":")
	exedURL := fmt.Sprintf("http://localhost:%s", httpPort)

	// Build the command
	// Force JSON log format for the exelet so we can parse its output to discover addresses.
	args := []string{
		"sudo",
		"LOG_FORMAT=json",
		fmt.Sprintf("LOG_LEVEL=%s", env.LogLevel),
		binPath,
		"-D",
		"--stage", "local",
		"--data-dir", "/data/exelet",
		"--storage-manager-address", "zfs:///data/exelet/storage?dataset=tank",
		"--network-manager-address", "nat:///data/exelet/network?network=100.64.0.0%2F24&disable_bandwidth=true",
		"--runtime-address", "cloudhypervisor:///data/exelet/runtime",
		"--listen-address", "tcp://:9080",
		"--http-addr", ":9081",
		"--exed-url", exedURL,
		"--instance-domain", env.BoxHost,
		"--reserved-cpus", "0",
	}

	if metricsdURL != "" {
		args = append(args, "--metrics-daemon-url", metricsdURL, "--metrics-daemon-interval", "10s")
	}

	slog.Info("starting exelet process", "exed_url", exedURL)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, fmt.Errorf("failed to start exelet: %w", err)
	}

	exited := make(chan struct{})
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.ErrorContext(ctx, "exelet process exited", "error", err)
		} else {
			slog.InfoContext(ctx, "exelet process exited normally")
		}
		close(exited)
	}()

	cleanup = func() {
		exec.Command("sudo", "pkill", "-9", "-f", "exeletd").Run()
		cancel()
		<-exited
	}
	defer func() {
		if retErr != nil && cleanup != nil {
			cleanup()
			cleanup = nil
		}
	}()

	// Parse JSON log output to find gRPC address
	type addrResult struct {
		addr string
		err  error
	}
	addrCh := make(chan addrResult, 1)

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Bytes()
			fmt.Println(string(line)) // echo to stdout

			if !json.Valid(line) {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}
			if entry["msg"] == "listening" {
				if addrVal, ok := entry["addr"].(string); ok {
					select {
					case addrCh <- addrResult{addr: addrVal}:
					default:
					}
				}
			}
		}
	}()

	// Wait for address with timeout
	select {
	case result := <-addrCh:
		if result.err != nil {
			return "", nil, result.err
		}
		// Parse the address to get the final form
		u, err := url.Parse(result.addr)
		if err != nil {
			return "", nil, fmt.Errorf("failed to parse exelet address %q: %w", result.addr, err)
		}
		finalAddr := fmt.Sprintf("tcp://localhost:%s", u.Port())
		slog.InfoContext(ctx, "exelet started", "address", finalAddr)
		return finalAddr, cleanup, nil
	case <-time.After(60 * time.Second):
		return "", nil, fmt.Errorf("timeout waiting for exelet to start")
	}
}

// findOrBuildExeletBinary returns a path to an exeletd binary.
// It checks for an existing binary in the working directory or /tmp first
// to avoid running `make exelet` (which invokes `go build`) at runtime —
// that OOMs small VMs when exed was also started via `go run`.
func findOrBuildExeletBinary() (string, error) {
	// Check working directory first (from a prior `make exelet`)
	if info, err := os.Stat("exeletd"); err == nil && !info.IsDir() {
		abs, err := filepath.Abs("exeletd")
		if err != nil {
			return "", err
		}
		slog.Info("using existing exelet binary", "path", abs)
		return abs, nil
	}
	// Check /tmp (from a prior buildExeletBinary call)
	if info, err := os.Stat(filepath.Join(os.TempDir(), "exeletd")); err == nil && !info.IsDir() {
		p := filepath.Join(os.TempDir(), "exeletd")
		slog.Info("using existing exelet binary", "path", p)
		return p, nil
	}
	slog.Info("building exelet binary")
	return buildExeletBinary()
}

const cloudHypervisorVersion = "48.0"

// bootstrapLocalhost ensures all dependencies are available for running exelet locally.
func bootstrapLocalhost() error {
	slog.Info("bootstrapping localhost for exelet")

	// Check for KVM
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("/dev/kvm not available: %w (KVM is required)", err)
	}

	// Check for zfs/zpool, install if missing
	if _, err := exec.LookPath("zfs"); err != nil {
		slog.Info("installing zfsutils-linux")
		exec.Command("sudo", "apt-get", "update").Run()
		if out, err := exec.Command("sudo", "apt-get", "install", "-y", "zfsutils-linux").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to install zfsutils-linux: %w\n%s", err, out)
		}
	}

	// Ensure cloud-hypervisor and virtiofsd are available, install if missing
	if _, err := exec.LookPath("cloud-hypervisor"); err != nil {
		if err := installCloudHypervisor(); err != nil {
			return fmt.Errorf("failed to install cloud-hypervisor: %w", err)
		}
	}
	if _, err := exec.LookPath("virtiofsd"); err != nil {
		if err := installVirtiofsd(); err != nil {
			return fmt.Errorf("failed to install virtiofsd: %w", err)
		}
	}

	// Ensure ZFS pool "tank" exists
	if err := exec.Command("sudo", "zpool", "list", "tank").Run(); err != nil {
		slog.Info("creating ZFS pool 'tank'")
		const poolFile = "/tmp/tank.img"
		const poolSize = "50G"
		if _, err := os.Stat(poolFile); os.IsNotExist(err) {
			if out, err := exec.Command("sudo", "truncate", "-s", poolSize, poolFile).CombinedOutput(); err != nil {
				return fmt.Errorf("failed to create sparse file: %w\n%s", err, out)
			}
		}
		if out, err := exec.Command("sudo", "zpool", "create", "tank", poolFile).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create ZFS pool: %w\n%s", err, out)
		}
	}

	// Ensure udevd is running
	udevRunning := exec.Command("pgrep", "-x", "udevd").Run() == nil ||
		exec.Command("pgrep", "-x", "systemd-udevd").Run() == nil
	if !udevRunning {
		slog.Info("starting systemd-udevd")
		exec.Command("sudo", "systemctl", "unmask", "systemd-udevd").Run()
		if out, err := exec.Command("sudo", "systemctl", "start", "systemd-udevd").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to start systemd-udevd: %w\n%s", err, out)
		}
	}

	slog.Info("bootstrap complete")
	return nil
}

// installCloudHypervisor downloads a static cloud-hypervisor binary from GitHub releases.
func installCloudHypervisor() error {
	arch, err := archSuffix()
	if err != nil {
		return err
	}
	// GitHub release binary naming: cloud-hypervisor-static (amd64) or cloud-hypervisor-static-aarch64 (arm64)
	binaryName := "cloud-hypervisor-static"
	if arch == "aarch64" {
		binaryName = "cloud-hypervisor-static-aarch64"
	}
	url := fmt.Sprintf("https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v%s/%s", cloudHypervisorVersion, binaryName)
	slog.Info("downloading cloud-hypervisor", "version", cloudHypervisorVersion, "url", url)
	return downloadBinary(url, "/usr/local/bin/cloud-hypervisor")
}

// installVirtiofsd installs virtiofsd via apt.
func installVirtiofsd() error {
	slog.Info("installing virtiofsd via apt")
	exec.Command("sudo", "apt-get", "update").Run()
	if out, err := exec.Command("sudo", "apt-get", "install", "-y", "virtiofsd").CombinedOutput(); err != nil {
		return fmt.Errorf("apt-get install virtiofsd failed: %w\n%s", err, out)
	}
	return nil
}

func archSuffix() (string, error) {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return "", fmt.Errorf("failed to determine architecture: %w", err)
	}
	arch := strings.TrimSpace(string(out))
	switch arch {
	case "x86_64", "aarch64":
		return arch, nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s", arch)
	}
}

// downloadBinary downloads a file from url and installs it to destPath with 0755 permissions.
func downloadBinary(url, destPath string) error {
	tmpFile := destPath + ".tmp"
	out, err := exec.Command("sudo", "curl", "-fSL", "--retry", "3", "-o", tmpFile, url).CombinedOutput()
	if err != nil {
		return fmt.Errorf("download failed: %w\n%s", err, out)
	}
	if out, err := exec.Command("sudo", "chmod", "0755", tmpFile).CombinedOutput(); err != nil {
		return fmt.Errorf("chmod failed: %w\n%s", err, out)
	}
	if out, err := exec.Command("sudo", "mv", tmpFile, destPath).CombinedOutput(); err != nil {
		return fmt.Errorf("mv failed: %w\n%s", err, out)
	}
	slog.Info("installed binary", "path", destPath)
	return nil
}
