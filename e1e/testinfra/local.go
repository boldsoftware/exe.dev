package testinfra

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// BootstrapLocalhost ensures all dependencies are available for running exelet locally.
// This includes:
// - ZFS tools (auto-installed if missing)
// - ZFS pool named "tank"
// - cloud-hypervisor binary (auto-downloaded if missing)
// - KVM device access
// - udevd running (auto-started if needed)
// - hugepages configured
func BootstrapLocalhost() error {
	fmt.Println("Bootstrapping localhost for e1e tests...")

	// Check for KVM
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("/dev/kvm not available: %w (KVM is required for local exelet)", err)
	}

	// Check for zfs/zpool, install if missing
	if _, err := exec.LookPath("zfs"); err != nil {
		fmt.Println("  Installing zfsutils-linux...")
		cmd := exec.Command("sudo", "apt-get", "update")
		cmd.Run() // ignore error
		cmd = exec.Command("sudo", "apt-get", "install", "-y", "zfsutils-linux")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to install zfsutils-linux: %w\n%s", err, out)
		}
		fmt.Println("  ✓ zfsutils-linux installed")
	}

	// Ensure cloud-hypervisor is available (auto-download if missing)
	if err := ensureCloudHypervisor(); err != nil {
		return err
	}
	fmt.Println("  ✓ Required binaries found")

	// Ensure ZFS pool "tank" exists
	if err := ensureZFSPool(); err != nil {
		return err
	}

	// Ensure udevd is running (required for zvol symlinks)
	if err := ensureUdevd(); err != nil {
		return err
	}

	// Configure hugepages - required for cloud-hypervisor
	if err := ensureHugepages(); err != nil {
		return fmt.Errorf("failed to configure hugepages: %w", err)
	}

	fmt.Println("  ✓ Localhost bootstrap complete")
	return nil
}

// ensureCloudHypervisor runs the install script to download cloud-hypervisor if not already installed.
func ensureCloudHypervisor() error {
	if _, err := exec.LookPath("cloud-hypervisor"); err == nil {
		return nil
	}

	// Find the install script relative to this source file
	_, thisFile, _, _ := runtime.Caller(0)
	scriptPath := filepath.Join(filepath.Dir(thisFile), "install-cloud-hypervisor.sh")

	cmd := exec.Command("bash", scriptPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install cloud-hypervisor: %w", err)
	}
	return nil
}

const (
	poolFile = "/tmp/tank.img"
	// poolSize is the size of the sparse file backing the ZFS pool.
	// VMs use copy-on-write so actual disk usage is much smaller than
	// the sum of VM disk sizes. 50GB is enough for parallel tests.
	poolSize = "50G"
)

func ensureZFSPool() error {
	// Check if tank pool already exists
	cmd := exec.Command("sudo", "zpool", "list", "tank")
	if err := cmd.Run(); err == nil {
		fmt.Println("  ✓ ZFS pool 'tank' exists")
		return nil
	}

	// Pool doesn't exist - create from sparse file
	fmt.Printf("  Creating ZFS pool 'tank' from %s sparse file...\n", poolSize)

	// Create sparse file if it doesn't exist
	if _, err := os.Stat(poolFile); os.IsNotExist(err) {
		cmd := exec.Command("sudo", "truncate", "-s", poolSize, poolFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create sparse file %s: %w\n%s", poolFile, err, out)
		}
	}

	// Create the pool
	cmd = exec.Command("sudo", "zpool", "create", "tank", poolFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create ZFS pool: %w\n%s", err, out)
	}

	// Register cleanup to destroy test datasets but preserve the pool and cached images.
	// This keeps tank/sha256:* volumes for faster subsequent test runs.
	AddCleanup(func() {
		fmt.Println("Cleaning up e1e test datasets (preserving image cache)...")
		// List and destroy only e1e-* datasets
		out, err := exec.Command("sudo", "zfs", "list", "-H", "-o", "name", "-r", "tank").Output()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				ds := strings.TrimSpace(line)
				if strings.Contains(ds, "/e1e-") {
					exec.Command("sudo", "zfs", "destroy", "-r", ds).Run()
				}
			}
		}
		// Clean up e1e snapshots on base images
		out, _ = exec.Command("sudo", "zfs", "list", "-H", "-t", "snapshot", "-o", "name").Output()
		for _, line := range strings.Split(string(out), "\n") {
			snap := strings.TrimSpace(line)
			if strings.Contains(snap, "@e1e-") {
				exec.Command("sudo", "zfs", "destroy", snap).Run()
			}
		}
	})

	fmt.Println("  ✓ ZFS pool 'tank' created")
	return nil
}

// ensureUdevd checks that udevd is running, which is required for ZFS zvol symlinks.
// If not running, attempts to start it.
func ensureUdevd() error {
	if err := exec.Command("pgrep", "-x", "udevd").Run(); err == nil {
		fmt.Println("  ✓ udevd is running")
		return nil
	}

	// Also check for systemd-udevd (the systemd variant)
	if err := exec.Command("pgrep", "-x", "systemd-udevd").Run(); err == nil {
		fmt.Println("  ✓ systemd-udevd is running")
		return nil
	}

	// Try to start systemd-udevd
	fmt.Println("  Starting systemd-udevd...")
	// Unmask in case it's masked
	exec.Command("sudo", "systemctl", "unmask", "systemd-udevd").Run()
	if out, err := exec.Command("sudo", "systemctl", "start", "systemd-udevd").CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start systemd-udevd: %w\n%s", err, out)
	}
	fmt.Println("  ✓ systemd-udevd started")
	return nil
}

// ensureHugepages configures hugepages for cloud-hypervisor.
func ensureHugepages() error {
	// Read current hugepages count
	data, err := os.ReadFile("/proc/sys/vm/nr_hugepages")
	if err != nil {
		return fmt.Errorf("failed to read hugepages: %w", err)
	}

	current := strings.TrimSpace(string(data))
	if current != "0" {
		fmt.Printf("  ✓ Hugepages already configured (%s)\n", current)
		return nil
	}

	// Calculate target: ~50% of RAM in 2MB pages
	// Read MemTotal from /proc/meminfo (in KB), divide by 4096 for 2MB pages at 50%
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return fmt.Errorf("failed to read meminfo: %w", err)
	}

	var memTotalKB int64
	for _, line := range strings.Split(string(meminfo), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				memTotalKB, _ = strconv.ParseInt(fields[1], 10, 64)
			}
			break
		}
	}

	if memTotalKB == 0 {
		return fmt.Errorf("could not determine total memory")
	}

	// Target: 50% of RAM in 2MB hugepages
	target := memTotalKB / 4096
	if target < 64 {
		target = 64 // Minimum 128MB
	}

	fmt.Printf("  Setting hugepages to %d...\n", target)
	cmd := exec.Command("sudo", "sh", "-c", fmt.Sprintf("echo %d > /proc/sys/vm/nr_hugepages", target))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set hugepages: %w\n%s", err, out)
	}

	fmt.Printf("  ✓ Hugepages configured (%d)\n", target)
	return nil
}
