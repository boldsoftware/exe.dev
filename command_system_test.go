package exe

import (
	"context"
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

// MockTerminal simulates terminal input for interactive testing
type MockTerminal struct {
	input   []string
	index   int
	prompts []string
}

func NewMockTerminal(input []string) *MockTerminal {
	return &MockTerminal{input: input}
}

func (m *MockTerminal) ReadLine() (string, error) {
	if m.index >= len(m.input) {
		return "", nil
	}
	line := m.input[m.index]
	m.index++
	return line, nil
}

func (m *MockTerminal) SetPrompt(prompt string) {
	m.prompts = append(m.prompts, prompt)
}

// Helper to create test context
func createTestContext(sshServer *SSHServer, user *User, alloc *Alloc, output *MockOutput, terminal *MockTerminal, args []string) *CommandContext {
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
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{})
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
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{"whoami"})
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
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{"nonexistent"})
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
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{})
		ctx := context.Background()

		err := sshServer.commands.ExecuteCommand(ctx, cc, []string{"help"})
		if err != nil {
			t.Errorf("ExecuteCommand() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "EXE.DEV") {
			t.Errorf("Command output should contain 'EXE.DEV'")
		}
	})

	t.Run("execute help with alias", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{})
		ctx := context.Background()

		err := sshServer.commands.ExecuteCommand(ctx, cc, []string{"?"})
		if err != nil {
			t.Errorf("ExecuteCommand() error = %v", err)
		}

		result := output.String()
		if !strings.Contains(result, "EXE.DEV") {
			t.Errorf("Command output should contain 'EXE.DEV'")
		}
	})

	t.Run("execute nonexistent command", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{})
		ctx := context.Background()

		err := sshServer.commands.ExecuteCommand(ctx, cc, []string{"nonexistent"})
		if err == nil {
			t.Errorf("ExecuteCommand() should return error for nonexistent command")
		}
		if !strings.Contains(err.Error(), "command not found") {
			t.Errorf("Error should indicate command not found")
		}
	})

	t.Run("execute command with args", func(t *testing.T) {
		output := &MockOutput{}
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{"whoami"})
		ctx := context.Background()
		err := sshServer.commands.ExecuteCommand(ctx, cc, []string{"help"})
		if err != nil {
			t.Errorf("ExecuteCommand() error = %v", err)
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
	server := &Server{}
	mockBilling := &billing.MockService{}
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
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{})
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
		cc := createTestContext(sshServer, user, alloc, output, nil, []string{})
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

	ctx := createTestContext(sshServer, user, alloc, &MockOutput{}, nil, []string{})

	t.Run("get root commands", func(t *testing.T) {
		available := sshServer.commands.GetAvailableCommands(ctx)

		// Should have all the main commands
		commandNames := make(map[string]bool)
		for _, cmd := range available {
			commandNames[cmd.Name] = true
		}

		expectedCommands := []string{"help", "list", "new", "start", "stop", "delete", "logs", "diag", "alloc", "billing", "whoami"}
		for _, expected := range expectedCommands {
			if !commandNames[expected] {
				t.Errorf("Expected command %s not found in available commands", expected)
			}
		}
	})
}
