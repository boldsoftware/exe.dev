package server

import (
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestBuildConversationSummary(t *testing.T) {
	// Create a server to get a SubagentRunner
	runner := &SubagentRunner{server: nil} // server not needed for buildConversationSummary

	// Create some mock messages
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello, please do task X"}},
	}
	userMsgJSON, _ := json.Marshal(userMsg)
	userMsgStr := string(userMsgJSON)

	assistantMsg := llm.Message{
		Role:    llm.MessageRoleAssistant,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "I'll start working on task X"}},
	}
	assistantMsgJSON, _ := json.Marshal(assistantMsg)
	assistantMsgStr := string(assistantMsgJSON)

	messages := []generated.Message{
		{
			MessageID: "msg1",
			Type:      "user",
			LlmData:   &userMsgStr,
		},
		{
			MessageID: "msg2",
			Type:      "agent",
			LlmData:   &assistantMsgStr,
		},
	}

	summary := runner.buildConversationSummary(messages)

	// Check that the summary contains expected content
	if summary == "" {
		t.Error("Expected non-empty summary")
	}
	if !strings.Contains(summary, "Hello") {
		t.Error("Summary should contain user message content")
	}
	if !strings.Contains(summary, "task X") {
		t.Error("Summary should contain assistant message content")
	}
}

func TestBuildConversationSummary_ToolUse(t *testing.T) {
	runner := &SubagentRunner{server: nil}

	// Create a message with tool use
	toolUseMsg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolUse,
			ID:        "tool1",
			ToolName:  "bash",
			ToolInput: json.RawMessage(`{"command": "ls -la"}`),
		}},
	}
	toolUseMsgJSON, _ := json.Marshal(toolUseMsg)
	toolUseMsgStr := string(toolUseMsgJSON)

	messages := []generated.Message{
		{
			MessageID: "msg1",
			Type:      "agent",
			LlmData:   &toolUseMsgStr,
		},
	}

	summary := runner.buildConversationSummary(messages)

	// Check that tool use is included
	if !strings.Contains(summary, "bash") {
		t.Error("Summary should contain tool name")
	}
	if !strings.Contains(summary, "ls -la") {
		t.Error("Summary should contain tool input")
	}
}

func TestBuildConversationSummary_Truncation(t *testing.T) {
	runner := &SubagentRunner{server: nil}

	// Create a message with very long content
	longText := make([]byte, 10000)
	for i := range longText {
		longText[i] = 'a'
	}

	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: string(longText)}},
	}
	userMsgJSON, _ := json.Marshal(userMsg)
	userMsgStr := string(userMsgJSON)

	messages := []generated.Message{
		{
			MessageID: "msg1",
			Type:      "user",
			LlmData:   &userMsgStr,
		},
	}

	summary := runner.buildConversationSummary(messages)

	// Check that the summary is truncated
	if !strings.Contains(summary, "[truncated]") {
		t.Error("Expected truncation marker in long message")
	}
}

func TestBuildConversationSummary_ToolResult(t *testing.T) {
	runner := &SubagentRunner{server: nil}

	// Create a message with tool result
	toolResultMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: "tool1",
			ToolError: false,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: "Command output: file1.txt file2.txt",
			}},
		}},
	}
	toolResultMsgJSON, _ := json.Marshal(toolResultMsg)
	toolResultMsgStr := string(toolResultMsgJSON)

	messages := []generated.Message{
		{
			MessageID: "msg1",
			Type:      "user",
			LlmData:   &toolResultMsgStr,
		},
	}

	summary := runner.buildConversationSummary(messages)

	// Check that tool result is included
	if !strings.Contains(summary, "file1.txt") {
		t.Error("Summary should contain tool result content")
	}
}

func TestBuildConversationSummary_ToolError(t *testing.T) {
	runner := &SubagentRunner{server: nil}

	// Create a message with tool error
	toolErrorMsg := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: "tool1",
			ToolError: true,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: "command not found: xyz",
			}},
		}},
	}
	toolErrorMsgJSON, _ := json.Marshal(toolErrorMsg)
	toolErrorMsgStr := string(toolErrorMsgJSON)

	messages := []generated.Message{
		{
			MessageID: "msg1",
			Type:      "user",
			LlmData:   &toolErrorMsgStr,
		},
	}

	summary := runner.buildConversationSummary(messages)

	// Check that error is marked
	if !strings.Contains(summary, "(error)") {
		t.Error("Summary should mark tool errors")
	}
}

func TestBuildConversationSummary_SkipsSystemMessages(t *testing.T) {
	runner := &SubagentRunner{server: nil}

	systemMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "SECRET SYSTEM PROMPT CONTENT"}},
	}
	systemMsgJSON, _ := json.Marshal(systemMsg)
	systemMsgStr := string(systemMsgJSON)

	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Regular user message"}},
	}
	userMsgJSON, _ := json.Marshal(userMsg)
	userMsgStr := string(userMsgJSON)

	messages := []generated.Message{
		{
			MessageID: "sys1",
			Type:      "system",
			LlmData:   &systemMsgStr,
		},
		{
			MessageID: "msg1",
			Type:      "user",
			LlmData:   &userMsgStr,
		},
	}

	summary := runner.buildConversationSummary(messages)

	// System message should be excluded
	if strings.Contains(summary, "SECRET") {
		t.Error("Summary should not include system messages")
	}
	// User message should be included
	if !strings.Contains(summary, "Regular user message") {
		t.Error("Summary should include user messages")
	}
}
