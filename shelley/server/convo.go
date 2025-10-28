package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/subpub"
)

var errConversationModelMismatch = errors.New("conversation model mismatch")

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID string
	db             *db.DB
	loop           *loop.Loop
	loopCancel     context.CancelFunc
	mu             sync.Mutex
	lastActivity   time.Time
	modelID        string
	history        []llm.Message
	system         []llm.SystemContent
	recordMessage  loop.MessageRecordFunc
	logger         *slog.Logger
	tools          []*llm.Tool

	subpub *subpub.SubPub[StreamResponse]

	hydrated              bool
	hasConversationEvents bool
}

// NewConversationManager constructs a manager with dependencies but defers hydration until needed.
func NewConversationManager(conversationID string, database *db.DB, baseLogger *slog.Logger, tools []*llm.Tool, recordMessage loop.MessageRecordFunc) *ConversationManager {
	logger := baseLogger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("conversationID", conversationID)

	return &ConversationManager{
		conversationID: conversationID,
		db:             database,
		lastActivity:   time.Now(),
		recordMessage:  recordMessage,
		logger:         logger,
		tools:          append([]*llm.Tool(nil), tools...),
		subpub:         subpub.New[StreamResponse](),
	}
}

// Hydrate loads conversation state from the database, generating a system prompt if missing.
func (cm *ConversationManager) Hydrate(ctx context.Context) error {
	cm.mu.Lock()
	if cm.hydrated {
		cm.lastActivity = time.Now()
		cm.mu.Unlock()
		return nil
	}
	cm.mu.Unlock()

	conversation, err := cm.db.GetConversationByID(ctx, cm.conversationID)
	if err != nil {
		return fmt.Errorf("conversation not found: %w", err)
	}

	var messages []generated.Message
	err = cm.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, cm.conversationID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get conversation history: %w", err)
	}

	if conversation.UserInitiated && !hasSystemMessage(messages) {
		systemMsg, err := cm.createSystemPrompt(ctx)
		if err != nil {
			return err
		}
		if systemMsg != nil {
			messages = append(messages, *systemMsg)
		}
	}

	history, system := cm.partitionMessages(messages)
	cm.mu.Lock()
	cm.history = history
	cm.system = system
	cm.hasConversationEvents = len(history) > 0
	cm.lastActivity = time.Now()
	cm.hydrated = true
	cm.mu.Unlock()

	cm.logSystemPromptState(system, len(messages))

	return nil
}

// AcceptUserMessage enqueues a user message, ensuring the loop is ready first.
func (cm *ConversationManager) AcceptUserMessage(ctx context.Context, service llm.Service, modelID string, message llm.Message) (bool, error) {
	if service == nil {
		return false, fmt.Errorf("llm service is required")
	}

	if err := cm.Hydrate(ctx); err != nil {
		return false, err
	}

	if err := cm.ensureLoop(service, modelID); err != nil {
		return false, err
	}

	cm.mu.Lock()
	isFirst := !cm.hasConversationEvents
	cm.hasConversationEvents = true
	loopInstance := cm.loop
	cm.lastActivity = time.Now()
	cm.mu.Unlock()

	if loopInstance == nil {
		return false, fmt.Errorf("conversation loop not initialized")
	}

	loopInstance.QueueUserMessage(message)

	return isFirst, nil
}

// Touch updates last activity timestamp.
func (cm *ConversationManager) Touch() {
	cm.mu.Lock()
	cm.lastActivity = time.Now()
	cm.mu.Unlock()
}

func hasSystemMessage(messages []generated.Message) bool {
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			return true
		}
	}
	return false
}

func (cm *ConversationManager) createSystemPrompt(ctx context.Context) (*generated.Message, error) {
	systemPrompt, err := GenerateSystemPrompt()
	if err != nil {
		return nil, fmt.Errorf("failed to generate system prompt: %w", err)
	}

	if systemPrompt == "" {
		cm.logger.Info("Skipping empty system prompt generation")
		return nil, nil
	}

	systemMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
	}

	created, err := cm.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: cm.conversationID,
		Type:           db.MessageTypeSystem,
		LLMData:        systemMessage,
		UsageData:      llm.Usage{},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store system prompt: %w", err)
	}

	if err := cm.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationTimestamp(ctx, cm.conversationID)
	}); err != nil {
		cm.logger.Warn("Failed to update conversation timestamp after system prompt", "error", err)
	}

	cm.logger.Info("Stored system prompt", "length", len(systemPrompt))
	return created, nil
}

func (cm *ConversationManager) partitionMessages(messages []generated.Message) ([]llm.Message, []llm.SystemContent) {
	var history []llm.Message
	var system []llm.SystemContent

	for _, msg := range messages {
		llmMsg, err := convertToLLMMessage(msg)
		if err != nil {
			cm.logger.Warn("Failed to convert message to LLM format", "messageID", msg.MessageID, "error", err)
			continue
		}

		if msg.Type == string(db.MessageTypeSystem) {
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && content.Text != "" {
					system = append(system, llm.SystemContent{Type: "text", Text: content.Text})
				}
			}
			continue
		}

		history = append(history, llmMsg)
	}

	return history, system
}

func (cm *ConversationManager) logSystemPromptState(system []llm.SystemContent, messageCount int) {
	if len(system) == 0 {
		cm.logger.Warn("No system prompt found in database", "message_count", messageCount)
		return
	}

	length := 0
	for _, sys := range system {
		length += len(sys.Text)
	}
	cm.logger.Info("Loaded system prompt from database", "system_items", len(system), "total_length", length)
}

func (cm *ConversationManager) ensureLoop(service llm.Service, modelID string) error {
	cm.mu.Lock()
	if cm.loop != nil {
		existingModel := cm.modelID
		cm.mu.Unlock()
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}

	history := append([]llm.Message(nil), cm.history...)
	system := append([]llm.SystemContent(nil), cm.system...)
	recordMessage := cm.recordMessage
	tools := append([]*llm.Tool(nil), cm.tools...)
	logger := cm.logger
	cm.mu.Unlock()

	loopInstance := loop.NewLoop(loop.Config{
		LLM:           service,
		History:       history,
		Tools:         tools,
		RecordMessage: recordMessage,
		Logger:        logger,
		System:        system,
	})

	processCtx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)

	cm.mu.Lock()
	if cm.loop != nil {
		cm.mu.Unlock()
		cancel()
		existingModel := cm.modelID
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}
	cm.loop = loopInstance
	cm.loopCancel = cancel
	cm.modelID = modelID
	cm.history = nil
	cm.system = nil
	cm.mu.Unlock()

	go func() {
		if err := loopInstance.Go(processCtx); err != nil && err != context.DeadlineExceeded {
			if logger != nil {
				logger.Error("Conversation loop stopped", "error", err)
			} else {
				slog.Default().Error("Conversation loop stopped", "error", err)
			}
		}
	}()

	return nil
}

func (cm *ConversationManager) stopLoop() {
	cm.mu.Lock()
	cancel := cm.loopCancel
	cm.loopCancel = nil
	cm.loop = nil
	cm.modelID = ""
	cm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}
