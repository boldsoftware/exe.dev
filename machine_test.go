package exe

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestCreateMachine(t *testing.T) {
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

	// Create test user and team first
	userID := "test-user-id"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	if err := server.addTeamMember(userID, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Test creating a machine
	machineName := "testmachine"
	containerID := "mock-container-123"
	image := "ubuntu:22.04"

	err = server.createMachine(userID, teamName, machineName, containerID, image)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Verify machine was created correctly
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	if machine.TeamName != teamName {
		t.Errorf("Expected team name %s, got %s", teamName, machine.TeamName)
	}

	if machine.Name != machineName {
		t.Errorf("Expected machine name %s, got %s", machineName, machine.Name)
	}

	if machine.Image != image {
		t.Errorf("Expected image %s, got %s", image, machine.Image)
	}

	if machine.ContainerID == nil || *machine.ContainerID != containerID {
		t.Errorf("Expected container ID %s, got %v", containerID, machine.ContainerID)
	}

	// TODO: Verify machine was created by the correct user
	// This test needs to be updated to work with the new user management system

	if machine.Status != "pending" {
		t.Errorf("Expected status 'pending', got %s", machine.Status)
	}
}

func TestGetMachineByName(t *testing.T) {
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

	// Create test data
	userID := "test-user-id"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(userID, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create machine
	machineName := "testmachine"
	containerID := "mock-container-123"
	if err := server.createMachine(userID, teamName, machineName, containerID, "ubuntu:22.04"); err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Test getting existing machine
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	if machine.Name != machineName {
		t.Errorf("Expected machine name %s, got %s", machineName, machine.Name)
	}

	// Test getting non-existent machine
	_, err = server.getMachineByName(teamName, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("Expected sql.ErrNoRows for non-existent machine, got %v", err)
	}

	// Test getting machine from different team
	if err := server.createTeam("otherteam", email); err != nil {
		t.Fatalf("Failed to create other team: %v", err)
	}
	_, err = server.getMachineByName("otherteam", machineName)
	if err != sql.ErrNoRows {
		t.Errorf("Expected sql.ErrNoRows for machine in different team, got %v", err)
	}
}

func TestMachineUniqueConstraint(t *testing.T) {
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

	// Create test data
	userID := "test-user-id"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(userID, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create first machine
	machineName := "testmachine"
	err = server.createMachine(userID, teamName, machineName, "container-1", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create first machine: %v", err)
	}

	// Try to create machine with same name in same team - should fail
	err = server.createMachine(userID, teamName, machineName, "container-2", "ubuntu:22.04")
	if err == nil {
		t.Error("Expected error when creating machine with duplicate name in same team")
	}

	// Create machine with same name in different team - should succeed
	otherTeam := "otherteam"
	if err := server.createTeam(otherTeam, email); err != nil {
		t.Fatalf("Failed to create other team: %v", err)
	}
	if err := server.addTeamMember(userID, otherTeam, true); err != nil {
		t.Fatalf("Failed to add to other team: %v", err)
	}

	err = server.createMachine(userID, otherTeam, machineName, "container-3", "ubuntu:22.04")
	if err != nil {
		t.Errorf("Failed to create machine with same name in different team: %v", err)
	}

	// Verify both machines exist
	machine1, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to get first machine: %v", err)
	}
	if *machine1.ContainerID != "container-1" {
		t.Errorf("Expected container-1, got %s", *machine1.ContainerID)
	}

	machine2, err := server.getMachineByName(otherTeam, machineName)
	if err != nil {
		t.Fatalf("Failed to get second machine: %v", err)
	}
	if *machine2.ContainerID != "container-3" {
		t.Errorf("Expected container-3, got %s", *machine2.ContainerID)
	}
}

func TestMachineTimestamps(t *testing.T) {
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

	// Create test data
	userID := "test-user-id"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(userID, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	beforeCreate := time.Now().UTC()

	// Create machine
	machineName := "testmachine"
	err = server.createMachine(userID, teamName, machineName, "container-123", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	afterCreate := time.Now().UTC()

	// Get machine and check timestamps
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	// Check CreatedAt is within reasonable range (allow 1 second tolerance for SQLite precision)
	tolerance := time.Second
	if machine.CreatedAt.Before(beforeCreate.Add(-tolerance)) || machine.CreatedAt.After(afterCreate.Add(tolerance)) {
		t.Errorf("CreatedAt timestamp %v is not within expected range %v - %v (±%v)",
			machine.CreatedAt, beforeCreate, afterCreate, tolerance)
	}

	// Check UpdatedAt is within reasonable range (allow 1 second tolerance for SQLite precision)
	if machine.UpdatedAt.Before(beforeCreate.Add(-tolerance)) || machine.UpdatedAt.After(afterCreate.Add(tolerance)) {
		t.Errorf("UpdatedAt timestamp %v is not within expected range %v - %v (±%v)",
			machine.UpdatedAt, beforeCreate, afterCreate, tolerance)
	}

	// LastStartedAt should be nil initially
	if machine.LastStartedAt != nil {
		t.Errorf("Expected LastStartedAt to be nil, got %v", machine.LastStartedAt)
	}
}

func TestMachineNameValidationIntegration(t *testing.T) {
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

	// Create test user and team
	userID := "test-user-123"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	if err := server.addTeamMember(userID, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	testCases := []struct {
		name          string
		machineName   string
		shouldSucceed bool
		description   string
	}{
		{"valid simple", "myapp", true, "simple valid name"},
		{"valid with numbers", "web123", true, "valid name with numbers"},
		{"valid with hyphens", "my-app", true, "valid name with hyphens"},
		{"valid 32 chars", "abcdefghijklmnopqrstuvwxyz123456", true, "valid 32 character name"},

		{"uppercase letters", "MyApp", false, "contains uppercase letters"},
		{"starts with number", "123app", false, "starts with number"},
		{"starts with hyphen", "-myapp", false, "starts with hyphen"},
		{"ends with hyphen", "myapp-", false, "ends with hyphen"},
		{"consecutive hyphens", "my--app", false, "contains consecutive hyphens"},
		{"too long", "abcdefghijklmnopqrstuvwxyz1234567", false, "33 characters (too long)"},
		{"contains underscore", "my_app", false, "contains underscore"},
		{"empty string", "", false, "empty string"},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Use a unique container ID for each test
			containerID := fmt.Sprintf("mock-container-%d", i)

			// Test direct validation first
			validationResult := server.isValidMachineName(tc.machineName)
			if validationResult != tc.shouldSucceed {
				t.Errorf("Validation mismatch for %s: expected %v, got %v", tc.description, tc.shouldSucceed, validationResult)
			}

			// Test actual machine creation - this should work for valid names
			// and is expected to work even for invalid names in this test since
			// we're testing the validation logic separately from database constraints
			if tc.shouldSucceed {
				err := server.createMachine(userID, teamName, tc.machineName, containerID, "ubuntu:22.04")
				if err != nil {
					t.Errorf("Failed to create machine with valid name %s (%s): %v", tc.machineName, tc.description, err)
				} else {
					// Verify machine was created
					machine, err := server.getMachineByName(teamName, tc.machineName)
					if err != nil {
						t.Errorf("Failed to retrieve created machine %s: %v", tc.machineName, err)
					} else if machine.Name != tc.machineName {
						t.Errorf("Machine name mismatch: expected %s, got %s", tc.machineName, machine.Name)
					}
				}
			}
		})
	}
}

func TestGeneratedMachineNamesAreValid(t *testing.T) {
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

	// Generate 100 random names and ensure they're all valid
	for i := 0; i < 100; i++ {
		generatedName := generateRandomContainerName()
		if !server.isValidMachineName(generatedName) {
			t.Errorf("Generated machine name '%s' failed validation", generatedName)
		}
		// Also check length
		if len(generatedName) > 32 {
			t.Errorf("Generated machine name '%s' is too long (%d chars)", generatedName, len(generatedName))
		}
	}
}
