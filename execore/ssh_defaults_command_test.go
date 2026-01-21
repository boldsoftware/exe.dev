package execore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

func TestDefaultsCommand_Hidden(t *testing.T) {
	sshServer := &SSHServer{}
	ct := NewCommandTree(sshServer)

	cmd := ct.FindCommand([]string{"defaults"})
	if cmd == nil {
		t.Fatal("defaults command not found")
	}

	if !cmd.Hidden {
		t.Error("defaults command should be hidden")
	}
}

func TestDefaultsCommand_SubcommandsExist(t *testing.T) {
	sshServer := &SSHServer{}
	ct := NewCommandTree(sshServer)

	subcommands := []string{"write", "read", "delete"}
	for _, subName := range subcommands {
		subCmd := ct.FindCommand([]string{"defaults", subName})
		if subCmd == nil {
			t.Errorf("defaults %s subcommand not found", subName)
		} else if subCmd.Name != subName {
			t.Errorf("expected subcommand name %q, got %q", subName, subCmd.Name)
		}
	}
}

func setupDefaultsTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "defaults_test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		rawDB.Close()
		t.Fatalf("Failed to run migrations: %v", err)
	}
	rawDB.Close()

	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("Failed to create sqlite wrapper: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func createDefaultsTestUser(t *testing.T, db *sqlite.DB, userID, email string) {
	t.Helper()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		return q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  email,
			Region: "pdx",
		})
	})
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
}

func TestDefaultsWriteAndRead(t *testing.T) {
	db := setupDefaultsTestDB(t)

	server := &Server{log: tslog.Slogger(t), db: db}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	ctx := context.Background()
	userID := "test-user-defaults"
	createDefaultsTestUser(t, db, userID, "test@example.com")

	user := &exedb.User{UserID: userID, Email: "test@example.com"}

	t.Run("write new-vm-email false", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email", "false"})

		err := sshServer.handleDefaultsWrite(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsWrite() error = %v", err)
		}

		// Verify the value was written
		defaults, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetUserDefaults, userID)
		if err != nil {
			t.Fatalf("GetUserDefaults() error = %v", err)
		}
		if defaults.NewVMEmail == nil {
			t.Fatal("NewVMEmail should not be nil")
		}
		if *defaults.NewVMEmail != 0 {
			t.Errorf("NewVMEmail = %v, want 0 (false)", *defaults.NewVMEmail)
		}
	})

	t.Run("read new-vm-email", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email"})

		err := sshServer.handleDefaultsRead(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsRead() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "false") {
			t.Errorf("Expected output to contain 'false', got %q", result)
		}
	})

	t.Run("write new-vm-email true", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email", "true"})

		err := sshServer.handleDefaultsWrite(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsWrite() error = %v", err)
		}

		defaults, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetUserDefaults, userID)
		if err != nil {
			t.Fatalf("GetUserDefaults() error = %v", err)
		}
		if defaults.NewVMEmail == nil {
			t.Fatal("NewVMEmail should not be nil")
		}
		if *defaults.NewVMEmail != 1 {
			t.Errorf("NewVMEmail = %v, want 1 (true)", *defaults.NewVMEmail)
		}
	})

	t.Run("write new-vm-email off", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email", "off"})

		err := sshServer.handleDefaultsWrite(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsWrite() error = %v", err)
		}

		defaults, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetUserDefaults, userID)
		if err != nil {
			t.Fatalf("GetUserDefaults() error = %v", err)
		}
		if defaults.NewVMEmail == nil {
			t.Fatal("NewVMEmail should not be nil")
		}
		if *defaults.NewVMEmail != 0 {
			t.Errorf("NewVMEmail = %v, want 0 (false/off)", *defaults.NewVMEmail)
		}
	})

	t.Run("write new-vm-email on", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email", "on"})

		err := sshServer.handleDefaultsWrite(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsWrite() error = %v", err)
		}

		defaults, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetUserDefaults, userID)
		if err != nil {
			t.Fatalf("GetUserDefaults() error = %v", err)
		}
		if defaults.NewVMEmail == nil {
			t.Fatal("NewVMEmail should not be nil")
		}
		if *defaults.NewVMEmail != 1 {
			t.Errorf("NewVMEmail = %v, want 1 (true/on)", *defaults.NewVMEmail)
		}
	})

	t.Run("read all defaults", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe"})

		err := sshServer.handleDefaultsRead(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsRead() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "new-vm-email") {
			t.Errorf("Expected output to contain 'new-vm-email', got %q", result)
		}
	})

	t.Run("delete new-vm-email", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email"})

		err := sshServer.handleDefaultsDelete(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsDelete() error = %v", err)
		}

		defaults, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetUserDefaults, userID)
		if err != nil {
			t.Fatalf("GetUserDefaults() error = %v", err)
		}
		if defaults.NewVMEmail != nil {
			t.Errorf("NewVMEmail should be nil after delete, got %v", *defaults.NewVMEmail)
		}
	})
}

func TestDefaultsWriteErrors(t *testing.T) {
	db := setupDefaultsTestDB(t)

	server := &Server{log: tslog.Slogger(t), db: db}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	ctx := context.Background()
	userID := "test-user-defaults-errors"
	createDefaultsTestUser(t, db, userID, "test-errors@example.com")

	user := &exedb.User{UserID: userID, Email: "test-errors@example.com"}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "wrong number of args",
			args:    []string{"dev.exe", "new-vm-email"},
			wantErr: "usage:",
		},
		{
			name:    "unknown domain",
			args:    []string{"com.apple", "key", "value"},
			wantErr: "unknown domain",
		},
		{
			name:    "unknown key",
			args:    []string{"dev.exe", "unknown-key", "value"},
			wantErr: "unknown key",
		},
		{
			name:    "invalid bool value",
			args:    []string{"dev.exe", "new-vm-email", "maybe"},
			wantErr: "invalid value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := &MockOutput{}
			cc := createTestContext(user, output, tt.args)

			err := sshServer.handleDefaultsWrite(ctx, cc)
			if err == nil {
				t.Errorf("handleDefaultsWrite() expected error containing %q", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("handleDefaultsWrite() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestGetUserDefaultNewVMEmail(t *testing.T) {
	db := setupDefaultsTestDB(t)

	server := &Server{log: tslog.Slogger(t), db: db}
	sshServer := &SSHServer{server: server}

	ctx := context.Background()
	userID := "test-user-email-default"
	createDefaultsTestUser(t, db, userID, "test-email@example.com")

	t.Run("returns true when no defaults set", func(t *testing.T) {
		result := sshServer.getUserDefaultNewVMEmail(ctx, userID)
		if result != true {
			t.Errorf("getUserDefaultNewVMEmail() = %v, want true", result)
		}
	})

	t.Run("returns false when new-vm-email is false", func(t *testing.T) {
		// Set the default to false (0)
		falseVal := int64(0)
		err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.UpsertUserDefaultNewVMEmail(ctx, exedb.UpsertUserDefaultNewVMEmailParams{
				UserID:     userID,
				NewVMEmail: &falseVal,
			})
		})
		if err != nil {
			t.Fatalf("UpsertUserDefaultNewVMEmail() error = %v", err)
		}

		result := sshServer.getUserDefaultNewVMEmail(ctx, userID)
		if result != false {
			t.Errorf("getUserDefaultNewVMEmail() = %v, want false", result)
		}
	})

	t.Run("returns true when new-vm-email is true", func(t *testing.T) {
		// Set the default to true (1)
		trueVal := int64(1)
		err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.UpsertUserDefaultNewVMEmail(ctx, exedb.UpsertUserDefaultNewVMEmailParams{
				UserID:     userID,
				NewVMEmail: &trueVal,
			})
		})
		if err != nil {
			t.Fatalf("UpsertUserDefaultNewVMEmail() error = %v", err)
		}

		result := sshServer.getUserDefaultNewVMEmail(ctx, userID)
		if result != true {
			t.Errorf("getUserDefaultNewVMEmail() = %v, want true", result)
		}
	})

	t.Run("returns true for nonexistent user", func(t *testing.T) {
		result := sshServer.getUserDefaultNewVMEmail(ctx, "nonexistent-user")
		if result != true {
			t.Errorf("getUserDefaultNewVMEmail() = %v, want true for nonexistent user", result)
		}
	})
}

func TestDefaultsReadNoDefaults(t *testing.T) {
	db := setupDefaultsTestDB(t)

	server := &Server{log: tslog.Slogger(t), db: db}
	sshServer := &SSHServer{server: server}

	ctx := context.Background()
	userID := "test-user-no-defaults"
	createDefaultsTestUser(t, db, userID, "test-nodefaults@example.com")

	user := &exedb.User{UserID: userID, Email: "test-nodefaults@example.com"}

	t.Run("read specific key when not set", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"dev.exe", "new-vm-email"})

		err := sshServer.handleDefaultsRead(ctx, cc)
		if err != nil {
			t.Errorf("handleDefaultsRead() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "(not set)") {
			t.Errorf("Expected output to contain '(not set)', got %q", result)
		}
	})
}
