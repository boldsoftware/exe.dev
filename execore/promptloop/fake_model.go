package promptloop

import (
	"context"
	"fmt"
	"sync"
)

// FakeModel is a test double for Model. You push pre-baked responses
// and it returns them in order. It records all requests for assertions.
type FakeModel struct {
	mu        sync.Mutex
	responses []*Response
	Requests  []*Request
	callIdx   int
}

// NewFakeModel creates a FakeModel with the given canned responses.
func NewFakeModel(responses ...*Response) *FakeModel {
	return &FakeModel{responses: responses}
}

func (m *FakeModel) SendMessage(_ context.Context, req *Request) (*Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Requests = append(m.Requests, req)
	if m.callIdx >= len(m.responses) {
		return nil, fmt.Errorf("FakeModel: no more responses (got %d calls, have %d responses)", m.callIdx+1, len(m.responses))
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

// CallCount returns how many times SendMessage was called.
func (m *FakeModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callIdx
}

// TextResponse creates a simple text-only Response that ends the turn.
func TextResponse(text string) *Response {
	return &Response{
		ID:         "msg_fake",
		Type:       "message",
		Role:       RoleAssistant,
		StopReason: "end_turn",
		Content: []ContentBlock{
			{Type: ContentTypeText, Text: text},
		},
	}
}

// ToolUseResponse creates a Response with a single tool call.
func ToolUseResponse(toolID, toolName string, input map[string]string) *Response {
	return &Response{
		ID:         "msg_fake",
		Type:       "message",
		Role:       RoleAssistant,
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: ContentTypeToolUse, ID: toolID, Name: toolName, Input: input},
		},
	}
}

// ToolUseWithTextResponse creates a Response with text and a tool call.
func ToolUseWithTextResponse(text, toolID, toolName string, input map[string]string) *Response {
	return &Response{
		ID:         "msg_fake",
		Type:       "message",
		Role:       RoleAssistant,
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: ContentTypeText, Text: text},
			{Type: ContentTypeToolUse, ID: toolID, Name: toolName, Input: input},
		},
	}
}

// --- Fake helpers for other interfaces ---

// FakeDispatcher is a CommandDispatcher that returns canned output for specific commands.
type FakeDispatcher struct {
	mu        sync.Mutex
	Outputs   map[string]string // command -> output
	ExitCodes map[string]int    // command -> exit code (default 0)
	Calls     []string          // recorded commands
}

func NewFakeDispatcher() *FakeDispatcher {
	return &FakeDispatcher{
		Outputs:   make(map[string]string),
		ExitCodes: make(map[string]int),
	}
}

func (d *FakeDispatcher) Dispatch(_ context.Context, command string) (string, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls = append(d.Calls, command)
	output := d.Outputs[command]
	exitCode := d.ExitCodes[command]
	return output, exitCode
}

// FakeOutput records all output for test assertions.
type FakeOutput struct {
	mu              sync.Mutex
	Texts           []string
	ToolCalls       []string
	ToolResults     []string
	Statuses        []string
	PromptResponses []string // pre-loaded responses to PromptUser
	promptIdx       int
}

func NewFakeOutput(promptResponses ...string) *FakeOutput {
	return &FakeOutput{PromptResponses: promptResponses}
}

func (o *FakeOutput) WriteText(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Texts = append(o.Texts, text)
}

func (o *FakeOutput) WriteToolCall(name, input string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ToolCalls = append(o.ToolCalls, fmt.Sprintf("%s: %s", name, input))
}

func (o *FakeOutput) WriteToolResult(name, result string, isError bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	errStr := ""
	if isError {
		errStr = " [error]"
	}
	o.ToolResults = append(o.ToolResults, fmt.Sprintf("%s%s: %s", name, errStr, result))
}

func (o *FakeOutput) PromptUser(prompt string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.promptIdx >= len(o.PromptResponses) {
		return "", fmt.Errorf("no more prompt responses")
	}
	resp := o.PromptResponses[o.promptIdx]
	o.promptIdx++
	return resp, nil
}

func (o *FakeOutput) WriteStatus(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Statuses = append(o.Statuses, text)
}
