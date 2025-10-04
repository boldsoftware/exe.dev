import React from "react";
import { Conversation } from "../types";

interface ConversationDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  conversations: Conversation[];
  currentConversationId: string | null;
  onSelectConversation: (id: string) => void;
  onNewConversation: () => void;
}

function ConversationDrawer({
  isOpen,
  onClose,
  conversations,
  currentConversationId,
  onSelectConversation,
  onNewConversation,
}: ConversationDrawerProps) {
  const formatDate = (timestamp: string) => {
    const date = new Date(timestamp);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));

    if (diffDays === 0) {
      return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    } else if (diffDays === 1) {
      return "Yesterday";
    } else if (diffDays < 7) {
      return `${diffDays} days ago`;
    } else {
      return date.toLocaleDateString();
    }
  };

  const getConversationPreview = (conversation: Conversation) => {
    if (conversation.slug) {
      return conversation.slug;
    }
    // Show full conversation ID
    return conversation.conversation_id;
  };

  return (
    <>
      {/* Drawer */}
      <div className={`drawer ${isOpen ? "open" : ""}`}>
        {/* Header */}
        <div className="drawer-header">
          <h2 className="drawer-title">Conversations</h2>
          <button
            onClick={onClose}
            className="btn-icon hide-on-desktop"
            aria-label="Close conversations"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>

        {/* New conversation button */}
        <div className="drawer-section">
          <button
            onClick={onNewConversation}
            className="btn-primary"
            style={{
              width: "100%",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              gap: "0.5rem",
            }}
          >
            <svg
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              style={{ width: "1rem", height: "1rem" }}
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M12 4v16m8-8H4"
              />
            </svg>
            <span>New Conversation</span>
          </button>
        </div>

        {/* Conversations list */}
        <div className="drawer-body scrollable">
          {conversations.length === 0 ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>No conversations yet</p>
              <p className="text-sm" style={{ marginTop: "0.25rem" }}>
                Start a new conversation to get started
              </p>
            </div>
          ) : (
            <div className="conversation-list">
              {conversations.map((conversation) => {
                const isActive = conversation.conversation_id === currentConversationId;
                return (
                  <button
                    key={conversation.conversation_id}
                    onClick={() => onSelectConversation(conversation.conversation_id)}
                    className={`conversation-item ${isActive ? "active" : ""}`}
                  >
                    <div className="conversation-title">{getConversationPreview(conversation)}</div>
                    <div className="conversation-date">{formatDate(conversation.updated_at)}</div>
                  </button>
                );
              })}
            </div>
          )}
        </div>
      </div>
    </>
  );
}

export default ConversationDrawer;
