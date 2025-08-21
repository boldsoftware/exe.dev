package exe

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"regexp"
	"sort"
	"strconv"

	"exe.dev/container"
)

// SSHDetails holds SSH connection information for a machine
type SSHDetails struct {
	Port       int
	PrivateKey string
	DockerHost *string // DOCKER_HOST value where this container runs
}

// GetMachineSSHDetails retrieves SSH connection details from the machines table
func (s *Server) GetMachineSSHDetails(machineID int) (*SSHDetails, error) {
	var port sql.NullInt64
	var privateKey sql.NullString
	var dockerHost sql.NullString

	query := `SELECT ssh_port, ssh_client_private_key, docker_host FROM machines WHERE id = ?`
	err := s.db.QueryRow(query, machineID).Scan(&port, &privateKey, &dockerHost)
	if err != nil {
		return nil, fmt.Errorf("failed to query machine SSH details: %v", err)
	}

	if !port.Valid || port.Int64 == 0 || !privateKey.Valid || privateKey.String == "" {
		// SSH not set up for this machine - this is for containers created before SSH support
		// TODO: Remove this code once all legacy containers are migrated
		log.Printf("Machine %d missing SSH setup, initializing SSH on container", machineID)
		err := s.setupContainerSSH(machineID)
		if err != nil {
			return nil, fmt.Errorf("failed to setup SSH on legacy container: %v", err)
		}

		// Re-query after setup
		err = s.db.QueryRow(query, machineID).Scan(&port, &privateKey, &dockerHost)
		if err != nil {
			return nil, fmt.Errorf("failed to re-query machine SSH details after setup: %v", err)
		}
	}

	sshPort := int(port.Int64)
	if sshPort <= 0 {
		return nil, fmt.Errorf("invalid SSH port for machine: %d", sshPort)
	}

	if privateKey.String == "" {
		return nil, fmt.Errorf("no SSH private key available for machine after setup")
	}

	var dockerHostPtr *string
	if dockerHost.Valid && dockerHost.String != "" {
		dockerHostPtr = &dockerHost.String
	}

	return &SSHDetails{
		Port:       sshPort,
		PrivateKey: privateKey.String,
		DockerHost: dockerHostPtr,
	}, nil
}

// setupContainerSSH sets up SSH on a legacy container that was created before SSH support
// TODO: Remove this method once all legacy containers are migrated to have SSH
func (s *Server) setupContainerSSH(machineID int) error {
	// Get machine details
	var containerID, userFingerprint, teamName, machineName, image string
	err := s.db.QueryRow(
		`SELECT container_id, created_by_user_id, team_name, name, image FROM machines WHERE id = ?`,
		machineID,
	).Scan(&containerID, &userFingerprint, &teamName, &machineName, &image)
	if err != nil {
		return fmt.Errorf("failed to get machine details: %v", err)
	}

	if containerID == "" {
		return fmt.Errorf("machine has no container ID")
	}

	// Generate SSH keys for this container
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		return fmt.Errorf("failed to generate SSH keys: %v", err)
	}

	// Set up SSH in the running container using the Docker manager's proper setup
	if s.containerManager != nil {
		// Use the Docker manager's setupContainerSSH method which properly configures all SSH files
		dockerManager, ok := s.containerManager.(*container.DockerManager)
		if ok {
			ctx := context.Background()
			err := dockerManager.SetupContainerSSH(ctx, containerID, "", sshKeys)
			if err != nil {
				log.Printf("Failed to setup SSH files in container %s: %v", containerID, err)
				// Continue anyway and update database
			}
		} else {
			log.Printf("Warning: Container manager is not DockerManager, cannot setup SSH files")
		}
	}

	// Update database with SSH keys
	_, err = s.db.Exec(`
		UPDATE machines SET 
			ssh_server_identity_key = ?, ssh_authorized_keys = ?, ssh_ca_public_key = ?,
			ssh_host_certificate = ?, ssh_client_private_key = ?, ssh_port = ?
		WHERE id = ?
	`, sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
		sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshKeys.SSHPort, machineID)
	if err != nil {
		return fmt.Errorf("failed to update machine SSH keys: %v", err)
	}

	log.Printf("SSH setup completed for machine %d", machineID)
	return nil
}

// runMigrations executes database migrations in order
func runMigrations(db *sql.DB) error {
	// Read all migration files
	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	// Filter and validate migration files
	var migrations []string
	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !migrationPattern.MatchString(entry.Name()) {
			continue
		}
		migrations = append(migrations, entry.Name())
	}

	// Sort migrations by number
	sort.Strings(migrations)

	// Get executed migrations
	executedMigrations := make(map[int]bool)
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='migrations'").Scan(&tableName)
	if err == nil {
		// Migrations table exists, load executed migrations
		rows, err := db.Query("SELECT migration_number FROM migrations")
		if err != nil {
			return fmt.Errorf("failed to query executed migrations: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var migrationNumber int
			if err := rows.Scan(&migrationNumber); err != nil {
				return fmt.Errorf("failed to scan migration number: %w", err)
			}
			executedMigrations[migrationNumber] = true
		}
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("failed to check migrations table: %w", err)
	} else {
		// Migrations table doesn't exist - executedMigrations remains empty
		slog.Info("migrations table not found, running all migrations")
	}

	// Run any migrations that haven't been executed
	for _, migration := range migrations {
		// Extract migration number from filename (e.g., "001-base.sql" -> 001)
		matches := migrationPattern.FindStringSubmatch(migration)
		if len(matches) != 2 {
			return fmt.Errorf("invalid migration filename format: %s", migration)
		}
		migrationNumber, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("failed to parse migration number from %s: %w", migration, err)
		}

		if !executedMigrations[migrationNumber] {
			slog.Info("running migration", "file", migration, "number", migrationNumber)
			if err := executeMigration(db, migration); err != nil {
				return fmt.Errorf("failed to execute migration %s: %w", migration, err)
			}
		}
	}

	return nil
}

// executeMigration executes a single migration file
func executeMigration(db *sql.DB, filename string) error {
	content, err := migrationFS.ReadFile("schema/" + filename)
	if err != nil {
		return fmt.Errorf("failed to read migration file %s: %w", filename, err)
	}

	_, err = db.Exec(string(content))
	if err != nil {
		return fmt.Errorf("failed to execute migration %s: %w", filename, err)
	}

	return nil
}
