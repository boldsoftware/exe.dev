import React, { useState, useEffect } from "react";
import { Conversation } from "../types";
import { api } from "../services/api";

interface ConversationDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  conversations: Conversation[];
  currentConversationId: string | null;
  onSelectConversation: (id: string) => void;
  onNewConversation: () => void;
  onConversationArchived?: (id: string) => void;
  onConversationUnarchived?: (conversation: Conversation) => void;
}

function ConversationDrawer({
  isOpen,
  onClose,
  conversations,
  currentConversationId,
  onSelectConversation,
  onNewConversation,
  onConversationArchived,
  onConversationUnarchived,
}: ConversationDrawerProps) {
  const [showArchived, setShowArchived] = useState(false);
  const [archivedConversations, setArchivedConversations] = useState<Conversation[]>([]);
  const [loadingArchived, setLoadingArchived] = useState(false);

  useEffect(() => {
    if (showArchived && archivedConversations.length === 0) {
      loadArchivedConversations();
    }
  }, [showArchived]);

  const loadArchivedConversations = async () => {
    setLoadingArchived(true);
    try {
      const archived = await api.getArchivedConversations();
      setArchivedConversations(archived);
    } catch (err) {
      console.error("Failed to load archived conversations:", err);
    } finally {
      setLoadingArchived(false);
    }
  };

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

  const handleArchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    try {
      await api.archiveConversation(conversationId);
      onConversationArchived?.(conversationId);
      // Refresh archived list if viewing
      if (showArchived) {
        loadArchivedConversations();
      }
    } catch (err) {
      console.error("Failed to archive conversation:", err);
    }
  };

  const handleUnarchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    try {
      const conversation = await api.unarchiveConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
      onConversationUnarchived?.(conversation);
    } catch (err) {
      console.error("Failed to unarchive conversation:", err);
    }
  };

  const handleDelete = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    if (!confirm("Are you sure you want to permanently delete this conversation?")) {
      return;
    }
    try {
      await api.deleteConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
    } catch (err) {
      console.error("Failed to delete conversation:", err);
    }
  };

  const displayedConversations = showArchived ? archivedConversations : conversations;

  return (
    <>
      {/* Drawer */}
      <div className={`drawer ${isOpen ? "open" : ""}`}>
        {/* Header */}
        <div className="drawer-header">
          <h2 className="drawer-title">{showArchived ? "Archived" : "Conversations"}</h2>
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
        {!showArchived && (
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
        )}

        {/* Conversations list */}
        <div className="drawer-body scrollable">
          {loadingArchived && showArchived ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>Loading...</p>
            </div>
          ) : displayedConversations.length === 0 ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>{showArchived ? "No archived conversations" : "No conversations yet"}</p>
              {!showArchived && (
                <p className="text-sm" style={{ marginTop: "0.25rem" }}>
                  Start a new conversation to get started
                </p>
              )}
            </div>
          ) : (
            <div className="conversation-list">
              {displayedConversations.map((conversation) => {
                const isActive = conversation.conversation_id === currentConversationId;
                return (
                  <div
                    key={conversation.conversation_id}
                    className={`conversation-item ${isActive ? "active" : ""}`}
                    onClick={() => {
                      if (!showArchived) {
                        onSelectConversation(conversation.conversation_id);
                      }
                    }}
                    style={{ cursor: showArchived ? "default" : "pointer" }}
                  >
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div className="conversation-title">
                        {getConversationPreview(conversation)}
                      </div>
                      <div className="conversation-date">{formatDate(conversation.updated_at)}</div>
                    </div>
                    <div
                      className="conversation-actions"
                      style={{ display: "flex", gap: "0.25rem", marginLeft: "0.5rem" }}
                    >
                      {showArchived ? (
                        <>
                          <button
                            onClick={(e) => handleUnarchive(e, conversation.conversation_id)}
                            className="btn-icon-sm"
                            title="Restore"
                            aria-label="Restore conversation"
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
                                d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                              />
                            </svg>
                          </button>
                          <button
                            onClick={(e) => handleDelete(e, conversation.conversation_id)}
                            className="btn-icon-sm btn-danger"
                            title="Delete permanently"
                            aria-label="Delete conversation"
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
                                d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                              />
                            </svg>
                          </button>
                        </>
                      ) : (
                        <button
                          onClick={(e) => handleArchive(e, conversation.conversation_id)}
                          className="btn-icon-sm"
                          title="Archive"
                          aria-label="Archive conversation"
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
                              d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
                            />
                          </svg>
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        {/* Footer with archived toggle */}
        <div className="drawer-footer">
          <button
            onClick={() => setShowArchived(!showArchived)}
            className="btn-secondary"
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
              {showArchived ? (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M11 15l-3-3m0 0l3-3m-3 3h8M3 12a9 9 0 1118 0 9 9 0 01-18 0z"
                />
              ) : (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
                />
              )}
            </svg>
            <span>{showArchived ? "Back to Conversations" : "View Archived"}</span>
          </button>
        </div>
      </div>
    </>
  );
}

export default ConversationDrawer;
