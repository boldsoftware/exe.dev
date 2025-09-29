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
      
      // If we have conversations and no current one selected, select the first
      if (!currentConversationId && convs.length > 0) {
        setCurrentConversationId(convs[0].conversation_id);
      }
      // If no conversations exist, leave currentConversationId as null
      // The UI will show the welcome screen and create conversation on first message
    } catch (err) {
      console.error('Failed to load conversations:', err);
      setError('Failed to load conversations. Please refresh the page.');
    } finally {
      setLoading(false);
    }
  };

  const startNewConversation = () => {
    // Just clear the current conversation - a new one will be created when the user sends their first message
    setCurrentConversationId(null);
    setDrawerOpen(false);
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

  const handleFirstMessage = async (message: string, model: string) => {
    try {
      const response = await api.sendMessageWithNewConversation({ message, model });
      const newConversationId = response.conversation_id;
      
      // Fetch the new conversation details
      const updatedConvs = await api.getConversations();
      setConversations(updatedConvs);
      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error('Failed to send first message:', err);
      setError('Failed to send message');
      throw err;
    }
  };

  return (
    <div className="h-screen bg-gray-50 dark:bg-gray-900 flex relative">
      {/* Conversations drawer */}
      <ConversationDrawer
        isOpen={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        conversations={conversations}
        currentConversationId={currentConversationId}
        onSelectConversation={selectConversation}
        onNewConversation={startNewConversation}
      />

      {/* Main chat interface */}
      <div className="flex-1 flex flex-col">
        <ChatInterface
          conversationId={currentConversationId}
          onOpenDrawer={() => setDrawerOpen(true)}
          onNewConversation={startNewConversation}
          currentConversation={currentConversation}
          onConversationUpdate={updateConversation}
          onFirstMessage={handleFirstMessage}
        />
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