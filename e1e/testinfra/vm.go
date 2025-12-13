package testinfra

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"exe.dev/ctrhosttest"
)

// ErrNoVM is returned if no VM is available and we don't know how to start one.
var ErrNoVM = errors.New("no ctr-host accessible")

// StartExeletVM starts or finds a VM to run an exelet.
// If the CTR_HOST environment variable is set, it is assumed to point to a VM.
// This returns the ssh address for the socket, in the form ssh://USER@ADDR.
// This returns [ErrNoVM] if no VM is available
// and we don't know how to start one.
func StartExeletVM() (string, error) {
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
		return startLinuxVM()

	default:
		return "", fmt.Errorf("don't know how to start a VM on %s", runtime.GOOS)
	}
}

// startLinuxVM starts a VM on Linux to run the exelet.
// It returns the ssh address for the host.
func startLinuxVM() (string, error) {
	userVal, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("starting VM failed: can't fetch current user name: %v", err)
	}
	name := userVal.Username + "-" + strconv.FormatInt(time.Now().Unix(), 10)

	outdir, err := os.MkdirTemp("", "ci-vm-start-")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %v", err)
	}
	AddCleanup(func() {
		os.RemoveAll(outdir)
	})

	// We are assumed to be running in a test.
	// Walk up the directory tree until we find "go.mod".
	srcdir := "."
	for {
		if _, err := os.Stat(filepath.Join(srcdir, "go.mod")); err == nil {
			break
		}
		srcdir = filepath.Join(srcdir, "..")
	}

	cmd := exec.Command(filepath.Join(srcdir, "ops/ci-vm-start.sh"))
	cmd.Env = append(cmd.Environ(),
		"NAME="+name,
		"OUTDIR="+outdir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
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
	for _, line := range strings.Split(string(envVars), "\n") {
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
