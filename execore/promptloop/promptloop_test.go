package promptloop

import (
	"context"
	"strings"
	"testing"
)

func TestSimpleTextResponse(t *testing.T) {
	model := NewFakeModel(
		TextResponse("Hello! I can help you."),
	)
	output := NewFakeOutput("") // empty string = end conversation

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: NewFakeDispatcher(),
		Output:     output,
	}, "hi")
	if err != nil {
		t.Fatal(err)
	}

	if model.CallCount() != 1 {
		t.Fatalf("expected 1 model call, got %d", model.CallCount())
	}
	if len(output.Texts) != 1 || output.Texts[0] != "Hello! I can help you." {
		t.Fatalf("unexpected texts: %v", output.Texts)
	}
}

func TestMultiTurnConversation(t *testing.T) {
	model := NewFakeModel(
		TextResponse("What VM would you like to inspect?"),
		TextResponse("OK, I'll look into that."),
	)
	// First prompt response continues the conversation, second ends it
	output := NewFakeOutput("my-vm", "")

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: NewFakeDispatcher(),
		Output:     output,
	}, "help me debug my VM")
	if err != nil {
		t.Fatal(err)
	}

	if model.CallCount() != 2 {
		t.Fatalf("expected 2 model calls, got %d", model.CallCount())
	}
	if len(output.Texts) != 2 {
		t.Fatalf("expected 2 text outputs, got %d", len(output.Texts))
	}

	// Verify the second request includes the conversation history
	req2 := model.Requests[1]
	if len(req2.Messages) != 3 {
		t.Fatalf("expected 3 messages in second request, got %d", len(req2.Messages))
	}
	if req2.Messages[0].Role != RoleUser {
		t.Fatalf("expected first message to be user, got %s", req2.Messages[0].Role)
	}
	if req2.Messages[1].Role != RoleAssistant {
		t.Fatalf("expected second message to be assistant, got %s", req2.Messages[1].Role)
	}
	if req2.Messages[2].Role != RoleUser {
		t.Fatalf("expected third message to be user, got %s", req2.Messages[2].Role)
	}
}

func TestExeCommandToolCall(t *testing.T) {
	dispatcher := NewFakeDispatcher()
	dispatcher.Outputs["whoami"] = "user@example.com\n"

	model := NewFakeModel(
		ToolUseResponse("tool_1", "exe_command", map[string]string{"command": "whoami"}),
		TextResponse("You are logged in as user@example.com."),
	)
	output := NewFakeOutput("") // end after final text

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: dispatcher,
		Output:     output,
	}, "who am I?")
	if err != nil {
		t.Fatal(err)
	}

	if model.CallCount() != 2 {
		t.Fatalf("expected 2 model calls, got %d", model.CallCount())
	}
	if len(dispatcher.Calls) != 1 || dispatcher.Calls[0] != "whoami" {
		t.Fatalf("unexpected dispatcher calls: %v", dispatcher.Calls)
	}

	// Verify tool result was sent back in messages
	req2 := model.Requests[1]
	lastMsg := req2.Messages[len(req2.Messages)-1]
	if lastMsg.Role != RoleUser {
		t.Fatalf("expected tool results in user message, got %s", lastMsg.Role)
	}
	if len(lastMsg.Content) != 1 || lastMsg.Content[0].Type != ContentTypeToolResult {
		t.Fatalf("expected tool_result content, got %v", lastMsg.Content)
	}
	if lastMsg.Content[0].ToolUseID != "tool_1" {
		t.Fatalf("expected tool_use_id=tool_1, got %s", lastMsg.Content[0].ToolUseID)
	}
}

func TestExeCommandError(t *testing.T) {
	dispatcher := NewFakeDispatcher()
	dispatcher.Outputs["ls nonexistent"] = `"ls nonexistent": command not found: "ls nonexistent"`
	dispatcher.ExitCodes["ls nonexistent"] = 1

	model := NewFakeModel(
		ToolUseResponse("tool_1", "exe_command", map[string]string{"command": "ls nonexistent"}),
		TextResponse("That command failed."),
	)
	output := NewFakeOutput("")

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: dispatcher,
		Output:     output,
	}, "list nonexistent")
	if err != nil {
		t.Fatal(err)
	}

	// Verify tool result was marked as error
	req2 := model.Requests[1]
	lastMsg := req2.Messages[len(req2.Messages)-1]
	if !lastMsg.Content[0].IsError {
		t.Fatal("expected tool result to be marked as error")
	}
}

func TestSuggestCommandApproved(t *testing.T) {
	dispatcher := NewFakeDispatcher()
	dispatcher.Outputs["rm my-vm"] = "VM my-vm deleted.\n"

	model := NewFakeModel(
		ToolUseResponse("tool_1", "suggest_command", map[string]string{
			"command":     "rm my-vm",
			"explanation": "deletes the VM",
		}),
		TextResponse("Done!"),
	)
	// First prompt: approve the command, second: end
	output := NewFakeOutput("y", "")

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: dispatcher,
		Output:     output,
	}, "delete my-vm")
	if err != nil {
		t.Fatal(err)
	}

	if len(dispatcher.Calls) != 1 || dispatcher.Calls[0] != "rm my-vm" {
		t.Fatalf("expected command to be run, got calls: %v", dispatcher.Calls)
	}

	// Check tool result indicates success
	req2 := model.Requests[1]
	lastMsg := req2.Messages[len(req2.Messages)-1]
	resultStr, ok := lastMsg.Content[0].Content.(string)
	if !ok {
		t.Fatal("expected string content in tool result")
	}
	if !strings.Contains(resultStr, "executed successfully") {
		t.Fatalf("expected success message, got: %s", resultStr)
	}
}

func TestSuggestCommandDeclined(t *testing.T) {
	dispatcher := NewFakeDispatcher()

	model := NewFakeModel(
		ToolUseResponse("tool_1", "suggest_command", map[string]string{
			"command":     "rm my-vm",
			"explanation": "deletes the VM",
		}),
		TextResponse("OK, I won't do that."),
	)
	// First prompt: decline, second: end
	output := NewFakeOutput("n", "")

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: dispatcher,
		Output:     output,
	}, "delete my vm")
	if err != nil {
		t.Fatal(err)
	}

	if len(dispatcher.Calls) != 0 {
		t.Fatalf("expected no commands to be run, got: %v", dispatcher.Calls)
	}

	// Check tool result indicates declined
	req2 := model.Requests[1]
	lastMsg := req2.Messages[len(req2.Messages)-1]
	resultStr := lastMsg.Content[0].Content.(string)
	if !strings.Contains(resultStr, "declined") {
		t.Fatalf("expected declined message, got: %s", resultStr)
	}
}

func TestTextAndToolUseInSameResponse(t *testing.T) {
	dispatcher := NewFakeDispatcher()
	dispatcher.Outputs["ls"] = "vm1\nvm2\n"

	model := NewFakeModel(
		ToolUseWithTextResponse("Let me check your VMs.", "tool_1", "exe_command", map[string]string{"command": "ls"}),
		TextResponse("You have 2 VMs."),
	)
	output := NewFakeOutput("")

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: dispatcher,
		Output:     output,
	}, "what VMs do I have?")
	if err != nil {
		t.Fatal(err)
	}

	// Should have 2 text outputs: one from first response, one from second
	if len(output.Texts) != 2 {
		t.Fatalf("expected 2 texts, got %d: %v", len(output.Texts), output.Texts)
	}
	if output.Texts[0] != "Let me check your VMs." {
		t.Fatalf("unexpected first text: %s", output.Texts[0])
	}
}

func TestToolsIncludedInRequest(t *testing.T) {
	model := NewFakeModel(
		TextResponse("hi"),
	)
	output := NewFakeOutput("")

	_ = Run(context.Background(), Config{
		Model:      model,
		Dispatcher: NewFakeDispatcher(),
		Output:     output,
	}, "hello")

	req := model.Requests[0]
	if len(req.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(req.Tools))
	}
	toolNames := make(map[string]bool)
	for _, tool := range req.Tools {
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"exe_command", "suggest_command"} {
		if !toolNames[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestExeCommandBlocksMutatingCommands(t *testing.T) {
	dispatcher := NewFakeDispatcher()

	model := NewFakeModel(
		ToolUseResponse("tool_1", "exe_command", map[string]string{"command": "rm my-vm"}),
		TextResponse("I see, I should have used suggest_command."),
	)
	output := NewFakeOutput("")

	err := Run(context.Background(), Config{
		Model:      model,
		Dispatcher: dispatcher,
		Output:     output,
	}, "delete my-vm")
	if err != nil {
		t.Fatal(err)
	}

	// The dispatcher should NOT have been called — rm is not read-only.
	if len(dispatcher.Calls) != 0 {
		t.Fatalf("expected no dispatcher calls, got: %v", dispatcher.Calls)
	}

	// Verify tool result was sent back as an error.
	req2 := model.Requests[1]
	lastMsg := req2.Messages[len(req2.Messages)-1]
	if !lastMsg.Content[0].IsError {
		t.Fatal("expected tool result to be marked as error")
	}
	resultStr, ok := lastMsg.Content[0].Content.(string)
	if !ok {
		t.Fatal("expected string content in tool result")
	}
	if !strings.Contains(resultStr, "not read-only") {
		t.Fatalf("expected 'not read-only' message, got: %s", resultStr)
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		readOnly bool
	}{
		{"ls", true},
		{"ls -l", true},
		{"help", true},
		{"help new", true},
		{"whoami", true},
		{"doc something", true},
		{"integrations list", true},
		{"integrations setup github", false},
		{"integrations add x", false},
		{"top", false},
		{"ssh-key list", false},
		{"shelley install x", false},
		{"rm my-vm", false},
		{"new", false},
		{"restart my-vm", false},
		{"rename old new", false},
		{"cp src dst", false},
		{"resize my-vm", false},
		{"", false},
		{"  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := isReadOnlyCommand(tt.cmd)
			if got != tt.readOnly {
				t.Errorf("isReadOnlyCommand(%q) = %v, want %v", tt.cmd, got, tt.readOnly)
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	model := NewFakeModel() // no responses needed
	output := NewFakeOutput()

	err := Run(ctx, Config{
		Model:      model,
		Dispatcher: NewFakeDispatcher(),
		Output:     output,
	}, "hello")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
