import React, { useState, useEffect } from 'react';
import ChatInterface from './components/ChatInterface';
import ConversationDrawer from './components/ConversationDrawer';
import { Conversation } from './types';
import { api } from './services/api';

function App() {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [currentConversationId, setCurrentConversationId] = useState<string | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Load conversations on mount
  useEffect(() => {
    loadConversations();
  }, []);

  const loadConversations = async () => {
    try {
      setLoading(true);
      setError(null);
      const convs = await api.getConversations();
      setConversations(convs);
      
      // If no current conversation, create a new one or select the first
      if (!currentConversationId && convs.length === 0) {
        await createNewConversation();
      } else if (!currentConversationId && convs.length > 0) {
        setCurrentConversationId(convs[0].conversation_id);
      }
    } catch (err) {
      console.error('Failed to load conversations:', err);
      setError('Failed to load conversations. Please refresh the page.');
    } finally {
      setLoading(false);
    }
  };

  const createNewConversation = async () => {
    try {
      const newConv = await api.createConversation();
      setConversations(prev => [newConv, ...prev]);
      setCurrentConversationId(newConv.conversation_id);
      setDrawerOpen(false);
    } catch (err) {
      console.error('Failed to create conversation:', err);
      setError('Failed to create new conversation');
    }
  };

  const selectConversation = (conversationId: string) => {
    setCurrentConversationId(conversationId);
    setDrawerOpen(false);
  };

  const updateConversation = (updatedConversation: Conversation) => {
    setConversations(prev => 
      prev.map(conv => 
        conv.conversation_id === updatedConversation.conversation_id 
          ? updatedConversation 
          : conv
      )
    );
  };

  if (loading && conversations.length === 0) {
    return (
      <div className="h-screen flex items-center justify-center">
        <div className="text-center">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary mx-auto mb-4"></div>
          <p className="text-gray-600 dark:text-gray-400">Loading...</p>
        </div>
      </div>
    );
  }

  if (error && conversations.length === 0) {
    return (
      <div className="h-screen flex items-center justify-center">
        <div className="text-center">
          <p className="text-red-600 dark:text-red-400 mb-4">{error}</p>
          <button
            onClick={loadConversations}
            className="px-4 py-2 bg-primary text-white rounded-lg hover:bg-primary-dark transition-colors"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  const currentConversation = conversations.find(conv => conv.conversation_id === currentConversationId);

  return (
    <div className="h-screen bg-gray-50 dark:bg-gray-900 flex relative">
      {/* Conversations drawer */}
      <ConversationDrawer
        isOpen={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        conversations={conversations}
        currentConversationId={currentConversationId}
        onSelectConversation={selectConversation}
        onNewConversation={createNewConversation}
      />

      {/* Main chat interface */}
      <div className="flex-1 flex flex-col">
        {currentConversationId ? (
          <ChatInterface
            conversationId={currentConversationId}
            onOpenDrawer={() => setDrawerOpen(true)}
            onNewConversation={createNewConversation}
            currentConversation={currentConversation}
            onConversationUpdate={updateConversation}
          />
        ) : (
          <div className="h-full flex items-center justify-center">
            <div className="text-center">
              <h2 className="text-xl font-semibold mb-4">Welcome to Shelley</h2>
              <p className="text-gray-600 dark:text-gray-400 mb-6">Start a new conversation to get started</p>
              <button
                onClick={createNewConversation}
                className="px-6 py-3 bg-primary text-white rounded-lg hover:bg-primary-dark transition-colors"
              >
                New Conversation
              </button>
            </div>
          </div>
        )}
      </div>

      {/* Backdrop for mobile drawer */}
      {drawerOpen && (
        <div
          className="fixed inset-0 bg-black bg-opacity-50 z-40 lg:hidden"
          onClick={() => setDrawerOpen(false)}
        />
      )}
    </div>
  );
}

export default App;