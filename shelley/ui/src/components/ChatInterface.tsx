import React, { useState, useEffect, useRef } from 'react';
import { Message, Model, Conversation, StreamResponse } from '../types';
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
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);

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
        <div className="flex items-center justify-center h-full">
          <div className="text-center text-gray-500 dark:text-gray-400">
            <p className="text-lg mb-2">Start a conversation</p>
            <p className="text-sm">Send a message to begin chatting with Shelley</p>
          </div>
        </div>
      );
    }

    // Build tool use map from all messages
    const toolUseMap: Record<string, {name: string, input: any}> = {};
    messages.forEach(message => {
      if (message.llm_data) {
        try {
          const llmData = typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            llmData.Content.forEach((content: any) => {
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
        const hasToolUse = llmData.Content.some((c: any) => c.Type === 5);
        if (!hasToolUse) return false;
        
        const textContent = llmData.Content.filter((c: any) => c.Type === 2).map((c: any) => c.Text).join(' ').trim();
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
    <div className="h-full flex flex-col">
      {/* Header */}
      <div className="bg-white dark:bg-gray-800 border-b dark:border-gray-700 px-4 py-3 flex items-center justify-between">
        <div className="flex items-center space-x-3">
          <button
            onClick={onOpenDrawer}
            className="lg:hidden p-2 rounded-md hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
            aria-label="Open conversations"
          >
            <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
            </svg>
          </button>
          
          <h1 className="text-lg font-semibold truncate" title={currentConversation?.slug || 'Shelley'}>
            {getDisplayTitle()}
          </h1>
        </div>
        
        <div className="flex items-center space-x-2">
          {/* Gear icon for settings */}
          <button
            onClick={() => setShowConfigModal(true)}
            className="p-2 rounded-md hover:bg-gray-100 dark:hover:bg-gray-700 transition-colors"
            aria-label="Settings"
          >
            <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
            </svg>
          </button>
          
          {/* Green + icon in circle for new conversation */}
          <button
            onClick={onNewConversation}
            className="w-8 h-8 bg-green-600 hover:bg-green-700 text-white rounded-full flex items-center justify-center transition-colors"
            aria-label="New conversation"
          >
            <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 4v16m8-8H4" />
            </svg>
          </button>
        </div>
      </div>

      {/* Messages area */}
      <div className="flex-1 overflow-y-auto scrollbar-thin">
        {loading ? (
          <div className="flex items-center justify-center h-full">
            <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
          </div>
        ) : (
          <div className="p-4 space-y-4">
            {renderMessages()}
            <div ref={messagesEndRef} />
          </div>
        )}
      </div>

      {/* Error banner */}
      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 border-t border-red-200 dark:border-red-800 px-4 py-3">
          <div className="flex items-center justify-between">
            <p className="text-red-700 dark:text-red-400 text-sm">{error}</p>
            <button
              onClick={() => setError(null)}
              className="text-red-500 hover:text-red-700 dark:hover:text-red-300"
            >
              <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
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
        <div className="space-y-4">
          <div>
            <label htmlFor="model-select" className="block text-sm font-medium mb-2">
              Model
            </label>
            <select
              id="model-select"
              value={selectedModel}
              onChange={(e) => setSelectedModel(e.target.value)}
              className="w-full bg-gray-100 dark:bg-gray-700 border dark:border-gray-600 rounded px-3 py-2 focus:outline-none focus:ring-2 focus:ring-primary"
              disabled={sending}
            >
              {models.map((model) => (
                <option key={model.id} value={model.id} disabled={!model.ready}>
                  {model.id} {!model.ready ? '(not ready)' : ''}
                </option>
              ))}
            </select>
          </div>
        </div>
      </Modal>
    </div>
  );
}

export default ChatInterface;
