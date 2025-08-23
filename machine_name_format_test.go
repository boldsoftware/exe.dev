package exe

import (
	"os"
	"testing"
)

// TestMachineNameFormatParsing tests the new machine.team format instead of team/machine
func TestMachineNameFormatParsing(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
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

	// Create test user and teams
	userID := "test-user-123"
	team1 := "team1"
	team2 := "team2"
	machineName := "testmachine"

	// Create user
	_, err = server.db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Create teams
	for _, team := range []string{team1, team2} {
		_, err = server.db.Exec(`INSERT INTO teams (team_name, created_at) VALUES (?, datetime('now'))`, team)
		if err != nil {
			t.Fatal(err)
		}

		// Add user to both teams
		_, err = server.db.Exec(`INSERT INTO team_members (user_id, team_name, is_admin) VALUES (?, ?, 1)`, userID, team)
		if err != nil {
			t.Fatal(err)
		}

		// Create a machine in each team
		_, err = server.db.Exec(`
			INSERT INTO machines (team_name, name, status, image, created_by_user_id, created_at, updated_at)
			VALUES (?, ?, 'stopped', 'ubuntu', ?, datetime('now'), datetime('now'))
		`, team, machineName, userID)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Set team1 as default team
	_, err = server.db.Exec(`INSERT INTO ssh_keys (user_id, public_key, verified, default_team) VALUES (?, ?, 1, ?)`, userID, "dummy-key", team1)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		machineName   string
		expectedTeam  string
		expectedFound bool
		description   string
	}{
		{
			name:          "machine only - uses default team",
			machineName:   "testmachine",
			expectedTeam:  team1, // default team
			expectedFound: true,
			description:   "When no team is specified, should use default team",
		},
		{
			name:          "machine.team format - team1",
			machineName:   "testmachine.team1",
			expectedTeam:  team1,
			expectedFound: true,
			description:   "Should parse machine.team format correctly",
		},
		{
			name:          "machine.team format - team2",
			machineName:   "testmachine.team2",
			expectedTeam:  team2,
			expectedFound: true,
			description:   "Should parse machine.team format correctly for different team",
		},
		{
			name:          "nonexistent machine",
			machineName:   "nonexistent",
			expectedFound: false,
			description:   "Should return nil for nonexistent machine",
		},
		{
			name:          "machine in nonexistent team",
			machineName:   "testmachine.nonexistent",
			expectedFound: false,
			description:   "Should return nil for machine in nonexistent team",
		},
		{
			name:          "old team/machine format should not work",
			machineName:   "team1/testmachine",
			expectedFound: false,
			description:   "Old team/machine format should not be recognized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := server.FindMachineByNameForUser(userID, tt.machineName)

			if tt.expectedFound {
				if machine == nil {
					t.Errorf("Expected to find machine, but got nil. %s", tt.description)
					return
				}
				if machine.TeamName != tt.expectedTeam {
					t.Errorf("Expected team %s, got %s. %s", tt.expectedTeam, machine.TeamName, tt.description)
				}
				if machine.Name != "testmachine" {
					t.Errorf("Expected machine name 'testmachine', got %s", machine.Name)
				}
			} else {
				if machine != nil {
					t.Errorf("Expected nil, but found machine: %+v. %s", machine, tt.description)
				}
			}
		})
	}
}

// TestFormatSSHConnectionInfo tests the new SSH connection format
func TestFormatSSHConnectionInfo(t *testing.T) {
	server := &Server{}

	tests := []struct {
		name        string
		devMode     string
		teamName    string
		machineName string
		expected    string
	}{
		{
			name:        "production mode",
			devMode:     "",
			teamName:    "myteam",
			machineName: "mymachine",
			expected:    "ssh mymachine.myteam@exe.dev",
		},
		{
			name:        "local dev mode with port 22",
			devMode:     "local",
			teamName:    "testteam",
			machineName: "testmachine",
			expected:    "ssh testmachine.testteam@localhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.devMode = tt.devMode
			result := server.formatSSHConnectionInfo(tt.teamName, tt.machineName)

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}
