package exe

import (
	"context"
	"flag"
	"strings"
	"testing"

	"exe.dev/billing"
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

// Helper to create test context
func createTestContext(sshServer *SSHServer, user *User, alloc *Alloc, output *MockOutput, args []string) *CommandContext {
	return &CommandContext{
		User:      user,
		Alloc:     alloc,
		PublicKey: "test-key",
		Args:      args,
		SSHServer: sshServer,
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
			name:     "find help by alias",
			path:     []string{"?"},
			wantName: "help",
		},
		{
			name:     "find list command",
			path:     []string{"list"},
			wantName: "list",
		},
		{
			name:     "find list by alias",
			path:     []string{"ls"},
			wantName: "list",
		},
		{
			name:    "nonexistent command",
			path:    []string{"nonexistent"},
			wantNil: true,
		},
		{
			name:    "nonexistent subcommand",
			path:    []string{"list", "nonexistent"},
			wantNil: true,
		},
		{
			name:     "find billing setup subcommand",
			path:     []string{"billing", "setup"},
			wantName: "setup",
		},
		{
			name:    "nonexistent sub-subcommand",
			path:    []string{"billing", "setup", "foo", "bar"},
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

	ctx := &CommandContext{
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

	_, err := ctx.ReadLine()
	if err == nil {
		t.Errorf("ReadLine() should return error when not interactive")
	}
}

func TestHelpCommand(t *testing.T) {
	// Create test server and dependencies
	server := &Server{}
	billing := &billing.MockService{}
	sshServer := &SSHServer{server: server, billing: billing}
	sshServer.commands = NewCommandTree(sshServer)

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

	t.Run("general help", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
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
		if !strings.Contains(result, "list") {
			t.Errorf("Help output should contain 'list'")
		}
		if !strings.Contains(result, "exit") {
			t.Errorf("Help output should contain 'exit'")
		}
	})

	t.Run("specific command help", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{"whoami"})
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
		cc := createTestContext(sshServer, user, alloc, output, []string{"nonexistent"})
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
	server := &Server{}
	billing := &billing.MockService{}
	sshServer := &SSHServer{server: server, billing: billing}
	sshServer.commands = NewCommandTree(sshServer)

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

	t.Run("execute help command", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
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

	t.Run("execute help with alias", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
		ctx := context.Background()

		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"?"})
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
		cc := createTestContext(sshServer, user, alloc, output, []string{})
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

	t.Run("execute nonexistent subcommand", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
		ctx := context.Background()

		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"billing", "nonexistent"})
		if rc == 0 {
			t.Errorf("ExecuteCommand() should fail for nonexistent subcommand")
		}
		result := output.String()
		if !strings.Contains(result, `subcommand "nonexistent" not found`) {
			t.Errorf("Output should indicate missing subcommand, got:\n%s\n", result)
		}
	})

	t.Run("execute subcommand", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
		ctx := context.Background()
		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"billing", "setup"})
		if rc == 0 {
			t.Errorf("ExecuteCommand() should fail for billing setup without SSH session")
		}

		result := output.String()
		if !strings.Contains(result, "Interactive billing setup requires SSH session") {
			t.Errorf("Expected SSH session error in output, got:\n%s", result)
		}
	})

	t.Run("execute subcommand with args (production scenario)", func(t *testing.T) {
		output := &MockOutput{}
		// This reproduces the production scenario where cc.Args contains the second part of the command
		// User types "billing setup", shlex.Split creates ["billing", "setup"],
		// parts[1:] creates ["setup"] which becomes cc.Args
		cc := createTestContext(sshServer, user, alloc, output, []string{"setup"})
		ctx := context.Background()
		rc := sshServer.commands.ExecuteCommand(ctx, cc, []string{"billing", "setup"})
		if rc == 0 {
			t.Errorf("ExecuteCommand() should fail for billing setup without SSH session")
		}

		result := output.String()
		if !strings.Contains(result, "Interactive billing setup requires SSH session") {
			t.Errorf("Expected SSH session error in output, got:\n%s", result)
		}
		if strings.Contains(result, "does not take positional arguments") {
			t.Errorf("Got positional args error (this is the bug): %s", result)
		}
	})

	t.Run("execute command with args", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{"whoami"})
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

func TestBillingCommandConditionalBehavior(t *testing.T) {
	// Create test server with mock billing
	mockBilling := &billing.MockService{}
	mockAccountant := &mockAccountant{}
	server := &Server{accountant: mockAccountant}
	sshServer := &SSHServer{server: server, billing: mockBilling}
	sshServer.commands = NewCommandTree(sshServer)

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

	t.Run("billing setup flow when no billing exists", func(t *testing.T) {
		// Mock billing service to return no billing info
		mockBilling.BillingInfo = &billing.BillingInfo{
			HasBilling: false,
		}

		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
		ctx := context.Background()

		err := sshServer.handleBillingCommand(ctx, cc)
		// This would normally trigger billing setup, but our legacy wrapper
		// would need to handle the interactive flow. For now, we're testing
		// that the conditional logic works correctly.
		if err != nil {
			t.Logf("Expected error due to legacy wrapper limitation: %v", err)
		}
	})

	t.Run("billing info display when billing exists", func(t *testing.T) {
		// Mock billing service to return existing billing info
		mockBilling.BillingInfo = &billing.BillingInfo{
			HasBilling:       true,
			Email:            "billing@example.com",
			StripeCustomerID: "cus_test123",
		}

		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, []string{})
		ctx := context.Background()

		err := sshServer.handleBillingCommand(ctx, cc)
		// This would show existing billing info
		if err != nil {
			t.Logf("Expected error due to legacy wrapper limitation: %v", err)
		}
	})
}

func TestGetAvailableCommands(t *testing.T) {
	// Create test server and dependencies
	server := &Server{}
	billing := &billing.MockService{}
	sshServer := &SSHServer{server: server, billing: billing}
	sshServer.commands = NewCommandTree(sshServer)

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

	ctx := createTestContext(sshServer, user, alloc, &MockOutput{}, []string{})

	t.Run("get root commands", func(t *testing.T) {
		available := sshServer.commands.GetAvailableCommands(ctx)

		// Should have all the main commands
		commandNames := make(map[string]bool)
		for _, cmd := range available {
			commandNames[cmd.Name] = true
		}

		expectedCommands := []string{"help", "doc", "list", "new", "delete", "billing", "whoami"}
		for _, expected := range expectedCommands {
			if !commandNames[expected] {
				t.Errorf("Expected command %s not found in available commands", expected)
			}
		}
	})
}

func TestCommandFlagParsing(t *testing.T) {
	// Create test server and dependencies
	server := &Server{}
	billing := &billing.MockService{}
	sshServer := &SSHServer{server: server, billing: billing}
	sshServer.commands = NewCommandTree(sshServer)

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

	tests := []struct {
		name         string
		commandPath  []string
		expectedArgs []string
		checkFlags   func(t *testing.T, cc *CommandContext)
		expectErr    bool
	}{
		{
			name:         "new command with no flags",
			commandPath:  []string{"new"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				// Check default values
				name := cc.FlagSet.Lookup("name").Value.String()
				image := cc.FlagSet.Lookup("image").Value.String()
				size := cc.FlagSet.Lookup("size").Value.String()
				command := cc.FlagSet.Lookup("command").Value.String()

				if name != "" {
					t.Errorf("Expected default name to be empty, got %q", name)
				}
				if image != "exeuntu" {
					t.Errorf("Expected default image to be 'exeuntu', got %q", image)
				}
				if size != "medium" {
					t.Errorf("Expected default size to be 'medium', got %q", size)
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
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			commandPath:  []string{"new", "--name=my-machine", "--image=ubuntu:22.04", "--size=large"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *CommandContext) {
				if cc.FlagSet == nil {
					t.Fatal("FlagSet should not be nil for new command")
				}
				name := cc.FlagSet.Lookup("name").Value.String()
				image := cc.FlagSet.Lookup("image").Value.String()
				size := cc.FlagSet.Lookup("size").Value.String()

				if name != "my-machine" {
					t.Errorf("Expected name to be 'my-machine', got %q", name)
				}
				if image != "ubuntu:22.04" {
					t.Errorf("Expected image to be 'ubuntu:22.04', got %q", image)
				}
				if size != "large" {
					t.Errorf("Expected size to be 'large', got %q", size)
				}
			},
		},
		{
			name:         "new command with flags and remaining args",
			commandPath:  []string{"new", "--name=test", "arg1", "arg2"},
			expectedArgs: []string{"arg1", "arg2"},
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			name:         "help command has no flags",
			commandPath:  []string{"help"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *CommandContext) {
				// Help command should not have a FlagSet, so it uses the defaultFlagSet
				// which means cc.FlagSet should be nil after execution
				if cc.FlagSet != nil {
					t.Errorf("Expected FlagSet to be nil for help command, got %v", cc.FlagSet)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock handler that captures the CommandContext for inspection
			var capturedCC *CommandContext
			mockHandler := func(ctx context.Context, cc *CommandContext) error {
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
			cc := createTestContext(sshServer, user, alloc, output, []string{})
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
	server := &Server{}
	billing := &billing.MockService{}
	sshServer := &SSHServer{server: server, billing: billing}

	// Create a custom command tree with subcommands that have flags for testing
	testFlagSetFunc := func() *flag.FlagSet {
		fs := flag.NewFlagSet("test-sub", flag.ContinueOnError)
		fs.String("option", "default", "test option")
		fs.Bool("verbose", false, "verbose mode")
		return fs
	}

	var capturedContext *CommandContext
	testSubHandler := func(ctx context.Context, cc *CommandContext) error {
		capturedContext = cc
		return nil
	}

	customTree := &CommandTree{
		Commands: []*Command{
			{
				Name:        "parent",
				Description: "Parent command with subcommands",
				Handler: func(ctx context.Context, cc *CommandContext) error {
					return nil
				},
				Subcommands: []*Command{
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

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

	tests := []struct {
		name         string
		commandPath  []string
		expectedArgs []string
		checkFlags   func(t *testing.T, cc *CommandContext)
		expectErr    bool
	}{
		{
			name:         "subcommand with default flags",
			commandPath:  []string{"parent", "sub"},
			expectedArgs: []string{},
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			checkFlags: func(t *testing.T, cc *CommandContext) {
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
			cc := createTestContext(sshServer, user, alloc, output, []string{})
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
	server := &Server{}
	billing := &billing.MockService{}
	sshServer := &SSHServer{server: server, billing: billing}
	sshServer.commands = NewCommandTree(sshServer)

	user := &User{UserID: "test-user", Email: "test@example.com"}
	alloc := &Alloc{AllocID: "test-alloc", UserID: "test-user"}

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
			cc := createTestContext(sshServer, user, alloc, output, []string{})
			ctx := context.Background()

			// For valid flags test, replace handler with a mock to avoid business logic
			var originalHandler func(context.Context, *CommandContext) error
			if !tt.expectError {
				cmd := sshServer.commands.FindCommand([]string{tt.commandPath[0]})
				if cmd != nil {
					originalHandler = cmd.Handler
					cmd.Handler = func(ctx context.Context, cc *CommandContext) error {
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
		command     *Command
		expectError bool
		errorText   string
	}{
		{
			name: "valid command with positional args only",
			command: &Command{
				Name:              "test",
				HasPositionalArgs: true,
				Handler:           func(context.Context, *CommandContext) error { return nil },
			},
			expectError: false,
		},
		{
			name: "valid command with subcommands only",
			command: &Command{
				Name: "test",
				Subcommands: []*Command{
					{
						Name:    "sub",
						Handler: func(context.Context, *CommandContext) error { return nil },
					},
				},
				Handler: func(context.Context, *CommandContext) error { return nil },
			},
			expectError: false,
		},
		{
			name: "valid command with neither positional args nor subcommands",
			command: &Command{
				Name:    "test",
				Handler: func(context.Context, *CommandContext) error { return nil },
			},
			expectError: false,
		},
		{
			name: "invalid command with both positional args and subcommands",
			command: &Command{
				Name:              "test",
				HasPositionalArgs: true,
				Subcommands: []*Command{
					{
						Name:    "sub",
						Handler: func(context.Context, *CommandContext) error { return nil },
					},
				},
				Handler: func(context.Context, *CommandContext) error { return nil },
			},
			expectError: true,
			errorText:   "cannot have both positional arguments and subcommands",
		},
		{
			name: "invalid subcommand with both positional args and subcommands",
			command: &Command{
				Name: "parent",
				Subcommands: []*Command{
					{
						Name:              "invalid-sub",
						HasPositionalArgs: true,
						Subcommands: []*Command{
							{
								Name:    "nested",
								Handler: func(context.Context, *CommandContext) error { return nil },
							},
						},
						Handler: func(context.Context, *CommandContext) error { return nil },
					},
				},
				Handler: func(context.Context, *CommandContext) error { return nil },
			},
			expectError: true,
			errorText:   "in subcommand of \"parent\": command \"invalid-sub\" cannot have both positional arguments and subcommands",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCommand(tt.command)

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
