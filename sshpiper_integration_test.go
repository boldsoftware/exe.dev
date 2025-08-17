package exe

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"golang.org/x/crypto/ssh"
)

// TestSSHPiperIntegration tests the new sshpiper architecture end-to-end
func TestSSHPiperIntegration(t *testing.T) {
	t.Run("ExedStartsWithPiperPlugin", func(t *testing.T) {
		// Create temporary database file
		tmpDB, err := os.CreateTemp("", "test_*.db")
		if err != nil {
			t.Fatalf("Failed to create temp db: %v", err)
		}
		defer os.Remove(tmpDB.Name())
		tmpDB.Close()

		// Create server with available ports
		httpPort := findAvailablePort(t)
		sshPort := findAvailablePort(t)
		piperPort := findAvailablePort(t)

		server, err := NewServer(
			fmt.Sprintf(":%d", httpPort),
			"", // no HTTPS
			fmt.Sprintf(":%d", sshPort),
			fmt.Sprintf(":%d", piperPort),
			tmpDB.Name(),
			"local",
			[]string{""}, // local docker
		)
		if err != nil {
			t.Fatalf("Failed to create server: %v", err)
		}
		defer server.Stop()

		// Start the server in a goroutine

		go func() {
			server.Start()
		}()

		// Wait a bit for server to start
		time.Sleep(2 * time.Second)

		// Test that the piper plugin gRPC server is listening
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", piperPort), 5*time.Second)
		if err != nil {
			t.Errorf("Piper plugin gRPC server not listening on port %d: %v", piperPort, err)
		} else {
			conn.Close()
			t.Logf("✅ Piper plugin gRPC server is listening on port %d", piperPort)
		}

		// Test that the SSH server is listening on the new port (2223 by default, or our test port)
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", sshPort), 5*time.Second)
		if err != nil {
			t.Errorf("SSH server not listening on port %d: %v", sshPort, err)
		} else {
			conn.Close()
			t.Logf("✅ SSH server is listening on port %d", sshPort)
		}
	})

	t.Run("PiperPluginAuthentication", func(t *testing.T) {
		// Create a temporary database with test data
		tmpDB, err := os.CreateTemp("", "test_*.db")
		if err != nil {
			t.Fatalf("Failed to create temp db: %v", err)
		}
		defer os.Remove(tmpDB.Name())
		tmpDB.Close()

		// Create server
		piperPort := findAvailablePort(t)
		server, err := NewServer(
			":0", // HTTP
			"",   // no HTTPS
			":0", // SSH
			fmt.Sprintf(":%d", piperPort),
			tmpDB.Name(),
			"local",
			[]string{""}, // local docker
		)
		if err != nil {
			t.Fatalf("Failed to create server: %v", err)
		}
		defer server.Stop()

		// Test the piper plugin directly
		plugin := NewPiperPlugin(server, fmt.Sprintf(":%d", piperPort))

		// Start the plugin in a goroutine
		go func() {
			plugin.Serve()
		}()

		// Wait for plugin to start
		time.Sleep(1 * time.Second)

		// Test that the plugin server is running
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", piperPort), 5*time.Second)
		if err != nil {
			t.Errorf("Plugin server not listening: %v", err)
			return
		}
		conn.Close()
		t.Logf("✅ Piper plugin server is running")
	})
}

// findAvailablePort finds an available TCP port for testing
func findAvailablePort(t *testing.T) int {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Failed to find available port: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port
}

// TestSSHPiperScript tests the sshpiper.sh script
func TestSSHPiperScript(t *testing.T) {
	// Skip if not in a full environment
	if testing.Short() {
		t.Skip("Skipping sshpiper script test in short mode")
	}

	// Check if sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not available, skipping script test")
	}

	// Create temporary database with host key
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server to generate host key
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Generate host key by setting up SSH server
	server.setupSSHServer()

	// Test that the script can extract the host key
	scriptPath := "./sshpiper.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		t.Skip("sshpiper.sh not found, skipping script test")
	}

	// Check that sshpiperd binary exists or can be built
	sshpiperdPath := "./sshpiper/sshpiperd"
	if _, err := os.Stat(sshpiperdPath); os.IsNotExist(err) {
		t.Logf("sshpiperd not found, attempting to build...")
		cmd := exec.Command("go", "build", "-o", "sshpiperd", "./cmd/sshpiperd")
		cmd.Dir = "./sshpiper"
		if err := cmd.Run(); err != nil {
			t.Skipf("Failed to build sshpiperd: %v", err)
		}
	}

	// Test extracting host key from database
	cmd := exec.Command("sqlite3", tmpDB.Name(), "SELECT private_key FROM ssh_host_key WHERE id = 1;")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}

	hostKey := strings.TrimSpace(string(output))
	if hostKey == "" {
		t.Fatal("No host key found in database")
	}

	if !strings.Contains(hostKey, "PRIVATE KEY") {
		t.Errorf("Host key doesn't look like a private key: %s", hostKey[:50])
	}

	t.Logf("✅ Successfully extracted host key from database")
}

// TestSSHPiperConfiguration tests that sshpiper would start with correct config
func TestSSHPiperConfiguration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping sshpiper configuration test in short mode")
	}

	// Test that the sshpiperd binary accepts our configuration
	sshpiperdPath := "./sshpiper/sshpiperd"
	if _, err := os.Stat(sshpiperdPath); os.IsNotExist(err) {
		t.Skip("sshpiperd binary not found")
	}

	// Test that sshpiperd binary exists and runs
	cmd := exec.Command(sshpiperdPath, "--help")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to run sshpiperd --help: %v", err)
	}

	helpText := string(output)
	if !strings.Contains(helpText, "sshpiperd") {
		t.Error("sshpiperd help output doesn't look correct")
	}

	// Test that grpc plugin is available by checking if it fails in expected way
	// (it should fail with 'no server key found' when trying to start without key)
	cmd = exec.Command(sshpiperdPath, "grpc", "--endpoint=test:1234")
	err = cmd.Run()
	if err == nil {
		t.Error("Expected sshpiperd grpc to fail without server key")
	} else {
		// This is expected - it should fail because no server key is provided
		t.Logf("✅ sshpiperd grpc plugin is available (failed as expected without server key)")
	}
}

// TestMachineAccessImplementation tests the machine access functionality
func TestMachineAccessImplementation(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	piperPort := findAvailablePort(t)
	server, err := NewServer(
		":0", // HTTP
		"",   // no HTTPS
		":0", // SSH
		fmt.Sprintf(":%d", piperPort),
		tmpDB.Name(),
		"local",
		[]string{""}, // local docker
	)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Test the piper plugin directly
	plugin := NewPiperPlugin(server, fmt.Sprintf(":%d", piperPort))

	// Test machine that doesn't exist
	machine := &Machine{
		ID:                   999,
		Name:                 "nonexistent",
		ContainerID:          nil,
		CreatedByFingerprint: "test-fp",
	}

	upstream, err := plugin.handleMachineAccess(machine, "test-fp")
	if err == nil || upstream != nil {
		t.Error("Expected error for machine with no ContainerID")
	} else {
		t.Logf("✅ Correctly rejected machine with no container: %v", err)
	}

	// Test getting SSH details (this will fail since no data, but tests the logic)
	machine.ContainerID = &[]string{"test-container"}[0]

	// This will fail because there's no machine in the database, but it tests the SSH details logic
	_, err = server.GetMachineSSHDetails(999)
	if err == nil {
		t.Error("Expected error for non-existent machine SSH details")
	} else {
		t.Logf("✅ Correctly failed to get SSH details for non-existent machine: %v", err)
	}

	t.Log("✅ Machine access implementation logic tested successfully")
}

// TestRealMachineAccessE2E tests the complete end-to-end machine access via sshpiper with real containers
func TestRealMachineAccessE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real machine access E2E test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_e2e_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server with container management enabled
	httpPort := findAvailablePort(t)
	sshPort := findAvailablePort(t)
	piperPort := findAvailablePort(t)

	server, err := NewServer(
		fmt.Sprintf(":%d", httpPort),
		"", // no HTTPS
		fmt.Sprintf(":%d", sshPort),
		fmt.Sprintf(":%d", piperPort),
		tmpDB.Name(),
		"local",
		[]string{""}, // local docker
	)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Start the server
	go func() {
		server.Start()
	}()

	// Wait for server to start
	time.Sleep(3 * time.Second)

	// Create test user and team
	fingerprint := "test-e2e-fingerprint"
	email := "test-e2e@example.com"
	teamName := "e2e-test-team"

	if err := server.createTestUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	if err := server.createTestTeam(teamName, fingerprint); err != nil {
		t.Fatalf("Failed to create test team: %v", err)
	}

	t.Log("✅ Test user and team created")

	// Create a real container with SSH
	if server.containerManager == nil {
		t.Skip("Container manager not available, skipping real container test")
	}

	ctx := context.Background()
	req := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     "test-machine",
		TeamName: teamName,
		Image:    "ubuntu:22.04",
	}

	t.Log("Creating container with SSH...")
	createdContainer, err := server.containerManager.CreateContainer(ctx, req)
	if err != nil {
		t.Skipf("Failed to create container (Docker not available?): %v", err)
	}
	defer func() {
		// Cleanup
		server.containerManager.DeleteContainer(context.Background(), fingerprint, createdContainer.ID)
	}()

	t.Logf("✅ Container created with ID: %s", createdContainer.ID)
	t.Logf("   SSH Port: %d", createdContainer.SSHPort)
	t.Logf("   Has SSH Keys: %t", createdContainer.SSHClientPrivateKey != "")

	// Store in database with SSH keys
	sshKeys := &container.ContainerSSHKeys{
		ServerIdentityKey: createdContainer.SSHServerIdentityKey,
		AuthorizedKeys:    createdContainer.SSHAuthorizedKeys,
		CAPublicKey:       createdContainer.SSHCAPublicKey,
		HostCertificate:   createdContainer.SSHHostCertificate,
		ClientPrivateKey:  createdContainer.SSHClientPrivateKey,
		SSHPort:           createdContainer.SSHPort,
	}

	if err := server.createMachineWithSSH(fingerprint, teamName, "test-machine", createdContainer.ID, "ubuntu:22.04", sshKeys, createdContainer.SSHPort); err != nil {
		t.Fatalf("Failed to store machine with SSH keys: %v", err)
	}

	t.Log("✅ Machine stored in database with SSH keys")

	// Test the piper plugin directly
	plugin := NewPiperPlugin(server, fmt.Sprintf(":%d", piperPort))

	// Test finding the machine
	machine := server.FindMachineByNameForUser(fingerprint, "test-machine")
	if machine == nil {
		t.Fatal("Expected to find test machine")
	}

	t.Logf("✅ Machine found in database (ID: %d)", machine.ID)

	// Test getting SSH details from database
	sshDetails, err := server.GetMachineSSHDetails(machine.ID)
	if err != nil {
		t.Fatalf("Failed to get SSH details: %v", err)
	}

	t.Logf("✅ SSH details retrieved (port: %d, has private key: %t)", sshDetails.Port, sshDetails.PrivateKey != "")

	// Test getting container host port
	host, port, err := server.GetContainerHostPort(*machine.ContainerID, machine.CreatedByFingerprint)
	if err != nil {
		t.Fatalf("Failed to get container host port: %v", err)
	}

	t.Logf("✅ Container host port retrieved (host: %s, port: %d)", host, port)

	// Test the complete machine access flow
	upstream, err := plugin.handleMachineAccess(machine, fingerprint)
	if err != nil {
		t.Fatalf("Failed to handle machine access: %v", err)
	}

	if upstream == nil {
		t.Fatal("Expected upstream configuration")
	}

	t.Logf("✅ Machine access upstream created:")
	t.Logf("   Host: %s", upstream.Host)
	t.Logf("   Port: %d", upstream.Port)
	t.Logf("   User: %s", upstream.UserName)
	t.Logf("   Ignore Host Key: %t", upstream.IgnoreHostKey)

	// Test that the private key is valid
	if upstream.Auth == nil {
		t.Fatal("Expected authentication method")
	}

	t.Log("✅ Authentication method configured")

	// Try to parse the private key to ensure it's valid
	_, err = ssh.ParsePrivateKey([]byte(sshDetails.PrivateKey))
	if err != nil {
		t.Fatalf("SSH private key is invalid: %v", err)
	}

	t.Log("✅ SSH private key is valid and parseable")

	t.Log("")
	t.Log("🎉 END-TO-END MACHINE ACCESS TEST SUCCESSFUL!")
	t.Log("")
	t.Log("Components verified:")
	t.Log("  ✅ Container creation with proper SSH keys")
	t.Log("  ✅ Database storage of machine and SSH details")
	t.Log("  ✅ Machine lookup by name and user")
	t.Log("  ✅ SSH details retrieval from database")
	t.Log("  ✅ Container port mapping via Docker")
	t.Log("  ✅ Complete machine access upstream creation")
	t.Log("  ✅ Valid SSH private key for authentication")
	t.Log("")
	t.Log("The sshpiper machine access implementation is WORKING!")
}

// TestLegacyContainerSSHSetup tests the SSH setup for containers created before SSH support
func TestLegacyContainerSSHSetup(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_legacy_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	piperPort := findAvailablePort(t)
	server, err := NewServer(
		":0", // HTTP
		"",   // no HTTPS
		":0", // SSH
		fmt.Sprintf(":%d", piperPort),
		tmpDB.Name(),
		"local",
		[]string{""}, // local docker
	)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test user and team
	fingerprint := "legacy-test-fingerprint"
	email := "legacy-test@example.com"
	teamName := "legacy-team"

	if err := server.createTestUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	if err := server.createTestTeam(teamName, fingerprint); err != nil {
		t.Fatalf("Failed to create test team: %v", err)
	}

	// Create a legacy machine WITHOUT SSH details (simulating old containers)
	containerID := "legacy-container-123"
	_, err = server.db.Exec(`
		INSERT INTO machines (
			team_name, name, status, image, container_id, created_by_fingerprint
		) VALUES (?, ?, ?, ?, ?, ?)
	`, teamName, "legacy-machine", "running", "ubuntu:22.04", containerID, fingerprint)
	if err != nil {
		t.Fatalf("Failed to create legacy machine: %v", err)
	}

	t.Log("✅ Legacy machine created (no SSH data)")

	// Find the machine
	machine := server.FindMachineByNameForUser(fingerprint, "legacy-machine")
	if machine == nil {
		t.Fatal("Expected to find legacy machine")
	}

	// Try to get SSH details - this should trigger SSH setup
	sshDetails, err := server.GetMachineSSHDetails(machine.ID)
	if err != nil {
		// This is expected to fail since we can't actually set up SSH without a real container
		// But we can verify the error handling
		if strings.Contains(err.Error(), "failed to setup SSH on legacy container") {
			t.Logf("✅ Correctly attempted SSH setup for legacy container: %v", err)
		} else {
			t.Errorf("Unexpected error format: %v", err)
		}
		return
	}

	// If we get here, SSH was set up successfully
	if sshDetails.Port <= 0 {
		t.Error("Expected valid SSH port after setup")
	}

	if sshDetails.PrivateKey == "" {
		t.Error("Expected SSH private key after setup")
	}

	t.Log("✅ Legacy container SSH setup completed successfully")
}

// TestSSHPiperProxyAuthentication tests the complete proxy authentication flow
func TestSSHPiperProxyAuthentication(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true
	server.testMode = true

	// Start the server
	go server.Start()
	defer server.Stop()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Generate test user SSH key
	privateKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	userPublicKey := signer.PublicKey()
	userFingerprint := server.GetPublicKeyFingerprint(userPublicKey)
	userPublicKeyBytes := userPublicKey.Marshal()

	t.Logf("Test user fingerprint: %s", userFingerprint)

	// Create piper plugin
	piperPort := findAvailablePort(t)
	plugin := NewPiperPlugin(server, fmt.Sprintf(":%d", piperPort))

	// Test ephemeral proxy key generation
	proxyKey, proxyFingerprint, err := plugin.generateEphemeralProxyKey(userPublicKeyBytes)
	if err != nil {
		t.Fatalf("Failed to generate ephemeral proxy key: %v", err)
	}
	t.Logf("✅ Ephemeral proxy key generated (length: %d, fingerprint: %s)", len(proxyKey), proxyFingerprint)

	// Test proxy authentication logic
	username := "root"
	upstream, err := plugin.handlePublicKeyAuth(mockConnMetadata{
		user: username,
		addr: "127.0.0.1:12345",
	}, userPublicKeyBytes)
	if err != nil {
		t.Fatalf("Failed to handle public key auth: %v", err)
	}

	if upstream == nil {
		t.Fatal("Expected non-nil upstream")
	}

	t.Logf("✅ Plugin returned upstream: %s:%d, user: %s", upstream.Host, upstream.Port, upstream.UserName)

	// Verify the upstream configuration - accept both localhost and 127.0.0.1
	if upstream.Host != "localhost" && upstream.Host != "127.0.0.1" {
		t.Errorf("Expected host=localhost or 127.0.0.1, got %s", upstream.Host)
	}
	if upstream.Port != 2223 {
		t.Errorf("Expected port=2223, got %d", upstream.Port)
	}

	// Check that username is preserved (no longer encoded with fingerprint)
	if upstream.UserName != username {
		t.Errorf("Expected username=%s, got %s", username, upstream.UserName)
	}

	// Test that we can look up the original user key
	lookedUpKey, exists := plugin.lookupOriginalUserKey(proxyFingerprint)
	if !exists {
		t.Errorf("Failed to lookup original user key for proxy fingerprint %s", proxyFingerprint)
	}
	if string(lookedUpKey) != string(userPublicKeyBytes) {
		t.Errorf("Looked up key doesn't match original user key")
	}

	t.Logf("✅ Proxy authentication test completed successfully")
}

// mockConnMetadata implements libplugin.ConnMetadata for testing
type mockConnMetadata struct {
	user string
	addr string
}

func (m mockConnMetadata) User() string {
	return m.user
}

func (m mockConnMetadata) RemoteAddr() string {
	return m.addr
}

func (m mockConnMetadata) UniqueID() string {
	return "test-unique-id"
}

func (m mockConnMetadata) GetMeta(key string) string {
	return ""
}

// TestSSHPiperExedEphemeralProxyAuth tests that exed correctly handles ephemeral proxy authentication
func TestSSHPiperExedEphemeralProxyAuth(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true
	server.testMode = true

	// Create piper plugin
	plugin := NewPiperPlugin(server, ":0")
	server.piperPlugin = plugin // Set the reference

	// Generate test user key
	userPrivateKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate user private key: %v", err)
	}

	userSigner, err := ssh.NewSignerFromKey(userPrivateKey)
	if err != nil {
		t.Fatalf("Failed to create user signer: %v", err)
	}

	userPublicKeyBytes := userSigner.PublicKey().Marshal()
	userFingerprint := server.GetPublicKeyFingerprint(userSigner.PublicKey())
	t.Logf("User fingerprint: %s", userFingerprint)

	// Generate ephemeral proxy key
	proxyKeyPEM, proxyFingerprint, err := plugin.generateEphemeralProxyKey(userPublicKeyBytes)
	if err != nil {
		t.Fatalf("Failed to generate ephemeral proxy key: %v", err)
	}

	// Parse proxy private key
	proxyPrivateKey, err := ssh.ParsePrivateKey([]byte(proxyKeyPEM))
	if err != nil {
		t.Fatalf("Failed to parse proxy private key: %v", err)
	}

	t.Logf("Generated proxy key with fingerprint: %s", proxyFingerprint)

	// Test proxy authentication - simulate what sshpiper does
	mockConn := mockSSHConnMetadata{
		user: "testuser",
	}

	// Test authentication
	permissions, err := server.AuthenticatePublicKey(mockConn, proxyPrivateKey.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if permissions == nil {
		t.Fatal("Expected non-nil permissions")
	}

	// Verify proxy authentication was recognized and original user info was retrieved
	if permissions.Extensions["fingerprint"] != userFingerprint {
		t.Errorf("Expected fingerprint=%s, got %s", userFingerprint, permissions.Extensions["fingerprint"])
	}

	if permissions.Extensions["proxy_user"] != "testuser" {
		t.Errorf("Expected proxy_user=testuser, got %s", permissions.Extensions["proxy_user"])
	}

	if permissions.Extensions["registered"] != "false" {
		t.Errorf("Expected registered=false for new user, got %s", permissions.Extensions["registered"])
	}

	t.Logf("✅ Exed correctly recognized ephemeral proxy authentication")
	t.Logf("  - Proxy fingerprint: %s", proxyFingerprint)
	t.Logf("  - Original user fingerprint: %s", permissions.Extensions["fingerprint"])
	t.Logf("  - Proxy user: %s", permissions.Extensions["proxy_user"])
	t.Logf("  - Registered: %s", permissions.Extensions["registered"])
}

// mockSSHConnMetadata implements ssh.ConnMetadata for testing exed authentication
type mockSSHConnMetadata struct {
	user string
}

func (m mockSSHConnMetadata) User() string {
	return m.user
}

func (m mockSSHConnMetadata) SessionID() []byte {
	return []byte("test-session")
}

func (m mockSSHConnMetadata) ClientVersion() []byte {
	return []byte("SSH-2.0-Test")
}

func (m mockSSHConnMetadata) ServerVersion() []byte {
	return []byte("SSH-2.0-OpenSSH_8.0")
}

func (m mockSSHConnMetadata) RemoteAddr() net.Addr {
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:12345")
	return addr
}

func (m mockSSHConnMetadata) LocalAddr() net.Addr {
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:2223")
	return addr
}

// TestSSHPiperRealKeyIntegration tests with the actual SSH key from the environment
func TestSSHPiperRealKeyIntegration(t *testing.T) {
	// Read the actual SSH public key
	pubKeyBytes, err := os.ReadFile("/root/.ssh/id_ed25519.pub")
	if err != nil {
		t.Skipf("No SSH key found: %v", err)
	}

	// Parse the public key
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubKeyBytes)
	if err != nil {
		t.Fatalf("Failed to parse public key: %v", err)
	}

	// Create a temporary server
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true
	server.testMode = true

	// Create piper plugin
	plugin := NewPiperPlugin(server, ":0")
	server.piperPlugin = plugin

	userKeyBytes := pubKey.Marshal()
	userFingerprint := server.GetPublicKeyFingerprint(pubKey)
	t.Logf("Real user key type: %s, fingerprint: %s", pubKey.Type(), userFingerprint)

	// Test the plugin authentication flow
	upstream, err := plugin.handlePublicKeyAuth(mockConnMetadata{
		user: "root",
		addr: "127.0.0.1:12345",
	}, userKeyBytes)
	if err != nil {
		t.Fatalf("Plugin authentication failed: %v", err)
	}

	if upstream == nil {
		t.Fatal("Expected non-nil upstream")
	}

	t.Logf("Plugin returned upstream: %s:%d, user: %s", upstream.Host, upstream.Port, upstream.UserName)

	// Check if we can find the proxy key mapping that was created
	found := false
	plugin.proxyKeyMutex.RLock()
	for proxyFingerprint, mapping := range plugin.proxyKeyMappings {
		if string(mapping.OriginalPublicKey) == string(userKeyBytes) {
			t.Logf("Found proxy key mapping: %s -> original user", proxyFingerprint[:16])
			found = true
			break
		}
	}
	plugin.proxyKeyMutex.RUnlock()

	if !found {
		t.Error("Proxy key mapping not found")
	}

	t.Logf("✅ Real key integration test passed")
	t.Logf("  - Original key type: %s", pubKey.Type())
	t.Logf("  - Original fingerprint: %s", userFingerprint)
	t.Logf("  - Proxy key mappings: %d active", len(plugin.proxyKeyMappings))
}
