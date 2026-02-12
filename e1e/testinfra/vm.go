package testinfra

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// startLinuxVMMu ensures exclusivity within a single process.
// We use a file lock for exclusivity between processes.
var startLinuxVMMu sync.Mutex

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

	// Running ci-vm-start.sh is not concurrent-safe,
	// so use a lock. We can't lock the shell script,
	// as that will make the executable file busy.
	// Just lock this file.
	startLinuxVMMu.Lock()

	cleanup, err := flock(filepath.Join(srcdir, "e1e/testinfra/vm.go"))
	if err != nil {
		startLinuxVMMu.Unlock()
		return "", fmt.Errorf("error acquiring ci-vm-start lock: %v", err)
	}

	cmd := exec.Command(filepath.Join(srcdir, "ops/ci-vm-start.sh"))
	cmd.Env = append(cmd.Environ(),
		"NAME="+name,
		"OUTDIR="+outdir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()

	cleanup()
	startLinuxVMMu.Unlock()

	if err != nil {
		return "", fmt.Errorf("ops/ci-vm-start.sh failed: %v\n", err)
	}

	envFile := filepath.Join(outdir, name+".env")

	AddCleanup(func() {
		cmd := exec.Command(filepath.Join(srcdir, "ops/ci-vm-destroy.sh"), envFile)
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ci-vm-destroy.sh failed: %v\n%s", err, out)
		}
	})

	envVars, err := os.ReadFile(envFile)
	if err != nil {
		return "", fmt.Errorf("can't read ci-vm-start.sh environment variables: %v", err)
	}

	var vmUser, vmIP string
	for line := range strings.SplitSeq(string(envVars), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("invalid line in ci-vm-start.sh output: %q", line)
		}
		switch name {
		case "VM_USER":
			vmUser = val
		case "VM_IP":
			vmIP = val
		}
	}

	if vmUser == "" || vmIP == "" {
		return "", fmt.Errorf("VM_USER and/or VM_IP missing from %s created by ci-vm-start.sh:\n%s", envFile, envVars)
	}

	ctrHost := "ssh://" + vmUser + "@" + vmIP

	return ctrHost, nil
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
