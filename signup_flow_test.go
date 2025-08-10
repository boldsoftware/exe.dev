package exe

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSignupFlowAuthentication(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create mock container manager
	mockManager := NewMockContainerManager()

	// Create server in dev mode (no actual emails sent)
	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.containerManager = mockManager
	defer server.Stop()

	// Generate test SSH key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create SSH signer: %v", err)
	}

	fingerprint := server.getPublicKeyFingerprint(signer.PublicKey())

	// Test that user is initially not registered
	permissions, err := server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if permissions.Extensions["registered"] != "false" {
		t.Error("New user should have registered=false")
	}

	// Directly test the registration completion flow by manually creating user/team
	// This simulates what happens after the full signup flow completes
	email := "test@example.com"
	teamName := "testteam"

	// Create user and team in database (simulating completed registration)
	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Now test that authentication correctly detects the user as registered
	permissions2, err := server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if permissions2.Extensions["registered"] != "true" {
		t.Error("User should be registered after completing signup")
	}

	// Create mock channel and terminal
	var outputBuf bytes.Buffer
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Override the buffer for output capture
	term.buffer = &outputBuf

	mockChannel := &MockSSHChannel{
		term: term,
	}

	// Test that the user can be properly handled by handleSSHShell as a registered user
	// This simulates what happens when a registered user connects
	registered := permissions2.Extensions["registered"] == "true"
	
	// Instead of calling the full handleSSHShell (which would hang waiting for commands),
	// let's manually create a user session and test the create command
	if registered {
		// This is what handleSSHShell does for registered users
		user, err := server.getUserByFingerprint(fingerprint)
		if err != nil {
			t.Fatalf("Failed to get user: %v", err)
		}

		teams, err := server.getUserTeams(fingerprint)
		if err != nil || len(teams) == 0 {
			t.Fatalf("User should have team membership")
		}

		team := teams[0]
		server.createUserSession(mockChannel, fingerprint, user.Email, team.TeamName, team.IsAdmin)
		defer server.removeUserSession(mockChannel)

		t.Log("=== Testing create command with registered user session ===")

		// The session should be created, so the create command should work
		server.handleCreateCommand(mockChannel, []string{"testcontainer"})

		rawOutput := outputBuf.String()
		output := stripANSI(rawOutput)

		t.Log("=== Create command output ===")
		t.Log(output)

		// This should NOT contain "user not authenticated" error
		if strings.Contains(output, "user not authenticated") {
			t.Error("Create command should work after registration - user should be authenticated")
		}

		// Should contain success message
		if !strings.Contains(output, "created successfully") {
			t.Error("Create command should succeed after registration")
		}

		// Verify container was actually created
		machine, err := server.getMachineByName("testteam", "testcontainer")
		if err != nil {
			t.Errorf("Container should be created in database: %v", err)
		}

		if machine.Name != "testcontainer" {
			t.Errorf("Expected container name 'testcontainer', got %s", machine.Name)
		}

		if machine.TeamName != "testteam" {
			t.Errorf("Expected team name 'testteam', got %s", machine.TeamName)
		}
	} else {
		t.Error("User should be registered after completing signup")
	}
}

// Test the email verification flow specifically
func TestEmailVerificationInDevMode(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server in dev mode 
	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	fingerprint := "test-fingerprint"
	email := "test@example.com"

	// Create email verification
	verification := &EmailVerification{
		PublicKeyFingerprint: fingerprint,
		Email:               email,
		CompleteChan:        make(chan struct{}),
		CreatedAt:          time.Now(),
	}

	server.emailVerificationsMu.Lock()
	server.emailVerifications[fingerprint] = verification
	server.emailVerificationsMu.Unlock()

	// In dev mode, the sendVerificationEmail should just log and immediately verify
	err = server.sendVerificationEmail(email, fingerprint)
	if err != nil {
		t.Errorf("sendVerificationEmail should not fail in dev mode: %v", err)
	}

	// Wait for the async dev mode cleanup to complete (it has a 100ms delay)
	time.Sleep(150 * time.Millisecond)

	// Verification should be completed and cleaned up
	server.emailVerificationsMu.RLock()
	_, exists := server.emailVerifications[fingerprint]
	server.emailVerificationsMu.RUnlock()

	if exists {
		t.Error("Email verification should be cleaned up after completion in dev mode")
	}
}