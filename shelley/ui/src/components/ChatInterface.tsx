import React, { useState, useEffect, useRef } from 'react';
import { Message, Model } from '../types';
import { api } from '../services/api';
import MessageComponent from './Message';
import MessageInput from './MessageInput';

interface ChatInterfaceProps {
  conversationId: string;
  onOpenDrawer: () => void;
}

function ChatInterface({ conversationId, onOpenDrawer }: ChatInterfaceProps) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [models, setModels] = useState<Model[]>([]);
  const [selectedModel, setSelectedModel] = useState<string>('qwen3-coder-fireworks');
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);

  // Load messages and set up streaming
  useEffect(() => {
    loadMessages();
    setupMessageStream();
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
    } catch (err) {
      console.error('Failed to load models:', err);
    }
  };

  const setupMessageStream = () => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close();
    }

    const eventSource = api.createMessageStream(conversationId);
    eventSourceRef.current = eventSource;

    eventSource.onmessage = (event) => {
      try {
        const updatedMessages: Message[] = JSON.parse(event.data);
        setMessages(updatedMessages);
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
      await api.sendMessage(conversationId, {
        message: message.trim(),
        model: selectedModel,
      });
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

    return messages.map((message) => (
      <MessageComponent key={message.message_id} message={message} />
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
          <h1 className="text-lg font-semibold">Shelley</h1>
        </div>
        
        {/* Model selector */}
        <div className="flex items-center space-x-2">
          <select
            value={selectedModel}
            onChange={(e) => setSelectedModel(e.target.value)}
            className="text-sm bg-gray-100 dark:bg-gray-700 border dark:border-gray-600 rounded px-2 py-1 focus:outline-none focus:ring-2 focus:ring-primary"
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
    </div>
  );
}

export default ChatInterface;