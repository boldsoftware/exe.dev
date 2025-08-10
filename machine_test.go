package exe

import (
	"database/sql"
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

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test user and team first
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Test creating a machine
	machineName := "testmachine"
	containerID := "mock-container-123"
	image := "ubuntu:22.04"

	err = server.createMachine(fingerprint, teamName, machineName, containerID, image)
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

	if machine.CreatedByFingerprint != fingerprint {
		t.Errorf("Expected created by %s, got %s", fingerprint, machine.CreatedByFingerprint)
	}

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

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test data
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create machine
	machineName := "testmachine"
	containerID := "mock-container-123"
	if err := server.createMachine(fingerprint, teamName, machineName, containerID, "ubuntu:22.04"); err != nil {
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

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test data
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create first machine
	machineName := "testmachine"
	err = server.createMachine(fingerprint, teamName, machineName, "container-1", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create first machine: %v", err)
	}

	// Try to create machine with same name in same team - should fail
	err = server.createMachine(fingerprint, teamName, machineName, "container-2", "ubuntu:22.04")
	if err == nil {
		t.Error("Expected error when creating machine with duplicate name in same team")
	}

	// Create machine with same name in different team - should succeed
	otherTeam := "otherteam"
	if err := server.createTeam(otherTeam, email); err != nil {
		t.Fatalf("Failed to create other team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, otherTeam, true); err != nil {
		t.Fatalf("Failed to add to other team: %v", err)
	}

	err = server.createMachine(fingerprint, otherTeam, machineName, "container-3", "ubuntu:22.04")
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

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test data
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	beforeCreate := time.Now().UTC()
	
	// Create machine
	machineName := "testmachine"
	err = server.createMachine(fingerprint, teamName, machineName, "container-123", "ubuntu:22.04")
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