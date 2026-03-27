package testinfra

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"exe.dev/ctrhosttest"
)

// ErrNoVM is returned if no VM is available and we don't know how to start one.
var ErrNoVM = errors.New("no ctr-host accessible")

// StartExeletVM starts or finds a VM to run an exelet.
// If the CTR_HOST environment variable is set, it is assumed to point to a VM.
//
// testRunID is a unique string for this invocation.
//
// This returns the ssh address for the socket, in the form ssh://USER@ADDR.
// This returns [ErrNoVM] if no VM is available
// and we don't know how to start one.
func StartExeletVM(testRunID string) (string, error) {
	ctrHost := strings.TrimSpace(os.Getenv("CTR_HOST"))
	if ctrHost != "" {
		return ctrHost, nil
	}

	switch runtime.GOOS {
	case "darwin":
		// On Darwin we don't start VMs,
		// but most people have started them already using lima.
		ctrHost := ctrhosttest.Detect()
		if ctrHost == "" {
			return "", ErrNoVM
		}
		return ctrHost, nil

	case "linux":
		return startLinuxVM(testRunID)

	default:
		return "", fmt.Errorf("don't know how to start a VM on %s", runtime.GOOS)
	}
}

// vmDriver returns the selected VM backend.
// Set VM_DRIVER=cloudhypervisor to use ci-vm-start.py (cloud-hypervisor direct)
// instead of the default ci-vm-start.sh (libvirt/QEMU).
func vmDriver() string {
	return os.Getenv("VM_DRIVER")
}

// vmStartCmd returns the start script/program for the current VM driver.
func vmStartCmd(srcdir string) *exec.Cmd {
	if vmDriver() == "cloudhypervisor" {
		return exec.Command("python3", filepath.Join(srcdir, "ops/ci-vm.py"), "create")
	}
	return exec.Command(filepath.Join(srcdir, "ops/ci-vm-start.sh"))
}

// vmDestroyCmd returns the destroy script/program for the current VM driver.
func vmDestroyCmd(srcdir, envFile string) *exec.Cmd {
	if vmDriver() == "cloudhypervisor" {
		return exec.Command("python3", filepath.Join(srcdir, "ops/ci-vm.py"), "destroy", envFile)
	}
	return exec.Command(filepath.Join(srcdir, "ops/ci-vm-destroy.sh"), envFile)
}

// startLinuxVM starts a VM on Linux to run the exelet.
// It returns the ssh address for the host.
func startLinuxVM(testRunID string) (string, error) {
	prefix := os.Getenv("E1E_VM_PREFIX")
	if prefix == "" {
		prefix = "ci-ubuntu"
	}
	name := prefix + "-" + testRunID + "-" + time.Now().Format("20060102150405")
	outdir, err := os.MkdirTemp("", "ci-vm-start-")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %v", err)
	}
	AddCleanup(func() {
		os.RemoveAll(outdir)
	})

	srcdir, err := exeRootDir()
	if err != nil {
		return "", err
	}

	cmd := vmStartCmd(srcdir)
	cmd.Env = append(cmd.Environ(),
		"NAME="+name,
		"OUTDIR="+outdir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("ci-vm-start failed (%s): %v\n", vmDriver(), err)
	}

	envFile := filepath.Join(outdir, name+".env")

	AddCleanup(func() {
		cmd := vmDestroyCmd(srcdir, envFile)
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ci-vm-destroy failed: %v\n%s", err, out)
		}
	})

	envVars, err := os.ReadFile(envFile)
	if err != nil {
		return "", fmt.Errorf("can't read VM envfile: %v", err)
	}

	var vmUser, vmIP string
	for line := range strings.SplitSeq(string(envVars), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("invalid line in VM envfile: %q", line)
		}
		switch name {
		case "VM_USER":
			vmUser = val
		case "VM_IP":
			vmIP = val
		}
	}

	if vmUser == "" || vmIP == "" {
		return "", fmt.Errorf("VM_USER and/or VM_IP missing from %s:\n%s", envFile, envVars)
	}

	return "ssh://" + vmUser + "@" + vmIP, nil
}

// exeRootDir returns the root of the source directory.
// We find it by walking up the directory tree until we find "go.mod".
func exeRootDir() (string, error) {
	srcdir := "."
	for range 32 {
		if _, err := os.Stat(filepath.Join(srcdir, "go.mod")); err == nil {
			return srcdir, nil
		}
		srcdir = filepath.Join(srcdir, "..")
	}
	return "", errors.New("could not find go.mod; directory too deep or in wrong directory")
}
