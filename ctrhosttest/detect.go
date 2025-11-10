package ctrhosttest

import (
	"bufio"
	"context"
	"flag"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultHost         = "lima-exe-ctr"
	defaultHostForTests = "lima-exe-ctr-tests"
)

// Detect returns a usable CTR_HOST value for tests and local dev.
// The CTR_HOST env var wins, but otherwise it will try lima-exe-ctr{,-tests}
// (depening if its being called from a unit test or not).
func Detect() string {
	if v := strings.TrimSpace(os.Getenv("CTR_HOST")); v != "" {
		return v
	}
	host := defaultHost
	if flag.Lookup("test.v") != nil {
		host = defaultHostForTests
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		host, "true",
	)
	if err := cmd.Run(); err == nil {
		return "ssh://" + host
	}
	return ""
}

func ResolveDefaultGateway() string {
	if v := strings.TrimSpace(os.Getenv("CTR_HOST")); v != "" {
		return v
	}
	host := defaultHost
	if flag.Lookup("test.v") != nil {
		host = defaultHostForTests
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		host, "sh", "-c", "getent ahostsv4 _gateway 2>/dev/null | awk '{print $1; exit}'",
	)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ResolveHostFromSSHConfig returns the HostName from SSH config for a given alias.
// It shells out to `ssh -G <alias>` and parses the resulting config to find the
// canonical host IP/name. Returns "" on failure.
func ResolveHostFromSSHConfig(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ""
	}
	// Ask OpenSSH to print the effective configuration for the alias.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-G", alias).Output()
	if err != nil {
		return ""
	}
	var hostName string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(strings.ToLower(line), "hostname ") {
			hostName = strings.TrimSpace(line[len("hostname "):])
			break
		}
	}
	if hostName == "" {
		return ""
	}
	// Basic sanity: ensure it resolves locally
	if _, err := net.LookupHost(hostName); err != nil {
		// Still return it; caller may attempt to dial regardless
		// but empty return would force a fallback to the alias, which we want to avoid.
	}
	return hostName
}

// DetectDialAddr returns a tcp://<ip> style address for the local Lima VM used in dev/tests.
// It keeps SSH aliasing (lima-exe-ctr) for SSH operations, but provides a direct IP for TCP dials.
// Returns empty string on failure.
func DetectDialAddr() string {
	host := defaultHost
	if flag.Lookup("test.v") != nil {
		host = defaultHostForTests
	}
	// Only return a dial addr if the SSH alias is reachable (quick probe)
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		host, "true",
	)
	if err := probe.Run(); err != nil {
		return ""
	}
	if ip := ResolveHostFromSSHConfig(host); ip != "" {
		return "tcp://" + ip
	}
	return ""
}
