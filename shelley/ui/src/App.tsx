import React, { useState, useEffect } from "react";
import ChatInterface from "./components/ChatInterface";
import ConversationDrawer from "./components/ConversationDrawer";
import { Conversation } from "./types";
import { api } from "./services/api";

// Check if a slug is a generated ID (format: cXXXX where X is alphanumeric)
function isGeneratedId(slug: string | null): boolean {
  if (!slug) return true;
  return /^c[a-z0-9]+$/i.test(slug);
}

function updatePageTitle(conversation: Conversation | undefined) {
  const hostname = window.__SHELLEY_INIT__?.hostname;
  const parts: string[] = [];

  if (conversation?.slug && !isGeneratedId(conversation.slug)) {
    parts.push(conversation.slug);
  }
  if (hostname) {
    parts.push(hostname);
  }
  parts.push("Shelley Agent");

  document.title = parts.join(" - ");
}

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

  // Update page title when conversation changes
  useEffect(() => {
    const currentConv = conversations.find(
      (conv) => conv.conversation_id === currentConversationId,
    );
    updatePageTitle(currentConv);
  }, [currentConversationId, conversations]);

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

  const handleConversationArchived = (conversationId: string) => {
    setConversations((prev) => prev.filter((conv) => conv.conversation_id !== conversationId));
    // If the archived conversation was current, switch to another or clear
    if (currentConversationId === conversationId) {
      const remaining = conversations.filter((conv) => conv.conversation_id !== conversationId);
      setCurrentConversationId(remaining.length > 0 ? remaining[0].conversation_id : null);
    }
  };

  const handleConversationUnarchived = (conversation: Conversation) => {
    // Add the unarchived conversation back to the list
    setConversations((prev) => [conversation, ...prev]);
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

  // Get the CWD from the most recent conversation (first in list, sorted by updated_at desc)
  const mostRecentCwd = conversations.length > 0 ? conversations[0].cwd : null;

  const handleFirstMessage = async (message: string, model: string, cwd?: string) => {
    try {
      const response = await api.sendMessageWithNewConversation({ message, model, cwd });
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
        onConversationArchived={handleConversationArchived}
        onConversationUnarchived={handleConversationUnarchived}
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
          mostRecentCwd={mostRecentCwd}
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
