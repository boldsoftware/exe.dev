package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/richardlehane/crock32"
	"shelley.exe.dev/llm"
	"sketch.dev/skribe"
)

type Listener interface {
	// TODO: Content is leaking an anthropic API; should we avoid it?
	// TODO: Where should we include start/end time and usage?
	OnToolCall(ctx context.Context, convo *Convo, toolCallID, toolName string, toolInput json.RawMessage, content llm.Content)
	OnToolResult(ctx context.Context, convo *Convo, toolCallID, toolName string, toolInput json.RawMessage, content llm.Content, result *string, err error)
	OnRequest(ctx context.Context, convo *Convo, requestID string, msg *llm.Message)
	OnResponse(ctx context.Context, convo *Convo, requestID string, msg *llm.Response)
}

type NoopListener struct{}

func (n *NoopListener) OnToolCall(ctx context.Context, convo *Convo, id, toolName string, toolInput json.RawMessage, content llm.Content) {
}

func (n *NoopListener) OnToolResult(ctx context.Context, convo *Convo, id, toolName string, toolInput json.RawMessage, content llm.Content, result *string, err error) {
}

func (n *NoopListener) OnResponse(ctx context.Context, convo *Convo, id string, msg *llm.Response) {
}
func (n *NoopListener) OnRequest(ctx context.Context, convo *Convo, id string, msg *llm.Message) {}

var ErrDoNotRespond = errors.New("do not respond")

// A Convo is a managed conversation with Claude.
// It automatically manages the state of the conversation,
// including appending messages send/received,
// calling tools and sending their results,
// tracking usage, etc.
//
// Exported fields must not be altered concurrently with calling any method on Convo.
// Typical usage is to configure a Convo once before using it.
type Convo struct {
	// ID is a unique ID for the conversation
	ID string
	// Ctx is the context for the entire conversation.
	Ctx context.Context
	// Service is the LLM service to use.
	Service llm.Service
	// Tools are the tools available during the conversation.
	Tools []*llm.Tool
	// SystemPrompt is the system prompt for the conversation.
	SystemPrompt string
	// PromptCaching indicates whether to use Anthropic's prompt caching.
	// See https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching#continuing-a-multi-turn-conversation
	// for the documentation. At request send time, we set the cache_control field on the
	// last message. We also cache the system prompt.
	// Default: true.
	PromptCaching bool
	// ToolUseOnly indicates whether Claude may only use tools during this conversation.
	// TODO: add more fine-grained control over tool use?
	ToolUseOnly bool
	// Parent is the parent conversation, if any.
	// It is non-nil for "subagent" calls.
	// It is set automatically when calling SubConvo,
	// and usually should not be set manually.
	Parent *Convo
	// Budget is the budget for this conversation (and all sub-conversations).
	// The Conversation DOES NOT automatically enforce the budget.
	// It is up to the caller to call OverBudget() as appropriate.
	Budget Budget
	// Hidden indicates that the output of this conversation should be hidden in the UI.
	// This is useful for subconversations that can generate noisy, uninteresting output.
	Hidden bool
	// ExtraData is extra data to make available to all tool calls.
	ExtraData map[string]any

	// messages tracks the messages so far in the conversation.
	messages []llm.Message

	// Listener receives messages being sent.
	Listener Listener

	toolUseCancelMu sync.Mutex
	toolUseCancel   map[string]context.CancelCauseFunc

	// Protects usage. This is used for subconversations (that share part of CumulativeUsage) as well.
	mu *sync.Mutex
	// usage tracks usage for this conversation and all sub-conversations.
	usage *CumulativeUsage
	// lastUsage tracks the usage from the most recent API call
	lastUsage llm.Usage
}

// newConvoID generates a new 8-byte random id.
// The uniqueness/collision requirements here are very low.
// They are not global identifiers,
// just enough to distinguish different convos in a single session.
func newConvoID() string {
	u1 := rand.Uint32()
	s := crock32.Encode(uint64(u1))
	if len(s) < 7 {
		s += strings.Repeat("0", 7-len(s))
	}
	return s[:3] + "-" + s[3:]
}

// New creates a new conversation with Claude with sensible defaults.
// ctx is the context for the entire conversation.
func New(ctx context.Context, srv llm.Service, usage *CumulativeUsage) *Convo {
	id := newConvoID()
	if usage == nil {
		usage = newUsage()
	}
	return &Convo{
		Ctx:           skribe.ContextWithAttr(ctx, slog.String("convo_id", id)),
		Service:       srv,
		PromptCaching: true,
		usage:         usage,
		Listener:      &NoopListener{},
		ID:            id,
		toolUseCancel: map[string]context.CancelCauseFunc{},
		mu:            &sync.Mutex{},
	}
}

// SubConvo creates a sub-conversation with the same configuration as the parent conversation.
// (This propagates context for cancellation, HTTP client, API key, etc.)
// The sub-conversation shares no messages with the parent conversation.
// It does not inherit tools from the parent conversation.
func (c *Convo) SubConvo() *Convo {
	id := newConvoID()
	return &Convo{
		Ctx:           skribe.ContextWithAttr(c.Ctx, slog.String("convo_id", id), slog.String("parent_convo_id", c.ID)),
		Service:       c.Service,
		PromptCaching: c.PromptCaching,
		Parent:        c,
		// For convenience, sub-convo usage shares tool uses map with parent,
		// all other fields separate, propagated in AddResponse
		usage:         newUsageWithSharedToolUses(c.usage),
		mu:            c.mu,
		Listener:      c.Listener,
		ID:            id,
		toolUseCancel: map[string]context.CancelCauseFunc{},
		// Do not copy Budget. Each budget is independent,
		// and OverBudget checks whether any ancestor is over budget.
	}
}

func (c *Convo) SubConvoWithHistory() *Convo {
	id := newConvoID()
	return &Convo{
		Ctx:           skribe.ContextWithAttr(c.Ctx, slog.String("convo_id", id), slog.String("parent_convo_id", c.ID)),
		Service:       c.Service,
		PromptCaching: c.PromptCaching,
		Parent:        c,
		// For convenience, sub-convo usage shares tool uses map with parent,
		// all other fields separate, propagated in AddResponse
		usage:    newUsageWithSharedToolUses(c.usage),
		mu:       c.mu,
		Listener: c.Listener,
		ID:       id,
		// Do not copy Budget. Each budget is independent,
		// and OverBudget checks whether any ancestor is over budget.
		messages: slices.Clone(c.messages),
	}
}

// Depth reports how many "sub-conversations" deep this conversation is.
// That it, it walks up parents until it finds a root.
func (c *Convo) Depth() int {
	x := c
	var depth int
	for x.Parent != nil {
		x = x.Parent
		depth++
	}
	return depth
}

// SendUserTextMessage sends a text message to the LLM in this conversation.
// otherContents contains additional contents to send with the message, usually tool results.
func (c *Convo) SendUserTextMessage(s string, otherContents ...llm.Content) (*llm.Response, error) {
	contents := slices.Clone(otherContents)
	if s != "" {
		contents = append(contents, llm.Content{Type: llm.ContentTypeText, Text: s})
	}
	msg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: contents,
	}
	return c.SendMessage(msg)
}

func (c *Convo) messageRequest(msg llm.Message) *llm.Request {
	system := []llm.SystemContent{}
	if c.SystemPrompt != "" {
		d := llm.SystemContent{Type: "text", Text: c.SystemPrompt}
		if c.PromptCaching {
			d.Cache = true
		}
		system = []llm.SystemContent{d}
	}

	// Claude is happy to return an empty response in response to our Done() call,
	// and, if so, you'll see something like:
	// API request failed with status 400 Bad Request
	// {"type":"error","error":  {"type":"invalid_request_error",
	// "message":"messages.5: all messages must have non-empty content except for the optional final assistant message"}}
	// So, we filter out those empty messages.
	var nonEmptyMessages []llm.Message
	for _, m := range c.messages {
		if len(m.Content) > 0 {
			nonEmptyMessages = append(nonEmptyMessages, m)
		}
	}

	// Also validate the new message being sent - don't add it if empty
	messagesToSend := nonEmptyMessages
	if len(msg.Content) > 0 {
		messagesToSend = append(messagesToSend, msg)
	}

	mr := &llm.Request{
		Messages: messagesToSend,
		System:   system,
		Tools:    c.Tools,
	}
	if c.ToolUseOnly {
		mr.ToolChoice = &llm.ToolChoice{Type: llm.ToolChoiceTypeAny}
	}
	return mr
}

func (c *Convo) findTool(name string) (*llm.Tool, error) {
	for _, tool := range c.Tools {
		if tool.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found", name)
}

// insertMissingToolResults adds error results for tool uses that were requested
// but not included in the message, which can happen in error paths like "out of budget."
// We only insert these if there were no tool responses at all, since an incorrect
// number of tool results would be a programmer error. Mutates inputs.
func (c *Convo) insertMissingToolResults(mr *llm.Request, msg *llm.Message) {
	if len(mr.Messages) < 2 {
		return
	}
	prev := mr.Messages[len(mr.Messages)-2]
	var toolUsePrev int
	for _, c := range prev.Content {
		if c.Type == llm.ContentTypeToolUse {
			toolUsePrev++
		}
	}
	if toolUsePrev == 0 {
		return
	}
	var toolUseCurrent int
	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeToolResult {
			toolUseCurrent++
		}
	}
	if toolUseCurrent != 0 {
		return
	}
	var prefix []llm.Content
	for _, part := range prev.Content {
		if part.Type != llm.ContentTypeToolUse {
			continue
		}
		content := llm.Content{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: part.ID,
			ToolError: true,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: "not executed; retry possible",
			}},
		}
		prefix = append(prefix, content)
	}
	msg.Content = append(prefix, msg.Content...)
	mr.Messages[len(mr.Messages)-1].Content = msg.Content
	slog.DebugContext(c.Ctx, "inserted missing tool results")
}

// SendMessage sends a message to Claude.
// The conversation records (internally) all messages succesfully sent and received.
func (c *Convo) SendMessage(msg llm.Message) (*llm.Response, error) {
	id := ulid.Make().String()
	mr := c.messageRequest(msg)
	var lastMessage *llm.Message
	if c.PromptCaching {
		lastMessage = &mr.Messages[len(mr.Messages)-1]
		if len(lastMessage.Content) > 0 {
			lastMessage.Content[len(lastMessage.Content)-1].Cache = true
		}
	}
	defer func() {
		if lastMessage == nil {
			return
		}
		if len(lastMessage.Content) > 0 {
			lastMessage.Content[len(lastMessage.Content)-1].Cache = false
		}
	}()
	c.insertMissingToolResults(mr, &msg)
	c.Listener.OnRequest(c.Ctx, c, id, &msg)

	startTime := time.Now()
	resp, err := c.Service.Do(c.Ctx, mr)
	if resp != nil {
		resp.StartTime = &startTime
		endTime := time.Now()
		resp.EndTime = &endTime
	}

	if err != nil {
		c.Listener.OnResponse(c.Ctx, c, id, nil)
		return nil, err
	}
	c.messages = append(c.messages, msg, resp.ToMessage())
	// Propagate usage to all ancestors (including us).
	for x := c; x != nil; x = x.Parent {
		x.usage.Add(resp.Usage)
		// Store the most recent usage (only on the current conversation, not ancestors)
		if x == c {
			x.lastUsage = resp.Usage
		}
	}
	c.Listener.OnResponse(c.Ctx, c, id, resp)
	return resp, err
}

type toolCallInfoKeyType string

var toolCallInfoKey toolCallInfoKeyType

type ToolCallInfo struct {
	ToolUseID string
}

func ToolCallInfoFromContext(ctx context.Context) ToolCallInfo {
	v := ctx.Value(toolCallInfoKey)
	i, _ := v.(ToolCallInfo)
	return i
}

func (c *Convo) ToolResultCancelContents(resp *llm.Response) ([]llm.Content, error) {
	if resp.StopReason != llm.StopReasonToolUse {
		return nil, nil
	}
	var toolResults []llm.Content

	for _, part := range resp.Content {
		if part.Type != llm.ContentTypeToolUse {
			continue
		}
		c.incrementToolUse(part.ToolName)

		content := llm.Content{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: part.ID,
		}

		content.ToolError = true
		content.ToolResult = []llm.Content{{
			Type: llm.ContentTypeText,
			Text: "user canceled this tool_use",
		}}
		toolResults = append(toolResults, content)
	}
	return toolResults, nil
}

// GetID returns the conversation ID
func (c *Convo) GetID() string {
	return c.ID
}

func (c *Convo) CancelToolUse(toolUseID string, err error) error {
	c.toolUseCancelMu.Lock()
	defer c.toolUseCancelMu.Unlock()
	cancel, ok := c.toolUseCancel[toolUseID]
	if !ok {
		return fmt.Errorf("cannot cancel %s: no cancel function registered for this tool_use_id. All I have is %+v", toolUseID, c.toolUseCancel)
	}
	delete(c.toolUseCancel, toolUseID)
	cancel(err)
	return nil
}

func (c *Convo) newToolUseContext(ctx context.Context, toolUseID string) (context.Context, context.CancelFunc) {
	c.toolUseCancelMu.Lock()
	defer c.toolUseCancelMu.Unlock()
	ctx, cancel := context.WithCancelCause(ctx)
	c.toolUseCancel[toolUseID] = cancel
	return ctx, func() { c.CancelToolUse(toolUseID, nil) }
}

// ToolResultContents runs all tool uses requested by the response and returns their results.
// Cancelling ctx will cancel any running tool calls.
// The boolean return value indicates whether any of the executed tools should end the turn.
func (c *Convo) ToolResultContents(ctx context.Context, resp *llm.Response) ([]llm.Content, bool, error) {
	if resp.StopReason != llm.StopReasonToolUse {
		return nil, false, nil
	}
	// Extract all tool calls from the response, call the tools, and gather the results.
	var wg sync.WaitGroup
	toolResultC := make(chan llm.Content, len(resp.Content))

	endsTurn := false
	for _, part := range resp.Content {
		if part.Type != llm.ContentTypeToolUse {
			continue
		}
		tool, err := c.findTool(part.ToolName)
		if err == nil && tool.EndsTurn {
			endsTurn = true
		}
		c.incrementToolUse(part.ToolName)
		startTime := time.Now()

		c.Listener.OnToolCall(ctx, c, part.ID, part.ToolName, part.ToolInput, llm.Content{
			Type:             llm.ContentTypeToolUse,
			ToolUseID:        part.ID,
			ToolUseStartTime: &startTime,
		})

		wg.Add(1)
		go func() {
			defer wg.Done()

			content := llm.Content{
				Type:             llm.ContentTypeToolResult,
				ToolUseID:        part.ID,
				ToolUseStartTime: &startTime,
			}
			sendErr := func(err error) {
				// Record end time
				endTime := time.Now()
				content.ToolUseEndTime = &endTime

				content.ToolError = true
				content.ToolResult = []llm.Content{{
					Type: llm.ContentTypeText,
					Text: err.Error(),
				}}
				c.Listener.OnToolResult(ctx, c, part.ID, part.ToolName, part.ToolInput, content, nil, err)
				toolResultC <- content
			}
			sendRes := func(toolOut llm.ToolOut) {
				// Record end time
				endTime := time.Now()
				content.ToolUseEndTime = &endTime

				content.ToolResult = toolOut.LLMContent
				content.Display = toolOut.Display
				var firstText string
				if len(toolOut.LLMContent) > 0 {
					firstText = toolOut.LLMContent[0].Text
				}
				c.Listener.OnToolResult(ctx, c, part.ID, part.ToolName, part.ToolInput, content, &firstText, nil)
				toolResultC <- content
			}

			tool, err := c.findTool(part.ToolName)
			if err != nil {
				sendErr(err)
				return
			}
			// Create a new context for just this tool_use call, and register its
			// cancel function so that it can be canceled individually.
			toolUseCtx, cancel := c.newToolUseContext(ctx, part.ID)
			defer cancel()
			// TODO: move this into newToolUseContext?
			toolUseCtx = context.WithValue(toolUseCtx, toolCallInfoKey, ToolCallInfo{ToolUseID: part.ID})
			toolOut := tool.Run(toolUseCtx, part.ToolInput)
			if errors.Is(toolOut.Error, ErrDoNotRespond) {
				return
			}
			if toolUseCtx.Err() != nil {
				sendErr(context.Cause(toolUseCtx))
				return
			}

			if toolOut.Error != nil {
				sendErr(toolOut.Error)
				return
			}
			sendRes(toolOut)
		}()
	}
	wg.Wait()
	close(toolResultC)
	var toolResults []llm.Content
	for toolResult := range toolResultC {
		toolResults = append(toolResults, toolResult)
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	return toolResults, endsTurn, nil
}

func (c *Convo) incrementToolUse(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.usage.ToolUses[name]++
}

// CumulativeUsage represents cumulative usage across a Convo, including all sub-conversations.
type CumulativeUsage struct {
	StartTime                time.Time      `json:"start_time"`
	Responses                uint64         `json:"messages"` // count of responses
	InputTokens              uint64         `json:"input_tokens"`
	OutputTokens             uint64         `json:"output_tokens"`
	CacheReadInputTokens     uint64         `json:"cache_read_input_tokens"`
	CacheCreationInputTokens uint64         `json:"cache_creation_input_tokens"`
	TotalCostUSD             float64        `json:"total_cost_usd"`
	ToolUses                 map[string]int `json:"tool_uses"` // tool name -> number of uses
}

func newUsage() *CumulativeUsage {
	return &CumulativeUsage{ToolUses: make(map[string]int), StartTime: time.Now()}
}

func newUsageWithSharedToolUses(parent *CumulativeUsage) *CumulativeUsage {
	return &CumulativeUsage{ToolUses: parent.ToolUses, StartTime: time.Now()}
}

func (u *CumulativeUsage) Clone() CumulativeUsage {
	v := *u
	v.ToolUses = maps.Clone(u.ToolUses)
	return v
}

func (c *Convo) CumulativeUsage() CumulativeUsage {
	if c == nil {
		return CumulativeUsage{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage.Clone()
}

// LastUsage returns the usage from the most recent API call
func (c *Convo) LastUsage() llm.Usage {
	if c == nil {
		return llm.Usage{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastUsage
}

func (u *CumulativeUsage) WallTime() time.Duration {
	return time.Since(u.StartTime)
}

func (u *CumulativeUsage) DollarsPerHour() float64 {
	hours := u.WallTime().Hours()
	// Prevent division by very small numbers that could cause issues
	if hours < 1e-6 {
		return 0
	}
	return u.TotalCostUSD / hours
}

func (u *CumulativeUsage) Add(usage llm.Usage) {
	u.Responses++
	u.InputTokens += usage.InputTokens
	u.OutputTokens += usage.OutputTokens
	u.CacheReadInputTokens += usage.CacheReadInputTokens
	u.CacheCreationInputTokens += usage.CacheCreationInputTokens
	u.TotalCostUSD += usage.CostUSD
}

// TotalInputTokens returns the grand total cumulative input tokens in u.
func (u *CumulativeUsage) TotalInputTokens() uint64 {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// Attr returns the cumulative usage as a slog.Attr with key "usage".
func (u CumulativeUsage) Attr() slog.Attr {
	elapsed := time.Since(u.StartTime)
	return slog.Group("usage",
		slog.Duration("wall_time", elapsed),
		slog.Uint64("responses", u.Responses),
		slog.Uint64("input_tokens", u.InputTokens),
		slog.Uint64("output_tokens", u.OutputTokens),
		slog.Uint64("cache_read_input_tokens", u.CacheReadInputTokens),
		slog.Uint64("cache_creation_input_tokens", u.CacheCreationInputTokens),
		slog.Float64("total_cost_usd", u.TotalCostUSD),
		slog.Float64("dollars_per_hour", u.TotalCostUSD/elapsed.Hours()),
		slog.Any("tool_uses", maps.Clone(u.ToolUses)),
	)
}

// A Budget represents the maximum amount of resources that may be spent on a conversation.
// Note that the default (zero) budget is unlimited.
type Budget struct {
	MaxDollars float64 // if > 0, max dollars that may be spent
}

// OverBudget returns an error if the convo (or any of its parents) has exceeded its budget.
// TODO: document parent vs sub budgets, multiple errors, etc, once we know the desired behavior.
func (c *Convo) OverBudget() error {
	for x := c; x != nil; x = x.Parent {
		if err := x.overBudget(); err != nil {
			return err
		}
	}
	return nil
}

// ResetBudget sets the budget to the passed in budget and
// adjusts it by what's been used so far.
func (c *Convo) ResetBudget(budget Budget) {
	c.Budget = budget
	if c.Budget.MaxDollars > 0 {
		c.Budget.MaxDollars += c.CumulativeUsage().TotalCostUSD
	}
}

func (c *Convo) overBudget() error {
	usage := c.CumulativeUsage()
	// TODO: stop before we exceed the budget instead of after?
	var err error
	cont := "Continuing to chat will reset the budget."
	if c.Budget.MaxDollars > 0 && usage.TotalCostUSD >= c.Budget.MaxDollars {
		err = errors.Join(err, fmt.Errorf("$%.2f spent, budget is $%.2f. %s", usage.TotalCostUSD, c.Budget.MaxDollars, cont))
	}
	return err
}

// DebugJSON returns the conversation history as JSON for debugging purposes.
func (c *Convo) DebugJSON() ([]byte, error) {
	return json.MarshalIndent(c.messages, "", "  ")
}
