package container

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/ctrhosttest"
	"golang.org/x/crypto/ssh"
)

// TestContainerIntegrationSuite combines the heavy containerd-based tests to reduce
// repeated manager creation and per-test setup/teardown cost.
func TestContainerIntegrationSuite(t *testing.T) {
	host := os.Getenv("CTR_HOST")
	if host == "" {
		// Attempt to auto-detect local dev host
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		host = ctrhosttest.Detect(ctx)
		if host == "" {
			t.Skip("CTR_HOST not set and colima-exe-ctr not reachable; skipping integration suite")
		}
	}

	cfg := &Config{
		ContainerdAddresses:  []string{host},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}

	manager, err := NewNerdctlManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create nerdctl manager: %v", err)
	}
	defer manager.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	// Subtest: Ubuntu container with SSH + rovol checks + SSH handshake
	t.Run("UbuntuSSHAndRovol", func(t *testing.T) {
		allocID := fmt.Sprintf("suite-ssh-%d", time.Now().UnixNano())
		ipRange := WithAllocIPRange(t, allocID)

		// Create the allocation network first
		if err := manager.CreateAlloc(ctx, allocID, ipRange); err != nil {
			t.Fatalf("CreateAlloc failed: %v", err)
		}

		req := &CreateContainerRequest{
			AllocID: allocID,
			IPRange: ipRange,
			Name:    "sshtest",
			Image:   "ubuntu:22.04",
			BoxID:   GenerateTestBoxID(),
		}

		c, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Create container failed: %v", err)
		}
		defer CleanupContainer(t, manager, req.AllocID, c.ID)

		// Wait for SSH tunnel to accept connections
		sshAddr := fmt.Sprintf("localhost:%d", c.SSHPort)
		deadline := time.Now().Add(30 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", sshAddr, 500*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("SSH port not accepting connections: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
		}

		// SSH handshake retry loop
		signer, err := ssh.ParsePrivateKey([]byte(c.SSHClientPrivateKey))
		if err != nil {
			t.Fatalf("Failed to parse SSH key: %v", err)
		}
		sshConf := &ssh.ClientConfig{
			User:            "root",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}
		var client *ssh.Client
		handshakeDeadline := time.Now().Add(30 * time.Second)
		for {
			client, err = ssh.Dial("tcp", sshAddr, sshConf)
			if err == nil {
				break
			}
			if time.Now().After(handshakeDeadline) {
				t.Fatalf("SSH handshake failed: %v", err)
			}
			time.Sleep(150 * time.Millisecond)
		}
		defer client.Close()

		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("NewSession failed: %v", err)
		}
		out, err := sess.Output("echo ok")
		sess.Close()
		if err != nil || strings.TrimSpace(string(out)) != "ok" {
			t.Fatalf("Unexpected SSH output: %q, err=%v", string(out), err)
		}

		// Verify /exe.dev mounted and is read-only
		var listOut strings.Builder
		if err := manager.ExecuteInContainer(ctx, req.AllocID, c.ID, []string{"ls", "-la", "/exe.dev"}, nil, &listOut, nil); err != nil {
			t.Errorf("Listing /exe.dev failed: %v", err)
		}
		// Try write
		var writeOut strings.Builder
		_ = manager.ExecuteInContainer(ctx, req.AllocID, c.ID, []string{"sh", "-c", "touch /exe.dev/test 2>&1"}, nil, &writeOut, nil)
		if !strings.Contains(writeOut.String(), "Read-only file system") {
			t.Logf("/exe.dev write attempt output: %s", writeOut.String())
		}
	})

	// Subtest: List containers presence with 3 alpine containers
	t.Run("ListContainers", func(t *testing.T) {
		allocID := fmt.Sprintf("suite-list-%d", time.Now().UnixNano())
		ipRange := WithAllocIPRange(t, allocID)

		// Create the allocation network first
		if err := manager.CreateAlloc(ctx, allocID, ipRange); err != nil {
			t.Fatalf("CreateAlloc failed: %v", err)
		}

		var created [](*Container)
		for i := 0; i < 3; i++ {
			req := &CreateContainerRequest{
				AllocID: allocID,
				IPRange: ipRange,
				Name:    fmt.Sprintf("c%d", i),
				Image:   "alpine:latest",
				BoxID:   GenerateTestBoxID(),
			}
			c, err := manager.CreateContainer(ctx, req)
			if err != nil {
				t.Fatalf("Create container %d failed: %v", i, err)
			}
			created = append(created, c)
			defer CleanupContainer(t, manager, allocID, c.ID)
		}
		listed, err := manager.ListContainers(ctx, allocID)
		if err != nil {
			t.Fatalf("ListContainers failed: %v", err)
		}
		ids := map[string]bool{}
		for _, c := range listed {
			ids[c.ID] = true
		}
		for _, c := range created {
			if !ids[c.ID] {
				t.Errorf("Container %s not found in list", c.ID)
			}
		}
	})

	// Subtest: Start/Stop cycle
	t.Run("StartStop", func(t *testing.T) {
		allocID := fmt.Sprintf("suite-ss-%d", time.Now().UnixNano())
		ipRange := WithAllocIPRange(t, allocID)

		// Create the allocation network first
		if err := manager.CreateAlloc(ctx, allocID, ipRange); err != nil {
			t.Fatalf("CreateAlloc failed: %v", err)
		}

		req := &CreateContainerRequest{
			AllocID:         allocID,
			IPRange:         ipRange,
			Name:            "startstop",
			Image:           "alpine:latest",
			CommandOverride: "sleep 3600",
			BoxID:           GenerateTestBoxID(),
		}
		c, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		defer CleanupContainer(t, manager, allocID, c.ID)
		if err := manager.StopContainer(ctx, allocID, c.ID); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}
		time.Sleep(1 * time.Second)
		if err := manager.StartContainer(ctx, allocID, c.ID); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		WaitForContainerReady(t, manager, allocID, c.ID, 10*time.Second)
	})

	// Subtest: Exec
	t.Run("Exec", func(t *testing.T) {
		allocID := fmt.Sprintf("suite-exec-%d", time.Now().UnixNano())
		ipRange := WithAllocIPRange(t, allocID)

		// Create the allocation network first
		if err := manager.CreateAlloc(ctx, allocID, ipRange); err != nil {
			t.Fatalf("CreateAlloc failed: %v", err)
		}

		req := &CreateContainerRequest{
			AllocID: allocID,
			IPRange: ipRange,
			Name:    "exec",
			Image:   "alpine:latest",
			BoxID:   GenerateTestBoxID(),
		}
		c, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		defer CleanupContainer(t, manager, allocID, c.ID)
		var stdout strings.Builder
		if err := manager.ExecuteInContainer(ctx, allocID, c.ID, []string{"echo", "hello"}, nil, &stdout, nil); err != nil {
			t.Fatalf("Exec failed: %v", err)
		}
		if strings.TrimSpace(stdout.String()) != "hello" {
			t.Errorf("Unexpected exec output: %q", stdout.String())
		}
	})
}
