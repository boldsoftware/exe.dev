package exe

import (
	"bytes"
	"database/sql"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// mockChannel implements a minimal ssh.Channel for testing
type mockChannel struct {
	output bytes.Buffer
	stderr bytes.Buffer
}

func (m *mockChannel) Read(data []byte) (int, error) {
	return 0, io.EOF
}

func (m *mockChannel) Write(data []byte) (int, error) {
	return m.output.Write(data)
}

func (m *mockChannel) Close() error {
	return nil
}

func (m *mockChannel) CloseWrite() error {
	return nil
}

func (m *mockChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *mockChannel) Stderr() io.ReadWriter {
	return &m.stderr
}

// Ensure mockChannel implements ssh.Channel
var _ ssh.Channel = (*mockChannel)(nil)

func TestTeamCommands(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_team_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18084", "", ":12226", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test users and team
	testAdmin := "admin-fingerprint"
	testMember := "member-fingerprint"
	testAdminEmail := "admin@example.com"
	testMemberEmail := "member@example.com"
	testTeam := "test-team"

	// Create users
	err = server.createUser(testAdmin, testAdminEmail)
	if err != nil {
		t.Fatalf("Failed to create admin user: %v", err)
	}

	err = server.createUser(testMember, testMemberEmail)
	if err != nil {
		t.Fatalf("Failed to create member user: %v", err)
	}

	// Create team
	_, err = server.db.Exec("INSERT INTO teams (name) VALUES (?)", testTeam)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	// Add admin to team
	_, err = server.db.Exec(`
		INSERT INTO team_members (user_fingerprint, team_name, is_admin)
		VALUES (?, ?, 1)`,
		testAdmin, testTeam)
	if err != nil {
		t.Fatalf("Failed to add admin to team: %v", err)
	}

	// Add member to team
	_, err = server.db.Exec(`
		INSERT INTO team_members (user_fingerprint, team_name, is_admin)
		VALUES (?, ?, 0)`,
		testMember, testTeam)
	if err != nil {
		t.Fatalf("Failed to add member to team: %v", err)
	}

	t.Run("ListTeamMembers", func(t *testing.T) {
		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Create user session
		server.createUserSession(bufferedChannel, testAdmin, testAdminEmail, testTeam, "", true)
		defer server.removeUserSession(bufferedChannel)

		// Test team list command
		server.handleTeamList(bufferedChannel, testAdmin, testTeam)

		output := channel.output.String()

		// Check output contains team name
		if !strings.Contains(output, testTeam) {
			t.Errorf("Output should contain team name: %s", output)
		}

		// Check output contains both members
		if !strings.Contains(output, testAdminEmail) {
			t.Errorf("Output should contain admin email: %s", output)
		}

		if !strings.Contains(output, testMemberEmail) {
			t.Errorf("Output should contain member email: %s", output)
		}

		// Check admin is marked as admin
		if !strings.Contains(output, "Admin") {
			t.Errorf("Admin should be marked as Admin: %s", output)
		}

		// Check member count
		if !strings.Contains(output, "Total members: 2") {
			t.Errorf("Should show total members count: %s", output)
		}
	})

	t.Run("InviteNewMember", func(t *testing.T) {
		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Create user session for admin
		server.createUserSession(bufferedChannel, testAdmin, testAdminEmail, testTeam, "", true)
		defer server.removeUserSession(bufferedChannel)

		// Test invite command
		newEmail := "newuser@example.com"
		server.handleTeamInvite(bufferedChannel, testAdmin, testTeam, []string{newEmail})

		output := channel.output.String()

		// In dev mode, email might fail but invite should still be created
		// Check if we got either success or the specific error
		inviteSent := strings.Contains(output, "Invitation sent")
		emailError := strings.Contains(output, "Error sending invite email")

		if !inviteSent && !emailError {
			t.Errorf("Should either send invitation or show error: %s", output)
		}

		// Verify invite was stored in database
		var count int
		err := server.db.QueryRow(`
			SELECT COUNT(*) FROM invites 
			WHERE team_name = ? AND email = ?`,
			testTeam, newEmail).Scan(&count)

		if err != nil || count != 1 {
			t.Errorf("Invite should be stored in database")
		}
	})

	t.Run("NonAdminCannotInvite", func(t *testing.T) {
		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Create user session for non-admin member
		server.createUserSession(bufferedChannel, testMember, testMemberEmail, testTeam, "", false)
		defer server.removeUserSession(bufferedChannel)

		// Try to invite as non-admin
		server.handleTeamInvite(bufferedChannel, testMember, testTeam, []string{"another@example.com"})

		output := channel.output.String()

		// Should get error message
		if !strings.Contains(output, "Only team admins") {
			t.Errorf("Should show admin-only error: %s", output)
		}
	})

	t.Run("JoinTeamWithCode", func(t *testing.T) {
		// Create an invite
		inviteCode := "testcode"
		expires := time.Now().Add(1 * time.Hour)
		newUserFingerprint := "newuser-fingerprint"
		newUserEmail := "newuser@example.com"

		// Create the new user first
		err := server.createUser(newUserFingerprint, newUserEmail)
		if err != nil {
			t.Fatalf("Failed to create new user: %v", err)
		}

		// Create invite
		_, err = server.db.Exec(`
			INSERT INTO invites (code, team_name, created_by_fingerprint, email, expires_at, max_uses)
			VALUES (?, ?, ?, ?, ?, 1)`,
			inviteCode, testTeam, testAdmin, newUserEmail, expires)
		if err != nil {
			t.Fatalf("Failed to create invite: %v", err)
		}

		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Create session for new user (not yet in team)
		server.createUserSession(bufferedChannel, newUserFingerprint, newUserEmail, "", "", false)
		defer server.removeUserSession(bufferedChannel)

		// Join team with code
		server.handleTeamJoin(bufferedChannel, newUserFingerprint, []string{inviteCode})

		output := channel.output.String()

		// Check success message
		if !strings.Contains(output, "Successfully joined team") {
			t.Errorf("Should show success message: %s", output)
		}

		// Verify user was added to team
		var count int
		err = server.db.QueryRow(`
			SELECT COUNT(*) FROM team_members 
			WHERE user_fingerprint = ? AND team_name = ?`,
			newUserFingerprint, testTeam).Scan(&count)

		if err != nil || count != 1 {
			t.Errorf("User should be added to team")
		}
	})

	t.Run("InvalidInviteCode", func(t *testing.T) {
		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Try with invalid code
		server.handleTeamJoin(bufferedChannel, testMember, []string{"invalid"})

		output := channel.output.String()

		// Should show error
		if !strings.Contains(output, "Invalid invite code") {
			t.Errorf("Should show invalid code error: %s", output)
		}
	})

	t.Run("RemoveTeamMember", func(t *testing.T) {
		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Create user session for admin
		server.createUserSession(bufferedChannel, testAdmin, testAdminEmail, testTeam, "", true)
		defer server.removeUserSession(bufferedChannel)

		// Add a user to remove
		removeFingerprint := "remove-fingerprint"
		removeEmail := "remove@example.com"

		err := server.createUser(removeFingerprint, removeEmail)
		if err != nil {
			t.Fatalf("Failed to create user to remove: %v", err)
		}

		_, err = server.db.Exec(`
			INSERT INTO team_members (user_fingerprint, team_name, is_admin)
			VALUES (?, ?, 0)`,
			removeFingerprint, testTeam)
		if err != nil {
			t.Fatalf("Failed to add user to team: %v", err)
		}

		// Remove the member
		server.handleTeamRemove(bufferedChannel, testAdmin, testTeam, []string{removeEmail})

		output := channel.output.String()

		// Check success message
		if !strings.Contains(output, "Successfully removed") {
			t.Errorf("Should show success message: %s", output)
		}

		// Verify user was removed
		var count int
		err = server.db.QueryRow(`
			SELECT COUNT(*) FROM team_members 
			WHERE user_fingerprint = ? AND team_name = ?`,
			removeFingerprint, testTeam).Scan(&count)

		if err != nil || count != 0 {
			t.Errorf("User should be removed from team")
		}
	})

	t.Run("CannotRemoveSelf", func(t *testing.T) {
		channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

		// Create user session for admin
		server.createUserSession(bufferedChannel, testAdmin, testAdminEmail, testTeam, "", true)
		defer server.removeUserSession(bufferedChannel)

		// Try to remove self
		server.handleTeamRemove(bufferedChannel, testAdmin, testTeam, []string{testAdminEmail})

		output := channel.output.String()

		// Should show error
		if !strings.Contains(output, "cannot remove yourself") {
			t.Errorf("Should prevent self-removal: %s", output)
		}
	})
}

func TestInviteExpiration(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_invite_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18085", "", ":12227", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test data
	testAdmin := "admin-fingerprint"
	testTeam := "test-team"
	testUser := "user-fingerprint"
	testUserEmail := "user@example.com"

	// Create admin and team
	server.createUser(testAdmin, "admin@example.com")
	server.db.Exec("INSERT INTO teams (name) VALUES (?)", testTeam)
	server.db.Exec(`INSERT INTO team_members (user_fingerprint, team_name, is_admin) VALUES (?, ?, 1)`,
		testAdmin, testTeam)

	// Create user
	server.createUser(testUser, testUserEmail)

	// Create expired invite
	expiredCode := "expired"
	expires := time.Now().Add(-1 * time.Hour) // Already expired

	_, err = server.db.Exec(`
		INSERT INTO invites (code, team_name, created_by_fingerprint, email, expires_at, max_uses)
		VALUES (?, ?, ?, ?, ?, 1)`,
		expiredCode, testTeam, testAdmin, testUserEmail, expires)
	if err != nil {
		t.Fatalf("Failed to create expired invite: %v", err)
	}

	channel := &mockChannel{}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(channel)

	// Try to use expired code
	server.handleTeamJoin(bufferedChannel, testUser, []string{expiredCode})

	output := channel.output.String()

	// Should show expiration error
	if !strings.Contains(output, "expired") {
		t.Errorf("Should show expiration error: %s", output)
	}

	// Verify invite was deleted
	var count int
	err = server.db.QueryRow(`
		SELECT COUNT(*) FROM invites WHERE code = ?`,
		expiredCode).Scan(&count)

	if err != sql.ErrNoRows && count > 0 {
		t.Errorf("Expired invite should be deleted")
	}
}
