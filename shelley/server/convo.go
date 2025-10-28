package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/subpub"
)

var errConversationModelMismatch = errors.New("conversation model mismatch")

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID string
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
