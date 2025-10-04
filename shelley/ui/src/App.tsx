import React, { useState, useEffect } from "react";
import ChatInterface from "./components/ChatInterface";
import ConversationDrawer from "./components/ConversationDrawer";
import { Conversation } from "./types";
import { api } from "./services/api";

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
      console.error("Failed to load conversations:", err);
      setError("Failed to load conversations. Please refresh the page.");
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
    setConversations((prev) =>
      prev.map((conv) =>
        conv.conversation_id === updatedConversation.conversation_id ? updatedConversation : conv,
      ),
    );
  };

  if (loading && conversations.length === 0) {
    return (
      <div className="loading-container">
        <div className="loading-content">
          <div className="spinner" style={{ margin: "0 auto 1rem" }}></div>
          <p className="text-secondary">Loading...</p>
        </div>
      </div>
    );
  }

  if (error && conversations.length === 0) {
    return (
      <div className="error-container">
        <div className="error-content">
          <p className="error-message" style={{ marginBottom: "1rem" }}>
            {error}
          </p>
          <button onClick={loadConversations} className="btn-primary">
            Retry
          </button>
        </div>
      </div>
    );
  }

  const currentConversation = conversations.find(
    (conv) => conv.conversation_id === currentConversationId,
  );

  const handleFirstMessage = async (message: string, model: string) => {
    try {
      const response = await api.sendMessageWithNewConversation({ message, model });
      const newConversationId = response.conversation_id;

      // Fetch the new conversation details
      const updatedConvs = await api.getConversations();
      setConversations(updatedConvs);
      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to send first message:", err);
      setError("Failed to send message");
      throw err;
    }
  };

  return (
    <div className="app-container">
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
      <div className="main-content">
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
        <div className="backdrop hide-on-desktop" onClick={() => setDrawerOpen(false)} />
      )}
    </div>
  );
}

export default App;
