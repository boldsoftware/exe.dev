package execore

import (
	"context"
	"flag"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/tslog"
	"github.com/gliderlabs/ssh"
)

// MockOutput captures output for testing
type MockOutput struct {
	output strings.Builder
}

func (m *MockOutput) Write(p []byte) (n int, err error) {
	return m.output.Write(p)
}

func (m *MockOutput) String() string {
	return m.output.String()
}

// mockShellSession is a minimal implementation of exemenu.ShellSession for testing
type mockShellSession struct{}

func (m *mockShellSession) Read(p []byte) (n int, err error)  { return 0, nil }
func (m *mockShellSession) Write(p []byte) (n int, err error) { return len(p), nil }
func (m *mockShellSession) Close() error                      { return nil }
func (m *mockShellSession) Push([]byte)                       {}
func (m *mockShellSession) Context() context.Context          { return context.Background() }
func (m *mockShellSession) Environ() []string                 { return nil }
func (m *mockShellSession) User() string                      { return "test" }
func (m *mockShellSession) Pty() (ssh.Pty, bool)              { return ssh.Pty{}, false }
func (m *mockShellSession) WaitWindowChange() bool            { return false }

// Helper to create test context
func createTestContext(user *exedb.User, output *MockOutput, args []string) *exemenu.CommandContext {
	var userInfo *exemenu.UserInfo
	if user != nil {
		userInfo = &exemenu.UserInfo{ID: user.UserID, Email: user.Email}
	}
	return &exemenu.CommandContext{
		User:      userInfo,
		PublicKey: "test-key",
		Args:      args,
		Output:    output,
		Terminal:  nil, // For now, we'll test without the terminal interface
	}
}

func TestCommandTree_FindCommand(t *testing.T) {
	sshServer := &SSHServer{}
	ct := NewCommandTree(sshServer)

	tests := []struct {
		name     string
		path     []string
		wantName string
		wantNil  bool
	}{
		{
			name:     "find help command",
			path:     []string{"help"},
			wantName: "help",
		},
		{
			name:     "find list command",
			path:     []string{"ls"},
			wantName: "ls",
		},
		{
			name:     "find list by alias",
			path:     []string{"role"},
			wantName: "hireme",
		},
		{
			name:    "nonexistent command",
			path:    []string{"nonexistent"},
			wantNil: true,
		},
		{
			name:    "nonexistent subcommand",
			path:    []string{"ls", "nonexistent"},
			wantNil: true,
		},
		{
			name:    "nonexistent sub-subcommand",
			path:    []string{"42", "setup", "foo", "bar"},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := ct.FindCommand(tt.path)
			if tt.wantNil {
				if cmd != nil {
					t.Errorf("FindCommand() = %v, want nil", cmd.Name)
				}
			} else {
				if cmd == nil {
					t.Errorf("FindCommand() = nil, want command with name %s", tt.wantName)
				} else if cmd.Name != tt.wantName {
					t.Errorf("FindCommand() = %s, want %s", cmd.Name, tt.wantName)
				}
			}
		})
	}
}

func TestCommandContext_HelperMethods(t *testing.T) {
	output := &MockOutput{}

	ctx := &exemenu.CommandContext{
		Output:   output,
		Terminal: nil, // Non-interactive for this test
	}

	// Test Write
	ctx.Write("Hello %s", "world")
	if !strings.Contains(output.String(), "Hello world") {
		t.Errorf("Write() did not output expected text")
	}

	// Test Writeln
	ctx.Writeln("Line %d", 1)
	if !strings.Contains(output.String(), "Line 1\r\n") {
		t.Errorf("Writeln() did not add carriage return and newline")
	}

	// Test non-interactive context
	if ctx.IsInteractive() {
		t.Errorf("IsInteractive() = true, want false when terminal is nil")
	}

	// Test IsSSHExec - no SSHSession, not interactive (web/mobile flow)
	if ctx.IsSSHExec() {
		t.Errorf("IsSSHExec() = true, want false when SSHSession is nil")
	}

	// Test IsSSHExec - has SSHSession, not interactive (ssh exec)
	ctx.SSHSession = &mockShellSession{}
	if !ctx.IsSSHExec() {
		t.Errorf("IsSSHExec() = false, want true for non-interactive SSH session")
	}

	_, err := ctx.ReadLine()
	if err == nil {
		t.Errorf("ReadLine() should return error when not interactive")
	}
}

func TestHelpCommand(t *testing.T) {
	// Create test server and dependencies
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	t.Run("general help", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{})
		ctx := context.Background()

		err := sshServer.handleHelpCommand(ctx, cc)
		if err != nil {
			t.Errorf("handleHelpCommand() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "EXE.DEV") {
			t.Errorf("Help output should contain 'EXE.DEV'")
		}
		if !strings.Contains(result, "commands") {
			t.Errorf("Help output should contain 'commands'")
		}
		if !strings.Contains(result, "ls") {
			t.Errorf("Help output should contain 'ls'")
		}
		if !strings.Contains(result, "exit") {
			t.Errorf("Help output should contain 'exit'")
		}
	})

	t.Run("specific command help", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"whoami"})
		ctx := context.Background()

		err := sshServer.handleHelpCommand(ctx, cc)
		if err != nil {
			t.Errorf("handleHelpCommand() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "Command: whoami") {
			t.Errorf("Help output should contain 'Command: whoami': %q", result)
		}
		if !strings.Contains(result, "Show your user information") {
			t.Errorf("Help output should contain command description: %q", result)
		}
	})

	t.Run("help for nonexistent command", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"nonexistent"})
		ctx := context.Background()

		err := sshServer.handleHelpCommand(ctx, cc)
		if err != nil {
			t.Errorf("handleHelpCommand() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "unrecognized command") {
			t.Errorf("Help output should contain 'Unknown command' for nonexistent command. Actual output:\n%s\n", output)
		}
	})
}

func TestExecuteCommand(t *testing.T) {
	// Create test server and dependencies
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	t.Run("execute help command", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{})
		ctx := context.Background()

		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"help"})
		if rc != 0 {
			t.Errorf("ExecuteCommand() = %d, want 0", rc)
		}

		result := output.String()
		if !strings.Contains(result, "EXE.DEV") {
			t.Errorf("Command output should contain 'EXE.DEV'")
		}
	})

	t.Run("execute nonexistent command", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{})
		ctx := context.Background()

		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"nonexistent"})
		if rc == 0 {
			t.Errorf("ExecuteCommand() should fail for nonexistent command")
		}
		result := output.String()
		if !strings.Contains(result, `command not found: "nonexistent"`) {
			t.Errorf("Output should indicate command lookup failure, got:\n%s\n", result)
		}
	})

	t.Run("execute command with args", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(user, output, []string{"whoami"})
		ctx := context.Background()
		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"help"})
		if rc != 0 {
			t.Errorf("ExecuteCommand() = %d, want 0", rc)
		}

		// Verify args were passed correctly by checking help output for whoami
		result := output.String()
		if !strings.Contains(result, "whoami") {
			t.Errorf("Command should have received whoami as argument")
		}
	})
}

func TestGetAvailableCommands(t *testing.T) {
	// Create test server and dependencies
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	ctx := createTestContext(user, &MockOutput{}, []string{})

	t.Run("get root commands", func(t *testing.T) {
		available := sshServer.commands.GetAvailableCommands(ctx)

		// Should have all the main commands
		commandNames := make(map[string]bool)
		for _, cmd := range available {
			commandNames[cmd.Name] = true
		}

		expectedCommands := []string{"help", "doc", "ls", "new", "rm", "whoami"}
		for _, expected := range expectedCommands {
			if !commandNames[expected] {
				t.Errorf("Expected command %s not found in available commands", expected)
			}
		}
	})
}

func TestCommandFlagParsing(t *testing.T) {
	// Create test server and dependencies
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
			name:         "new command with no flags",
			commandPath:  []string{"new"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				// Check default values
				name := cc.FlagSet.Lookup("name").Value.String()
				image := cc.FlagSet.Lookup("image").Value.String()
				command := cc.FlagSet.Lookup("command").Value.String()

				if name != "" {
					t.Errorf("Expected default name to be empty, got %q", name)
				}
				if image != "exeuntu" {
					t.Errorf("Expected default image to be 'exeuntu', got %q", image)
				}
				if command != "auto" {
					t.Errorf("Expected default command to be 'auto', got %q", command)
				}
			},
		},
		{
			name:         "new command with name flag",
			commandPath:  []string{"new", "--name=test-machine"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				name := cc.FlagSet.Lookup("name").Value.String()
				if name != "test-machine" {
					t.Errorf("Expected name to be 'test-machine', got %q", name)
				}
			},
		},
		{
			name:         "new command with multiple flags",
			commandPath:  []string{"new", "--name=my-machine", "--image=ubuntu:22.04"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				name := cc.FlagSet.Lookup("name").Value.String()
				image := cc.FlagSet.Lookup("image").Value.String()

				if name != "my-machine" {
					t.Errorf("Expected name to be 'my-machine', got %q", name)
				}
				if image != "ubuntu:22.04" {
					t.Errorf("Expected image to be 'ubuntu:22.04', got %q", image)
				}
			},
		},
		{
			name:         "new command with flags and remaining args",
			commandPath:  []string{"new", "--name=test", "arg1", "arg2"},
			expectedArgs: []string{"arg1", "arg2"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				name := cc.FlagSet.Lookup("name").Value.String()
				if name != "test" {
					t.Errorf("Expected name to be 'test', got %q", name)
				}
			},
			expectErr: true,
		},
		{
			name:         "new command with separated flag value",
			commandPath:  []string{"new", "--name", "separated-value"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				name := cc.FlagSet.Lookup("name").Value.String()
				if name != "separated-value" {
					t.Errorf("Expected name to be 'separated-value', got %q", name)
				}
			},
		},
		{
			name:         "help command uses raw args",
			commandPath:  []string{"help"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				// help uses RawArgs so flags are not parsed by the framework
				if cc.FlagSet != nil {
					t.Error("Expected FlagSet to be nil for RawArgs help command")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock handler that captures the CommandContext for inspection
			var capturedCC *exemenu.CommandContext
			mockHandler := func(ctx context.Context, cc *exemenu.CommandContext) error {
				capturedCC = cc
				return nil
			}

			// Replace the handler for testing
			cmd := sshServer.commands.FindCommand([]string{tt.commandPath[0]})
			if cmd == nil {
				t.Fatalf("Command %s not found", tt.commandPath[0])
			}
			originalHandler := cmd.Handler
			cmd.Handler = mockHandler

			// Execute the command
			output := &MockOutput{}
			cc := createTestContext(user, output, []string{})
			ctx := context.Background()

			rc := sshServer.commands.ExecuteCommand(ctx, cc, tt.commandPath)
			if tt.expectErr {
				if rc == 0 {
					t.Fatal("ExecuteCommand() should have failed")
				}
				if capturedCC != nil {
					t.Fatal("Handler should not have been called, but was")
				}
				return
			}

			if rc != 0 {
				t.Errorf("ExecuteCommand() = %d, want 0", rc)
			}

			// Check that the handler was called with the right context
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

			// Check flags
			tt.checkFlags(t, capturedCC)

			// Restore original handler
			cmd.Handler = originalHandler
		})
	}
}

func TestSubcommandFlagParsing(t *testing.T) {
	// Create test server and dependencies

	// Create a custom command tree with subcommands that have flags for testing
	testFlagSetFunc := func() *flag.FlagSet {
		fs := flag.NewFlagSet("test-sub", flag.ContinueOnError)
		fs.String("option", "default", "test option")
		fs.Bool("verbose", false, "verbose mode")
		return fs
	}

	var capturedContext *exemenu.CommandContext
	testSubHandler := func(ctx context.Context, cc *exemenu.CommandContext) error {
		capturedContext = cc
		return nil
	}

	customTree := &exemenu.CommandTree{
		Commands: []*exemenu.Command{
			{
				Name:        "parent",
				Description: "Parent command with subcommands",
				Handler: func(ctx context.Context, cc *exemenu.CommandContext) error {
					return nil
				},
				Subcommands: []*exemenu.Command{
					{
						Name:              "sub",
						Description:       "Subcommand with flags",
						Handler:           testSubHandler,
						FlagSetFunc:       testFlagSetFunc,
						HasPositionalArgs: true,
					},
					{
						Name:              "nosub",
						Description:       "Subcommand without flags",
						Handler:           testSubHandler,
						HasPositionalArgs: true,
					},
				},
			},
		},
	}

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	tests := []struct {
		name         string
		commandPath  []string
		expectedArgs []string
		checkFlags   func(t *testing.T, cc *exemenu.CommandContext)
		expectErr    bool
	}{
		{
			name:         "subcommand with default flags",
			commandPath:  []string{"parent", "sub"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for subcommand with flags")
				}
				option := cc.FlagSet.Lookup("option").Value.String()
				verbose := cc.FlagSet.Lookup("verbose").Value.String()
				if option != "default" {
					t.Errorf("Expected option to be 'default', got %q", option)
				}
				if verbose != "false" {
					t.Errorf("Expected verbose to be 'false', got %q", verbose)
				}
			},
		},
		{
			name:         "subcommand with custom flags",
			commandPath:  []string{"parent", "sub", "--option=custom", "--verbose"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for subcommand with flags")
				}
				option := cc.FlagSet.Lookup("option").Value.String()
				verbose := cc.FlagSet.Lookup("verbose").Value.String()
				if option != "custom" {
					t.Errorf("Expected option to be 'custom', got %q", option)
				}
				if verbose != "true" {
					t.Errorf("Expected verbose to be 'true', got %q", verbose)
				}
			},
		},
		{
			name:         "subcommand with flags and remaining args",
			commandPath:  []string{"parent", "sub", "--option=test", "arg1", "arg2"},
			expectedArgs: []string{"arg1", "arg2"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for subcommand with flags")
				}
				option := cc.FlagSet.Lookup("option").Value.String()
				if option != "test" {
					t.Errorf("Expected option to be 'test', got %q", option)
				}
			},
		},
		{
			name:         "subcommand without flags",
			commandPath:  []string{"parent", "nosub", "arg1"},
			expectedArgs: []string{"arg1"},
			checkFlags: func(t *testing.T, cc *exemenu.CommandContext) {
				if cc.FlagSet != nil {
					t.Errorf("Expected FlagSet to be nil for subcommand without flags, got %v", cc.FlagSet)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedContext = nil // Reset

			output := &MockOutput{}
			cc := createTestContext(user, output, []string{})
			ctx := context.Background()

			rc := customTree.ExecuteCommand(ctx, cc, tt.commandPath)
			if tt.expectErr {
				if rc == 0 {
					t.Fatal("ExecuteCommand() should have failed")
				}
				if capturedContext != nil {
					t.Fatal("Handler should not have been called, but was")
				}
				return
			}

			if rc != 0 {
				t.Errorf("ExecuteCommand() = %d, want 0", rc)
			}

			// Check that the handler was called
			if capturedContext == nil {
				t.Fatal("Handler was not called")
			}

			// Check remaining args
			if len(capturedContext.Args) != len(tt.expectedArgs) {
				t.Errorf("Expected %d args, got %d: %v", len(tt.expectedArgs), len(capturedContext.Args), capturedContext.Args)
			}
			for i, expected := range tt.expectedArgs {
				if i >= len(capturedContext.Args) {
					t.Errorf("Missing arg %d: expected %q", i, expected)
				} else if capturedContext.Args[i] != expected {
					t.Errorf("Arg %d: expected %q, got %q", i, expected, capturedContext.Args[i])
				}
			}

			// Check flags
			tt.checkFlags(t, capturedContext)
		})
	}
}

func TestFlagParsingErrorHandling(t *testing.T) {
	// Create test server and dependencies
	server := &Server{log: tslog.Slogger(t)}
	sshServer := &SSHServer{server: server}
	sshServer.commands = NewCommandTree(sshServer)

	user := &exedb.User{UserID: "test-user", Email: "test@example.com"}

	tests := []struct {
		name        string
		commandPath []string
		expectError bool
		errorText   string
	}{
		{
			name:        "unknown flag",
			commandPath: []string{"new", "--unknown-flag=value"},
			expectError: true,
			errorText:   "flag parsing error",
		},
		{
			name:        "flag without value",
			commandPath: []string{"new", "--name"},
			expectError: true,
			errorText:   "flag parsing error",
		},
		{
			name:        "valid flags",
			commandPath: []string{"new", "--name=valid"},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := &MockOutput{}
			cc := createTestContext(user, output, []string{})
			ctx := context.Background()

			// For valid flags test, replace handler with a mock to avoid business logic
			var originalHandler func(context.Context, *exemenu.CommandContext) error
			if !tt.expectError {
				cmd := sshServer.commands.FindCommand([]string{tt.commandPath[0]})
				if cmd != nil {
					originalHandler = cmd.Handler
					cmd.Handler = func(ctx context.Context, cc *exemenu.CommandContext) error {
						return nil // Success
					}
				}
			}

			rc := sshServer.commands.ExecuteCommand(ctx, cc, tt.commandPath)

			// Restore original handler if we replaced it
			if originalHandler != nil {
				cmd := sshServer.commands.FindCommand([]string{tt.commandPath[0]})
				if cmd != nil {
					cmd.Handler = originalHandler
				}
			}

			if tt.expectError {
				if rc == 0 {
					t.Errorf("Expected failure but got success")
				} else if !strings.Contains(output.String(), tt.errorText) {
					t.Errorf("Expected output containing %q, got %q", tt.errorText, output.String())
				}
			} else {
				if rc != 0 {
					t.Errorf("Expected success but got exit code %d", rc)
				}
			}
		})
	}
}

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name        string
		command     *exemenu.Command
		expectError bool
		errorText   string
	}{
		{
			name: "valid command with positional args only",
			command: &exemenu.Command{
				Name:              "test",
				HasPositionalArgs: true,
				Handler:           func(context.Context, *exemenu.CommandContext) error { return nil },
			},
			expectError: false,
		},
		{
			name: "valid command with subcommands only",
			command: &exemenu.Command{
				Name: "test",
				Subcommands: []*exemenu.Command{
					{
						Name:    "sub",
						Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
					},
				},
				Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
			},
			expectError: false,
		},
		{
			name: "valid command with neither positional args nor subcommands",
			command: &exemenu.Command{
				Name:    "test",
				Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
			},
			expectError: false,
		},
		{
			name: "invalid command with both positional args and subcommands",
			command: &exemenu.Command{
				Name:              "test",
				HasPositionalArgs: true,
				Subcommands: []*exemenu.Command{
					{
						Name:    "sub",
						Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
					},
				},
				Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
			},
			expectError: true,
			errorText:   "cannot have both positional arguments and subcommands",
		},
		{
			name: "invalid subcommand with both positional args and subcommands",
			command: &exemenu.Command{
				Name: "parent",
				Subcommands: []*exemenu.Command{
					{
						Name:              "invalid-sub",
						HasPositionalArgs: true,
						Subcommands: []*exemenu.Command{
							{
								Name:    "nested",
								Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
							},
						},
						Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
					},
				},
				Handler: func(context.Context, *exemenu.CommandContext) error { return nil },
			},
			expectError: true,
			errorText:   "in subcommand of \"parent\": command \"invalid-sub\" cannot have both positional arguments and subcommands",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exemenu.ValidateCommand(tt.command)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorText) {
					t.Errorf("Expected error containing %q, got %q", tt.errorText, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestSSHKeyCommand_SubcommandsExist(t *testing.T) {
	sshServer := &SSHServer{}
	ct := NewCommandTree(sshServer)

	// Test that ssh-key command exists
	cmd := ct.FindCommand([]string{"ssh-key"})
	if cmd == nil {
		t.Fatal("ssh-key command not found")
	}

	// Test that all subcommands exist
	subcommands := []string{"list", "add", "remove"}
	for _, subName := range subcommands {
		subCmd := ct.FindCommand([]string{"ssh-key", subName})
		if subCmd == nil {
			t.Errorf("ssh-key %s subcommand not found", subName)
		} else if subCmd.Name != subName {
			t.Errorf("expected subcommand name %q, got %q", subName, subCmd.Name)
		}
	}

	// Verify add subcommand has examples with ssh-keygen instructions
	addCmd := ct.FindCommand([]string{"ssh-key", "add"})
	if addCmd == nil {
		t.Fatal("ssh-key add command not found")
	}

	hasKeygenExample := false
	for _, example := range addCmd.Examples {
		if strings.Contains(example, "ssh-keygen") {
			hasKeygenExample = true
			break
		}
	}
	if !hasKeygenExample {
		t.Error("ssh-key add command should have examples with ssh-keygen instructions")
	}
}

func TestResolveCommandNameCanonical(t *testing.T) {
	t.Parallel()

	server := &Server{log: tslog.Slogger(t)}
	ss := &SSHServer{server: server}
	ss.commands = NewCommandTree(ss)

	tests := []struct {
		input []string
		want  string
	}{
		{[]string{"whoami"}, "whoami"},
		{[]string{"ssh-key", "list"}, "ssh-key list"},
		{[]string{"ssh-key", "list", "--json"}, "ssh-key list"},
		// Subcommand aliases resolve to canonical names.
		{[]string{"share", "add-share-link"}, "share add-link"},
		{[]string{"share", "remove-share-link"}, "share remove-link"},
		// Unknown command returns "".
		{[]string{"nonexistent"}, ""},
	}

	for _, tt := range tests {
		got := ss.commands.ResolveCommandName(tt.input)
		if got != tt.want {
			t.Errorf("ResolveCommandName(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// collectCommands walks a command tree and returns all command paths with their commands.
func collectCommands(commands []*exemenu.Command, prefix string) []struct {
	path string
	cmd  *exemenu.Command
} {
	var result []struct {
		path string
		cmd  *exemenu.Command
	}
	for _, cmd := range commands {
		p := cmd.Name
		if prefix != "" {
			p = prefix + " " + cmd.Name
		}
		result = append(result, struct {
			path string
			cmd  *exemenu.Command
		}{path: p, cmd: cmd})
		if len(cmd.Subcommands) > 0 {
			result = append(result, collectCommands(cmd.Subcommands, p)...)
		}
	}
	return result
}

func TestRequiresSudoCommandsMustBeHidden(t *testing.T) {
	ss := &SSHServer{}
	ct := NewCommandTree(ss)

	for _, entry := range collectCommands(ct.Commands, "") {
		if entry.cmd.RequiresSudo && !entry.cmd.Hidden {
			t.Errorf("command %q has RequiresSudo=true but Hidden=false; sudo commands must be hidden", entry.path)
		}
	}
}

// TestRequiresSudoExplicitlySet ensures every command in the real command tree
// has been explicitly considered for RequiresSudo. If you add a new command,
// add it to the appropriate set below.
func TestRequiresSudoExplicitlySet(t *testing.T) {
	ss := &SSHServer{}
	ct := NewCommandTree(ss)

	// Commands that require sudo. Update this when adding new sudo commands.
	wantSudo := map[string]bool{
		"exelets":                 true,
		"resize":                  true,
		"backfill-allocated-cpus": true,
		"sudo-exe":                true,
		"sudo-exe llm-credits":    true,
		"sudo-exe set-limits":     true,
		"team create":             true,
		"team enroll":             true,
		"team unenroll":           true,
		"team promote":            true,
		"team demote":             true,
		"team list":               true,
		"team show":               true,
	}

	allCommands := collectCommands(ct.Commands, "")

	// Check that every command marked RequiresSudo is in our expected set
	for _, entry := range allCommands {
		if entry.cmd.RequiresSudo && !wantSudo[entry.path] {
			t.Errorf("command %q has RequiresSudo=true but is not in the expected sudo set; add it to wantSudo in this test", entry.path)
		}
	}

	// Check that every command in our expected set actually has RequiresSudo
	for _, entry := range allCommands {
		if wantSudo[entry.path] && !entry.cmd.RequiresSudo {
			t.Errorf("command %q is in the expected sudo set but has RequiresSudo=false; set RequiresSudo: true on the command", entry.path)
		}
	}

	// Check that every entry in wantSudo corresponds to a real command
	commandPaths := make(map[string]bool)
	for _, entry := range allCommands {
		commandPaths[entry.path] = true
	}
	for path := range wantSudo {
		if !commandPaths[path] {
			t.Errorf("wantSudo contains %q but no such command exists; remove it from the test", path)
		}
	}
}

func TestValidateCommand_RequiresSudoMustBeHidden(t *testing.T) {
	err := exemenu.ValidateCommand(&exemenu.Command{
		Name:         "bad",
		RequiresSudo: true,
		Hidden:       false,
		Handler:      func(context.Context, *exemenu.CommandContext) error { return nil },
	})
	if err == nil {
		t.Fatal("expected error for RequiresSudo command that is not Hidden")
	}
	if !strings.Contains(err.Error(), "RequiresSudo") {
		t.Errorf("error should mention RequiresSudo, got: %v", err)
	}

	// Valid: RequiresSudo + Hidden
	err = exemenu.ValidateCommand(&exemenu.Command{
		Name:         "good",
		RequiresSudo: true,
		Hidden:       true,
		Handler:      func(context.Context, *exemenu.CommandContext) error { return nil },
	})
	if err != nil {
		t.Errorf("unexpected error for valid RequiresSudo+Hidden command: %v", err)
	}
}

func TestSudoCheckerEnforcement(t *testing.T) {
	sudoCalled := false
	tree := &exemenu.CommandTree{
		Commands: []*exemenu.Command{
			{
				Name:         "admin-cmd",
				Hidden:       true,
				RequiresSudo: true,
				Handler: func(ctx context.Context, cc *exemenu.CommandContext) error {
					cc.Write("executed")
					return nil
				},
			},
			{
				Name: "normal-cmd",
				Handler: func(ctx context.Context, cc *exemenu.CommandContext) error {
					cc.Write("executed")
					return nil
				},
			},
		},
		SudoChecker: func(ctx context.Context, userID string) bool {
			sudoCalled = true
			return userID == "admin"
		},
	}

	// Non-admin user tries sudo command
	output := &MockOutput{}
	cc := &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: "regular", Email: "regular@test.com"},
		Output: output,
	}
	rc := tree.ExecuteCommand(context.Background(), cc, []string{"admin-cmd"})
	if rc == 0 {
		t.Error("non-sudo user should be rejected")
	}
	if !sudoCalled {
		t.Error("SudoChecker should have been called")
	}
	if !strings.Contains(output.String(), "sudoers") {
		t.Errorf("expected sudoers message, got: %s", output.String())
	}

	// Admin user tries sudo command
	sudoCalled = false
	output = &MockOutput{}
	cc = &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: "admin", Email: "admin@test.com"},
		Output: output,
	}
	rc = tree.ExecuteCommand(context.Background(), cc, []string{"admin-cmd"})
	if rc != 0 {
		t.Errorf("admin user should succeed, got rc=%d, output=%s", rc, output.String())
	}
	if !strings.Contains(output.String(), "executed") {
		t.Error("admin command should have executed")
	}

	// Normal command doesn't require sudo check
	sudoCalled = false
	output = &MockOutput{}
	cc = &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: "regular", Email: "regular@test.com"},
		Output: output,
	}
	rc = tree.ExecuteCommand(context.Background(), cc, []string{"normal-cmd"})
	if rc != 0 {
		t.Errorf("normal command should succeed, got rc=%d", rc)
	}
	if sudoCalled {
		t.Error("SudoChecker should not be called for non-sudo commands")
	}
}
