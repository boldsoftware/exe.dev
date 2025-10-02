import React, { useState, useEffect, useRef } from 'react';
import { Message, Model, Conversation, StreamResponse, LLMContent } from '../types';
import { api } from '../services/api';
import MessageComponent from './Message';
import MessageInput from './MessageInput';
import Modal from './Modal';

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
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);
  const overflowMenuRef = useRef<HTMLDivElement>(null);

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
      
      // Retry after 2 seconds
      setTimeout(() => {
        if (eventSourceRef.current === null) {
          setupMessageStream();
        }
      }, 2000);
    };

    eventSource.onopen = () => {
      console.log('Message stream connected');
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

  const getDisplayTitle = () => {
    if (currentConversation?.slug) {
      // Truncate if too long (more than ~30 characters)
      const slug = currentConversation.slug;
      return slug.length > 30 ? `${slug.substring(0, 27)}...` : slug;
    }
    return 'Shelley';
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

    // Build tool use map from all messages
    const toolUseMap: Record<string, {name: string, input: unknown}> = {};
    messages.forEach(message => {
      if (message.llm_data) {
        try {
          const llmData = typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            llmData.Content.forEach((content: LLMContent) => {
              if (content && content.Type === 5 && content.ID && content.ToolName) { // tool_use
                toolUseMap[content.ID] = {
                  name: content.ToolName,
                  input: content.ToolInput
                };
              }
            });
          }
        } catch (err) {
          console.error('Failed to parse message LLM data for tool mapping:', err);
        }
      }
    });

    // Filter out agent messages that only contain tool use + generic text
    const shouldHideMessage = (message: Message) => {
      if (message.type !== 'agent') return false;
      
      try {
        const llmData = message.llm_data ? (typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data) : null;
        if (!llmData?.Content) return false;
        
        // Check if message only contains:
        // 1. Generic "I'll use the X tool now" text
        // 2. Tool use content
        const hasToolUse = llmData.Content.some((c: LLMContent) => c.Type === 5);
        if (!hasToolUse) return false;
        
        const textContent = llmData.Content.filter((c: LLMContent) => c.Type === 2).map((c: LLMContent) => c.Text).join(' ').trim();
        const isGenericToolText = textContent.match(/^I'll use the \w+ tool now\.?$/);
        
        return isGenericToolText !== null;
      } catch (err) {
        console.error('Failed to parse message for hiding logic:', err);
        return false;
      }
    };

    return messages
      .filter(message => !shouldHideMessage(message))
      .map((message) => (
        <MessageComponent 
          key={message.message_id} 
          message={message} 
          toolUseMap={toolUseMap}
        />
      ));
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
