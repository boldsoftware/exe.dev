import React, { useState, useEffect, useRef } from 'react';
import { Message, Model, Conversation, StreamResponse, LLMContent } from '../types';
import { api } from '../services/api';
import MessageComponent from './Message';
import MessageInput from './MessageInput';
import Modal from './Modal';
import BashTool from './BashTool';
import PatchTool from './PatchTool';

interface CoalescedToolCallProps {
  toolName: string;
  toolInput?: unknown;
  toolResult?: LLMContent[];
  toolError?: boolean;
  toolStartTime?: string | null;
  toolEndTime?: string | null;
  hasResult?: boolean;
}

function CoalescedToolCall({ 
  toolName, 
  toolInput, 
  toolResult, 
  toolError, 
  toolStartTime, 
  toolEndTime, 
  hasResult 
}: CoalescedToolCallProps) {
  // Calculate execution time if available
  let executionTime = '';
  if (hasResult && toolStartTime && toolEndTime) {
    const start = new Date(toolStartTime).getTime();
    const end = new Date(toolEndTime).getTime();
    const diffMs = end - start;
    if (diffMs < 1000) {
      executionTime = `${diffMs}ms`;
    } else {
      executionTime = `${(diffMs / 1000).toFixed(1)}s`;
    }
  }

  // Use specialized BashTool component for bash
  if (toolName === 'bash') {
    return (
      <BashTool
        toolInput={toolInput}
        isRunning={!hasResult}
        toolResult={toolResult}
        hasError={toolError}
        executionTime={executionTime}
      />
    );
  }

  // Use specialized PatchTool component for patch
  if (toolName === 'patch') {
    return (
      <PatchTool
        toolInput={toolInput}
        isRunning={!hasResult}
        toolResult={toolResult}
        hasError={toolError}
        executionTime={executionTime}
      />
    );
  }

  const getToolResultSummary = (results: LLMContent[]) => {
    if (!results || results.length === 0) return 'No output';
    
    const firstResult = results[0];
    if (firstResult.Type === 2 && firstResult.Text) { // text content
      const text = firstResult.Text.trim();
      if (text.length <= 50) return text;
      return text.substring(0, 47) + '...';
    }
    
    return `${results.length} result${results.length > 1 ? 's' : ''}`;
  };

  const renderContent = (content: LLMContent) => {
    if (content.Type === 2) { // text
      return (
        <div className="whitespace-pre-wrap break-words">
          {content.Text || ''}
        </div>
      );
    }
    return (
      <div className="text-secondary text-sm italic">
        [Content type {content.Type}]
      </div>
    );
  };

  if (!hasResult) {
    // Show "running" state
    return (
      <div className="message message-tool" data-testid="tool-call-running">
        <div className="message-content">
          <div className="tool-running">
            <div className="tool-running-header">
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem', color: 'var(--blue-text)'}}>
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
              </svg>
              <span className="tool-name">
                Tool: {toolName}
              </span>
              <span className="tool-status-running">(running)</span>
            </div>
            <div className="tool-input">
              {typeof toolInput === 'string' 
                ? toolInput 
                : JSON.stringify(toolInput, null, 2)
              }
            </div>
          </div>
        </div>
      </div>
    );
  }

  // Show completed state with result
  const summary = toolResult ? getToolResultSummary(toolResult) : 'No output';

  return (
    <div className="message message-tool" data-testid="tool-call-completed">
      <div className="message-content">
        <details className={`tool-result-details ${toolError ? 'error' : ''}`}>
          <summary className="tool-result-summary">
            <div className="tool-result-meta">
              <div className="flex items-center space-x-2">
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem', color: 'var(--blue-text)'}}>
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                </svg>
                <span className="text-sm font-medium text-blue">
                  {toolName}
                </span>
                <span className={`tool-result-status text-xs ${toolError ? 'error' : 'success'}`}>
                  {toolError ? '✗' : '✓'} {summary}
                </span>
              </div>
              <div className="tool-result-time">
                {executionTime && <span>{executionTime}</span>}
              </div>
            </div>
          </summary>
          <div className="tool-result-content">
            {/* Show tool input */}
            <div className="tool-result-section">
              <div className="tool-result-label">Input:</div>
              <div className="tool-result-data">
                {toolInput ? (
                  typeof toolInput === 'string' 
                    ? toolInput 
                    : JSON.stringify(toolInput, null, 2)
                ) : (
                  <span className="text-secondary italic">No input data</span>
                )}
              </div>
            </div>
            
            {/* Show tool output with header */}
            <div className={`tool-result-section output ${toolError ? 'error' : ''}`}>
              <div className="tool-result-label">Output{toolError ? ' (Error)' : ''}:</div>
              <div className="space-y-2">
                {toolResult?.map((result, idx) => (
                  <div key={idx}>
                    {renderContent(result)}
                  </div>
                ))}
              </div>
            </div>
          </div>
        </details>
      </div>
    </div>
  );
}


interface ChatInterfaceProps {
  conversationId: string | null;
  onOpenDrawer: () => void;
  onNewConversation: () => void;
  currentConversation?: Conversation;
  onConversationUpdate?: (conversation: Conversation) => void;
  onFirstMessage?: (message: string, model: string) => Promise<void>;
}

function ChatInterface({ conversationId, onOpenDrawer, onNewConversation, currentConversation, onConversationUpdate, onFirstMessage }: ChatInterfaceProps) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [models, setModels] = useState<Model[]>([]);
  const [selectedModel, setSelectedModel] = useState<string>('qwen3-coder-fireworks');
  const [showConfigModal, setShowConfigModal] = useState(false);
  const [showOverflowMenu, setShowOverflowMenu] = useState(false);
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const [reconnectAttempts, setReconnectAttempts] = useState(0);
  const [isDisconnected, setIsDisconnected] = useState(false);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);
  const overflowMenuRef = useRef<HTMLDivElement>(null);
  const reconnectTimeoutRef = useRef<number | null>(null);

  // Load messages and set up streaming
  useEffect(() => {
    if (conversationId) {
      loadMessages();
      setupMessageStream();
    } else {
      // No conversation yet, show empty state
      setMessages([]);
      setLoading(false);
    }
    loadModels();

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current);
      }
    };
  }, [conversationId]);

  // Auto-scroll to bottom when new messages arrive
  useEffect(() => {
    scrollToBottom();
  }, [messages]);

  // Close overflow menu when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (overflowMenuRef.current && !overflowMenuRef.current.contains(event.target as Node)) {
        setShowOverflowMenu(false);
      }
    };

    if (showOverflowMenu) {
      document.addEventListener('mousedown', handleClickOutside);
      return () => {
        document.removeEventListener('mousedown', handleClickOutside);
      };
    }
  }, [showOverflowMenu]);

  const loadMessages = async () => {
    if (!conversationId) return;
    try {
      setLoading(true);
      setError(null);
      const msgs = await api.getMessages(conversationId);
      setMessages(msgs);
    } catch (err) {
      console.error('Failed to load messages:', err);
      setError('Failed to load messages');
    } finally {
      // Always set loading to false, even if other operations fail
      setLoading(false);
    }
  };

  const loadModels = async () => {
    try {
      const modelList = await api.getModels();
      setModels(modelList);
      // Prefer first ready model; fallback stays as-is
      const firstReady = modelList.find((m) => m.ready);
      if (firstReady) setSelectedModel(firstReady.id);
    } catch (err) {
      console.error('Failed to load models:', err);
    }
  };

  const setupMessageStream = () => {
    if (!conversationId) return;
    
    if (eventSourceRef.current) {
      eventSourceRef.current.close();
    }

    const eventSource = api.createMessageStream(conversationId);
    eventSourceRef.current = eventSource;

    eventSource.onmessage = (event) => {
      try {
        const streamResponse: StreamResponse = JSON.parse(event.data);

        // Merge new messages without losing existing ones.
        // If no new messages (e.g., only conversation/slug update), keep existing list.
        if (Array.isArray(streamResponse.messages) && streamResponse.messages.length > 0) {
          setMessages((prev) => {
            const byId = new Map<string, Message>();
            for (const m of prev) byId.set(m.message_id, m);
            for (const m of streamResponse.messages) byId.set(m.message_id, m);
            // Preserve original order, then append truly new ones in the order received
            const result: Message[] = [];
            for (const m of prev) result.push(byId.get(m.message_id)!);
            for (const m of streamResponse.messages) {
              if (!prev.find((p) => p.message_id === m.message_id)) result.push(m);
            }
            return result;
          });
        }
        
        // Update conversation data if provided
        if (onConversationUpdate) {
          onConversationUpdate(streamResponse.conversation);
        }
      } catch (err) {
        console.error('Failed to parse message stream data:', err);
      }
    };

    eventSource.onerror = (event) => {
      console.warn('Message stream error (will retry):', event);
      // Close and retry after a delay
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
        eventSourceRef.current = null;
      }
      
      // Backoff delays: 1s, 5s, 10s, then give up
      const delays = [1000, 5000, 10000];
      
      setReconnectAttempts((prev) => {
        const attempts = prev + 1;
        
        if (attempts > delays.length) {
          // Give up and show disconnected UI
          setIsDisconnected(true);
          return attempts;
        }
        
        const delay = delays[attempts - 1];
        console.log(`Reconnecting in ${delay}ms (attempt ${attempts}/${delays.length})`);
        
        reconnectTimeoutRef.current = window.setTimeout(() => {
          if (eventSourceRef.current === null) {
            setupMessageStream();
          }
        }, delay);
        
        return attempts;
      });
    };

    eventSource.onopen = () => {
      console.log('Message stream connected');
      // Reset reconnect attempts on successful connection
      setReconnectAttempts(0);
      setIsDisconnected(false);
    };
  };

  const sendMessage = async (message: string) => {
    if (!message.trim() || sending) return;

    try {
      setSending(true);
      setError(null);
      
      // If no conversation ID, this is the first message
      if (!conversationId && onFirstMessage) {
        await onFirstMessage(message.trim(), selectedModel);
      } else if (conversationId) {
        await api.sendMessage(conversationId, {
          message: message.trim(),
          model: selectedModel,
        });
      }
    } catch (err) {
      console.error('Failed to send message:', err);
      setError('Failed to send message. Please try again.');
    } finally {
      setSending(false);
    }
  };

  const scrollToBottom = () => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  };

  const handleManualReconnect = () => {
    setIsDisconnected(false);
    setReconnectAttempts(0);
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current);
      reconnectTimeoutRef.current = null;
    }
    setupMessageStream();
  };

  const getDisplayTitle = () => {
    if (currentConversation?.slug) {
      // Truncate if too long (more than ~30 characters)
      const slug = currentConversation.slug;
      return slug.length > 30 ? `${slug.substring(0, 27)}...` : slug;
    }
    return 'Shelley';
  };

  // Process messages to coalesce tool calls
  const processMessages = () => {
    if (messages.length === 0) {
      return [];
    }

    interface CoalescedItem {
      type: 'message' | 'tool';
      message?: Message;
      toolUseId?: string;
      toolName?: string;
      toolInput?: unknown;
      toolResult?: LLMContent[];
      toolError?: boolean;
      toolStartTime?: string | null;
      toolEndTime?: string | null;
      hasResult?: boolean;
    }

    const coalescedItems: CoalescedItem[] = [];
    const toolResultMap: Record<string, {
      result: LLMContent[];
      error: boolean;
      startTime: string | null;
      endTime: string | null;
    }> = {};

    // First pass: collect all tool results
    messages.forEach(message => {
      if (message.llm_data) {
        try {
          const llmData = typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            llmData.Content.forEach((content: LLMContent) => {
              if (content && content.Type === 6 && content.ToolUseID) { // tool_result
                toolResultMap[content.ToolUseID] = {
                  result: content.ToolResult || [],
                  error: content.ToolError || false,
                  startTime: content.ToolUseStartTime || null,
                  endTime: content.ToolUseEndTime || null
                };
              }
            });
          }
        } catch (err) {
          console.error('Failed to parse message LLM data for tool results:', err);
        }
      }
    });

    // Second pass: process messages and extract tool uses
    messages.forEach(message => {
      // Skip system messages
      if (message.type === 'system') {
        return;
      }

      if (message.type === 'error') {
        coalescedItems.push({ type: 'message', message });
        return;
      }

      // Check if this is a user message with tool results (skip rendering them as messages)
      let hasToolResult = false;
      if (message.llm_data) {
        try {
          const llmData = typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            hasToolResult = llmData.Content.some((c: LLMContent) => c.Type === 6);
          }
        } catch (err) {
          console.error('Failed to parse message LLM data:', err);
        }
      }

      // If it's a user message without tool results, show it
      if (message.type === 'user' && !hasToolResult) {
        coalescedItems.push({ type: 'message', message });
        return;
      }

      // If it's a user message with tool results, skip it (we'll handle it via the toolResultMap)
      if (message.type === 'user' && hasToolResult) {
        return;
      }

      if (message.llm_data) {
        try {
          const llmData = typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            // Extract text content and tool uses separately
            const textContents: LLMContent[] = [];
            const toolUses: LLMContent[] = [];

            llmData.Content.forEach((content: LLMContent) => {
              if (content.Type === 2) { // text
                textContents.push(content);
              } else if (content.Type === 5) { // tool_use
                toolUses.push(content);
              }
            });

            // If we have text content, add it as a message (but only if it's not empty)
            const textString = textContents.map(c => c.Text || '').join('').trim();
            if (textString) {
              coalescedItems.push({ type: 'message', message });
            }

            // Add tool uses as separate items
            toolUses.forEach(toolUse => {
              const resultData = toolUse.ID ? toolResultMap[toolUse.ID] : undefined;
              coalescedItems.push({
                type: 'tool',
                toolUseId: toolUse.ID,
                toolName: toolUse.ToolName,
                toolInput: toolUse.ToolInput,
                toolResult: resultData?.result,
                toolError: resultData?.error,
                toolStartTime: resultData?.startTime,
                toolEndTime: resultData?.endTime,
                hasResult: !!resultData
              });
            });
          }
        } catch (err) {
          console.error('Failed to parse message LLM data:', err);
          coalescedItems.push({ type: 'message', message });
        }
      } else {
        coalescedItems.push({ type: 'message', message });
      }
    });

    return coalescedItems;
  };

  const renderMessages = () => {
    if (messages.length === 0) {
      return (
        <div className="empty-state">
          <div className="empty-state-content">
            <p className="text-lg" style={{marginBottom: '0.5rem'}}>Start a conversation</p>
            <p className="text-sm">Send a message to begin chatting with Shelley</p>
          </div>
        </div>
      );
    }

    const coalescedItems = processMessages();

    return coalescedItems.map((item, index) => {
      if (item.type === 'message' && item.message) {
        return (
          <MessageComponent 
            key={item.message.message_id} 
            message={item.message}
          />
        );
      } else if (item.type === 'tool') {
        return (
          <CoalescedToolCall
            key={item.toolUseId || `tool-${index}`}
            toolName={item.toolName || 'Unknown Tool'}
            toolInput={item.toolInput}
            toolResult={item.toolResult}
            toolError={item.toolError}
            toolStartTime={item.toolStartTime}
            toolEndTime={item.toolEndTime}
            hasResult={item.hasResult}
          />
        );
      }
      return null;
    });
  };

  return (
    <div className="full-height flex flex-col">
      {/* Header */}
      <div className="header">
        <div className="flex items-center space-x-3">
          <button
            onClick={onOpenDrawer}
            className="btn-icon hide-on-desktop"
            aria-label="Open conversations"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
            </svg>
          </button>
          
          <h1 className="header-title" title={currentConversation?.slug || 'Shelley'}>
            {getDisplayTitle()}
          </h1>
        </div>
        
        <div className="header-actions">
          {/* Green + icon in circle for new conversation */}
          <button
            onClick={onNewConversation}
            className="btn-new"
            aria-label="New conversation"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem'}}>
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 4v16m8-8H4" />
            </svg>
          </button>
          
          {/* Overflow menu */}
          <div ref={overflowMenuRef} style={{position: 'relative'}}>
            <button
              onClick={() => setShowOverflowMenu(!showOverflowMenu)}
              className="btn-icon"
              aria-label="More options"
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 5v.01M12 12v.01M12 19v.01M12 6a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2z" />
              </svg>
            </button>
            
            {showOverflowMenu && (
              <div className="overflow-menu">
                <button
                  onClick={() => {
                    setShowOverflowMenu(false);
                    setShowConfigModal(true);
                  }}
                  className="overflow-menu-item"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1.25rem', height: '1.25rem', marginRight: '0.75rem'}}>
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                  </svg>
                  Settings
                </button>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Messages area */}
      <div className="messages-container scrollable">
        {loading ? (
          <div className="flex items-center justify-center full-height">
            <div className="spinner"></div>
          </div>
        ) : (
          <div className="messages-list">
            {renderMessages()}
            <div ref={messagesEndRef} />
          </div>
        )}
      </div>

      {/* Disconnect banner */}
      {isDisconnected && (
        <div className="disconnect-banner">
          <div className="disconnect-banner-content">
            <p className="disconnect-message">Disconnected</p>
            <button
              onClick={handleManualReconnect}
              className="btn-reconnect"
            >
              Retry
            </button>
          </div>
        </div>
      )}

      {/* Error banner */}
      {error && (
        <div className="error-banner">
          <div className="error-banner-content">
            <p className="error-message">{error}</p>
            <button
              onClick={() => setError(null)}
              className="btn-icon"
              style={{color: 'var(--error-text)'}}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem'}}>
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>
          </div>
        </div>
      )}

      {/* Message input */}
      <MessageInput onSend={sendMessage} disabled={sending || loading} />
      
      {/* Configuration Modal */}
      <Modal
        isOpen={showConfigModal}
        onClose={() => setShowConfigModal(false)}
        title="Configuration"
      >
        <div>
          <label htmlFor="model-select">
            Model
          </label>
          <select
            id="model-select"
            value={selectedModel}
            onChange={(e) => setSelectedModel(e.target.value)}
            disabled={sending}
          >
            {models.map((model) => (
              <option key={model.id} value={model.id} disabled={!model.ready}>
                {model.id} {!model.ready ? '(not ready)' : ''}
              </option>
            ))}
          </select>
        </div>
      </Modal>
    </div>
  );
}

export default ChatInterface;
