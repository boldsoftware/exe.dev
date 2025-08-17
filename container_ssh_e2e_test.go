package exe

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
)

// TestSSHContainerE2E tests the complete SSH container creation and database storage flow
func TestSSHContainerE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create server with test database
	tmpDB, err := os.CreateTemp("", "test_ssh_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test user and team
	fingerprint := "test-ssh-fingerprint"
	email := "test-ssh@example.com"
	teamName := "ssh-test-team"

	if err := server.createTestUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	if err := server.createTestTeam(teamName, fingerprint); err != nil {
		t.Fatalf("Failed to create test team: %v", err)
	}

	// Create container with SSH
	ctx := context.Background()
	req := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     "ssh-container",
		TeamName: teamName,
		Image:    "ubuntu:22.04",
	}

	createdContainer, err := server.containerManager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	defer func() {
		// Cleanup
		server.containerManager.DeleteContainer(context.Background(), fingerprint, createdContainer.ID)
	}()

	// Verify SSH keys were generated
	if createdContainer.SSHServerIdentityKey == "" {
		t.Error("SSH server identity key not generated")
	}
	if createdContainer.SSHAuthorizedKeys == "" {
		t.Error("SSH authorized keys not generated")
	}
	if createdContainer.SSHCAPublicKey == "" {
		t.Error("SSH CA public key not generated")
	}
	if createdContainer.SSHHostCertificate == "" {
		t.Error("SSH host certificate not generated")
	}
	if createdContainer.SSHClientPrivateKey == "" {
		t.Error("SSH client private key not generated")
	}
	if createdContainer.SSHPort == 0 {
		t.Error("SSH port not set")
	}

	// Store in database using the SSH-enabled function
	sshKeys := &container.ContainerSSHKeys{
		ServerIdentityKey: createdContainer.SSHServerIdentityKey,
		AuthorizedKeys:    createdContainer.SSHAuthorizedKeys,
		CAPublicKey:       createdContainer.SSHCAPublicKey,
		HostCertificate:   createdContainer.SSHHostCertificate,
		ClientPrivateKey:  createdContainer.SSHClientPrivateKey,
		SSHPort:           createdContainer.SSHPort,
	}

	if err := server.createMachineWithSSH(fingerprint, teamName, "ssh-container", createdContainer.ID, "ubuntu:22.04", sshKeys, createdContainer.SSHPort); err != nil {
		t.Fatalf("Failed to store machine with SSH keys: %v", err)
	}

	// Verify SSH keys are stored in database
	var machineID int
	var storedServerKey, storedAuthKeys, storedCAKey, storedHostCert, storedClientKey string
	var storedSSHPort int

	err = server.db.QueryRow(`
		SELECT id, ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, 
		       ssh_host_certificate, ssh_client_private_key, ssh_port
		FROM machines 
		WHERE team_name = ? AND name = ? AND container_id = ?
	`, teamName, "ssh-container", createdContainer.ID).Scan(
		&machineID, &storedServerKey, &storedAuthKeys, &storedCAKey,
		&storedHostCert, &storedClientKey, &storedSSHPort)

	if err != nil {
		t.Fatalf("Failed to query stored machine: %v", err)
	}

	// Verify all SSH keys are stored correctly
	if storedServerKey != createdContainer.SSHServerIdentityKey {
		t.Error("SSH server identity key not stored correctly")
	}
	if storedAuthKeys != createdContainer.SSHAuthorizedKeys {
		t.Error("SSH authorized keys not stored correctly")
	}
	if storedCAKey != createdContainer.SSHCAPublicKey {
		t.Error("SSH CA public key not stored correctly")
	}
	if storedHostCert != createdContainer.SSHHostCertificate {
		t.Error("SSH host certificate not stored correctly")
	}
	if storedClientKey != createdContainer.SSHClientPrivateKey {
		t.Error("SSH client private key not stored correctly")
	}
	if storedSSHPort != createdContainer.SSHPort {
		t.Errorf("SSH port not stored correctly: expected %d, got %d", createdContainer.SSHPort, storedSSHPort)
	}

	// Test key formats
	if !strings.Contains(storedServerKey, "OPENSSH PRIVATE KEY") {
		t.Error("Server key not in OpenSSH format")
	}
	if !strings.Contains(storedClientKey, "OPENSSH PRIVATE KEY") {
		t.Error("Client key not in OpenSSH format")
	}
	if !strings.HasPrefix(storedAuthKeys, "ssh-ed25519 AAAAC3") {
		t.Error("Authorized keys not in public key format")
	}
	if !strings.HasPrefix(storedCAKey, "ssh-ed25519") {
		t.Error("CA key not in ed25519 format")
	}
	if !strings.HasPrefix(storedHostCert, "ssh-ed25519-cert-v01@openssh.com") {
		t.Error("Host certificate not in certificate format")
	}

	// Wait a bit for SSH daemon to potentially start (if container is using ubuntu with openssh)
	time.Sleep(5 * time.Second)

	// Test that we can execute commands in the container (basic functionality)
	var stdout strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = server.containerManager.ExecuteInContainer(ctx, fingerprint, createdContainer.ID,
		[]string{"echo", "SSH container test successful"},
		nil, &stdout, nil)
	if err != nil {
		t.Errorf("Failed to execute command in SSH-enabled container: %v", err)
	} else {
		output := strings.TrimSpace(stdout.String())
		if output != "SSH container test successful" {
			t.Errorf("Unexpected command output: %s", output)
		}
	}

	// Test SSH key parsing functions with stored keys
	_, err = container.ParsePrivateKey(storedServerKey)
	if err != nil {
		t.Errorf("Failed to parse stored server private key: %v", err)
	}

	_, err = container.ParsePrivateKey(storedClientKey)
	if err != nil {
		t.Errorf("Failed to parse stored client private key: %v", err)
	}

	_, err = container.CreateSSHSigner(storedServerKey)
	if err != nil {
		t.Errorf("Failed to create SSH signer from stored server key: %v", err)
	}

	_, err = container.CreateSSHSigner(storedClientKey)
	if err != nil {
		t.Errorf("Failed to create SSH signer from stored client key: %v", err)
	}

	t.Logf("SSH Container E2E test completed successfully:")
	t.Logf("- Machine ID: %d", machineID)
	t.Logf("- SSH Port: %d", storedSSHPort)
	t.Logf("- Container ID: %s", createdContainer.ID)
	t.Logf("- Keys generated and stored successfully")
}

// Helper functions for test setup
func (s *Server) createTestUser(fingerprint, email string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO users (public_key_fingerprint, email)
		VALUES (?, ?)
	`, fingerprint, email)
	return err
}

func (s *Server) createTestTeam(teamName, ownerFingerprint string) error {
	// Create team
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO teams (name, is_personal, owner_fingerprint)
		VALUES (?, ?, ?)
	`, teamName, true, ownerFingerprint)
	if err != nil {
		return err
	}

	// Add owner as admin member
	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO team_members (user_fingerprint, team_name, is_admin)
		VALUES (?, ?, ?)
	`, ownerFingerprint, teamName, true)
	return err
}
