package execore

import (
	"context"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/tslog"
)

// TestCpCommand_Exists tests that the cp command is registered in the command tree.
func TestCpCommand_Exists(t *testing.T) {
	t.Parallel()
	sshServer := &SSHServer{}
	ct := NewCommandTree(sshServer)

	cmd := ct.FindCommand([]string{"cp"})
	if cmd == nil {
		t.Fatal("cp command not found in command tree")
	}

	if cmd.Name != "cp" {
		t.Errorf("expected command name 'cp', got %q", cmd.Name)
	}

	// Verify command has proper description
	if cmd.Description == "" {
		t.Error("cp command should have a description")
	}

	// Verify command has usage information
	if cmd.Usage == "" {
		t.Error("cp command should have usage information")
	}

	// Verify command accepts positional args (source VM name, optional dest name)
	if !cmd.HasPositionalArgs {
		t.Error("cp command should have HasPositionalArgs=true")
	}

	// Verify command has flag set for --json
	if cmd.FlagSetFunc == nil {
		t.Fatal("cp command should have a FlagSetFunc")
	}

	fs := cmd.FlagSetFunc()
	if fs.Lookup("json") == nil {
		t.Error("cp command should have --json flag")
	}
}

// TestCpCommand_FlagParsing tests flag parsing for the cp command.
func TestCpCommand_FlagParsing(t *testing.T) {
	t.Parallel()
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	tests := []struct {
		name         string
		commandPath  []string
		expectedArgs []string
		checkFlags   func(t *testing.T, cc *exemenu.CommandContext)
		expectErr    bool
	}{
		{
			name:         "cp with source only",
			commandPath:  []string{"cp", "source-vm"},
			expectedArgs: []string{"source-vm"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for cp command")
				}
			},
		},
		{
			name:         "cp with source and dest",
			commandPath:  []string{"cp", "source-vm", "dest-vm"},
			expectedArgs: []string{"source-vm", "dest-vm"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for cp command")
				}
			},
		},
		{
			name:         "cp with --json flag",
			commandPath:  []string{"cp", "source-vm", "--json"},
			expectedArgs: []string{"source-vm"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for cp command")
				}
				jsonFlag := cc.FlagSet.Lookup("json").Value.String()
				if jsonFlag != "true" {
					t.Errorf("Expected json to be 'true', got %q", jsonFlag)
				}
			},
		},
		{
			name:         "cp with dest and --json flag",
			commandPath:  []string{"cp", "source-vm", "dest-vm", "--json"},
			expectedArgs: []string{"source-vm", "dest-vm"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for cp command")
				}
				jsonFlag := cc.FlagSet.Lookup("json").Value.String()
				if jsonFlag != "true" {
					t.Errorf("Expected json to be 'true', got %q", jsonFlag)
				}
			},
		},
		{
			name:         "cp with flags before args",
			commandPath:  []string{"cp", "--json", "source-vm", "dest-vm"},
			expectedArgs: []string{"source-vm", "dest-vm"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for cp command")
				}
				jsonFlag := cc.FlagSet.Lookup("json").Value.String()
				if jsonFlag != "true" {
					t.Errorf("Expected json to be 'true', got %q", jsonFlag)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedCC *exemenu.CommandContext
			mockHandler := func(ctx context.Context, cc *exemenu.CommandContext) error {
				capturedCC = cc
				return nil
			}

			cmd := sshServer.commands.FindCommand([]string{"cp"})
			if cmd == nil {
				t.Skip("cp command not implemented yet")
			}
			originalHandler := cmd.Handler
			cmd.Handler = mockHandler
			defer func() { cmd.Handler = originalHandler }()

			output := &MockOutput{}
			cc := createTestContext(user, output, []string{})
			ctx := context.Background()

			rc := sshServer.commands.ExecuteCommand(ctx, cc, tt.commandPath)
			if tt.expectErr {
				if rc == 0 {
					t.Fatal("ExecuteCommand() should have failed")
				}
				return
			}

			if rc != 0 {
				t.Errorf("ExecuteCommand() = %d, want 0; output: %s", rc, output.String())
				return
			}

			if capturedCC == nil {
				t.Fatal("Handler was not called")
			}

			// Check remaining args
			if len(capturedCC.Args) != len(tt.expectedArgs) {
				t.Errorf("Expected %d args, got %d: %v", len(tt.expectedArgs), len(capturedCC.Args), capturedCC.Args)
			}
			for i, expected := range tt.expectedArgs {
				if i >= len(capturedCC.Args) {
					t.Errorf("Missing arg %d: expected %q", i, expected)
				} else if capturedCC.Args[i] != expected {
					t.Errorf("Arg %d: expected %q, got %q", i, expected, capturedCC.Args[i])
				}
			}

			tt.checkFlags(t, capturedCC)
		})
	}
}

// TestCpCommand_UsageErrors tests that cp command returns proper usage errors.
func TestCpCommand_UsageErrors(t *testing.T) {
	t.Parallel()
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	tests := []struct {
		name        string
		commandPath []string
		wantErr     bool
		errContains string
	}{
		{
			name:        "cp with no arguments",
			commandPath: []string{"cp"},
			wantErr:     true,
			errContains: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := sshServer.commands.FindCommand([]string{"cp"})
			if cmd == nil {
				t.Skip("cp command not implemented yet")
			}

			output := &MockOutput{}
			cc := createTestContext(user, output, []string{})
			ctx := context.Background()

			rc := sshServer.commands.ExecuteCommand(ctx, cc, tt.commandPath)
			outputStr := strings.ToLower(output.String())

			if tt.wantErr {
				if rc == 0 {
					t.Errorf("Expected error but got success")
				}
				if !strings.Contains(outputStr, tt.errContains) {
					t.Errorf("Expected output to contain %q, got %q", tt.errContains, output.String())
				}
			} else {
				if rc != 0 {
					t.Errorf("Expected success but got error: %s", output.String())
				}
			}
		})
	}
}

// TestCpCommand_InvalidFlagErrors tests that cp command handles invalid flags properly.
func TestCpCommand_InvalidFlagErrors(t *testing.T) {
	t.Parallel()
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	tests := []struct {
		name        string
		commandPath []string
		errContains string
	}{
		{
			name:        "unknown flag",
			commandPath: []string{"cp", "source-vm", "--unknown-flag=value"},
			errContains: "flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := sshServer.commands.FindCommand([]string{"cp"})
			if cmd == nil {
				t.Skip("cp command not implemented yet")
			}

			output := &MockOutput{}
			cc := createTestContext(user, output, []string{})
			ctx := context.Background()

			rc := sshServer.commands.ExecuteCommand(ctx, cc, tt.commandPath)

			if rc == 0 {
				t.Errorf("Expected failure for invalid flag")
			}
			outputStr := strings.ToLower(output.String())
			if !strings.Contains(outputStr, tt.errContains) {
				t.Errorf("Expected output to contain %q, got %q", tt.errContains, output.String())
			}
		})
	}
}

// TestCpCommand_Examples tests that cp command has examples.
func TestCpCommand_Examples(t *testing.T) {
	t.Parallel()
	sshServer := &SSHServer{}
	ct := NewCommandTree(sshServer)

	cmd := ct.FindCommand([]string{"cp"})
	if cmd == nil {
		t.Skip("cp command not implemented yet")
	}

	if len(cmd.Examples) == 0 {
		t.Error("cp command should have examples")
	}

	// Check that at least one example shows basic usage
	hasBasicExample := false
	for _, example := range cmd.Examples {
		if strings.Contains(example, "cp") {
			hasBasicExample = true
			break
		}
	}
	if !hasBasicExample {
		t.Error("cp command should have an example showing basic cp usage")
	}
}

// TestCpCommand_HelpOutput tests that help for cp command shows relevant info.
func TestCpCommand_HelpOutput(t *testing.T) {
	t.Parallel()
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	cmd := sshServer.commands.FindCommand([]string{"cp"})
	if cmd == nil {
		t.Skip("cp command not implemented yet")
	}

	output := &MockOutput{}
	cc := createTestContext(user, output, []string{"cp"})
	ctx := context.Background()

	err := sshServer.handleHelpCommand(ctx, cc)
	if err != nil {
		t.Errorf("handleHelpCommand() error = %v", err)
	}

	result := output.String()

	// Check that help output contains expected information
	expectedContents := []string{
		"cp",
		"source-vm",
		"new-name",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(strings.ToLower(result), strings.ToLower(expected)) {
			t.Errorf("Help output should contain %q, got:\n%s", expected, result)
		}
	}
}
