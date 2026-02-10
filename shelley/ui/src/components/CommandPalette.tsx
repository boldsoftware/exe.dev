import React, { useState, useEffect, useRef, useMemo, useCallback } from "react";
import { Conversation } from "../types";
import { api } from "../services/api";

interface CommandItem {
  id: string;
  type: "action" | "conversation";
  title: string;
  subtitle?: string;
  icon?: React.ReactNode;
  action: () => void;
  keywords?: string[]; // Additional keywords for search
}

interface CommandPaletteProps {
  isOpen: boolean;
  onClose: () => void;
  conversations: Conversation[];
  onNewConversation: () => void;
  onSelectConversation: (conversation: Conversation) => void;
  onOpenDiffViewer: () => void;
  onOpenModelsModal: () => void;
  onOpenNotificationsModal: () => void;
  hasCwd: boolean;
}

// Simple fuzzy match for actions - returns score (higher is better), -1 if no match
function fuzzyMatch(query: string, text: string): number {
  const lowerQuery = query.toLowerCase();
  const lowerText = text.toLowerCase();

  // Exact match gets highest score
  if (lowerText === lowerQuery) return 1000;

  // Starts with gets high score
  if (lowerText.startsWith(lowerQuery)) return 500 + (lowerQuery.length / lowerText.length) * 100;

  // Contains gets medium score
  if (lowerText.includes(lowerQuery)) return 100 + (lowerQuery.length / lowerText.length) * 50;

  // Fuzzy match - all query chars must appear in order
  let queryIdx = 0;
  let score = 0;
  let consecutiveBonus = 0;

  for (let i = 0; i < lowerText.length && queryIdx < lowerQuery.length; i++) {
    if (lowerText[i] === lowerQuery[queryIdx]) {
      score += 1 + consecutiveBonus;
      consecutiveBonus += 0.5;
      queryIdx++;
    } else {
      consecutiveBonus = 0;
    }
  }

  // All query chars must be found
  if (queryIdx !== lowerQuery.length) return -1;

  return score;
}

function CommandPalette({
  isOpen,
  onClose,
  conversations,
  onNewConversation,
  onSelectConversation,
  onOpenDiffViewer,
  onOpenModelsModal,
  onOpenNotificationsModal,
  hasCwd,
}: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [searchResults, setSearchResults] = useState<Conversation[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<number | null>(null);

  // Search conversations on the server
  const searchConversations = useCallback(async (searchQuery: string) => {
    if (!searchQuery.trim()) {
      setSearchResults([]);
      setIsSearching(false);
      return;
    }

    setIsSearching(true);
    try {
      const results = await api.searchConversations(searchQuery);
      setSearchResults(results);
    } catch (err) {
      console.error("Failed to search conversations:", err);
      setSearchResults([]);
    } finally {
      setIsSearching(false);
    }
  }, []);

  // Debounced search when query changes
  useEffect(() => {
    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
    }

    if (query.trim()) {
      searchTimeoutRef.current = window.setTimeout(() => {
        searchConversations(query);
      }, 150); // 150ms debounce
    } else {
      setSearchResults([]);
      setIsSearching(false);
    }

    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
      }
    };
  }, [query, searchConversations]);

  // Build action items (these are always available)
  const actionItems: CommandItem[] = useMemo(() => {
    const items: CommandItem[] = [];

    items.push({
      id: "new-conversation",
      type: "action",
      title: "New Conversation",
      subtitle: "Start a new conversation",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 4v16m8-8H4" />
        </svg>
      ),
      action: () => {
        onNewConversation();
        onClose();
      },
      keywords: ["new", "create", "start", "conversation", "chat"],
    });

    if (hasCwd) {
      items.push({
        id: "open-diffs",
        type: "action",
        title: "View Diffs",
        subtitle: "Open the git diff viewer",
        icon: (
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
            />
          </svg>
        ),
        action: () => {
          onOpenDiffViewer();
          onClose();
        },
        keywords: ["diff", "git", "changes", "view", "compare"],
      });
    }

    items.push({
      id: "manage-models",
      type: "action",
      title: "Add/Remove Models/Keys",
      subtitle: "Configure custom AI models and API keys",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
          />
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
          />
        </svg>
      ),
      action: () => {
        onOpenModelsModal();
        onClose();
      },
      keywords: [
        "model",
        "key",
        "api",
        "configure",
        "settings",
        "anthropic",
        "openai",
        "gemini",
        "custom",
      ],
    });

    items.push({
      id: "notification-settings",
      type: "action",
      title: "Notification Settings",
      subtitle: "Configure notification channels",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
          />
        </svg>
      ),
      action: () => {
        onOpenNotificationsModal();
        onClose();
      },
      keywords: ["notification", "notify", "alert", "discord", "webhook", "browser", "favicon"],
    });

    return items;
  }, [onNewConversation, onOpenDiffViewer, onOpenModelsModal, onOpenNotificationsModal, onClose, hasCwd]);

  // Convert conversations to command items
  const conversationToItem = useCallback(
    (conv: Conversation): CommandItem => ({
      id: `conv-${conv.conversation_id}`,
      type: "conversation",
      title: conv.slug || conv.conversation_id,
      subtitle: conv.cwd || undefined,
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z"
          />
        </svg>
      ),
      action: () => {
        onSelectConversation(conv);
        onClose();
      },
    }),
    [onSelectConversation, onClose],
  );

  // Compute the final list of items to display
  const displayItems = useMemo(() => {
    const trimmedQuery = query.trim();

    // Filter actions based on query (client-side fuzzy match)
    let filteredActions = actionItems;
    if (trimmedQuery) {
      filteredActions = actionItems.filter((item) => {
        let maxScore = fuzzyMatch(trimmedQuery, item.title);
        if (item.subtitle) {
          const subtitleScore = fuzzyMatch(trimmedQuery, item.subtitle);
          if (subtitleScore > maxScore) maxScore = subtitleScore * 0.8;
        }
        if (item.keywords) {
          for (const keyword of item.keywords) {
            const keywordScore = fuzzyMatch(trimmedQuery, keyword);
            if (keywordScore > maxScore) maxScore = keywordScore * 0.7;
          }
        }
        return maxScore > 0;
      });
    }

    // Use search results if we have a query, otherwise use initial conversations
    const conversationsToShow = trimmedQuery ? searchResults : conversations;
    const conversationItems = conversationsToShow.map(conversationToItem);

    return [...filteredActions, ...conversationItems];
  }, [query, actionItems, searchResults, conversations, conversationToItem]);

  // Reset selection when items change
  useEffect(() => {
    setSelectedIndex(0);
  }, [displayItems]);

  // Focus input when opened
  useEffect(() => {
    if (isOpen) {
      setQuery("");
      setSelectedIndex(0);
      setSearchResults([]);
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isOpen]);

  // Scroll selected item into view
  useEffect(() => {
    if (!listRef.current) return;
    const selectedElement = listRef.current.querySelector(`[data-index="${selectedIndex}"]`);
    selectedElement?.scrollIntoView({ block: "nearest" });
  }, [selectedIndex]);

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setSelectedIndex((prev) => Math.min(prev + 1, displayItems.length - 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        setSelectedIndex((prev) => Math.max(prev - 1, 0));
        break;
      case "Enter":
        e.preventDefault();
        if (displayItems[selectedIndex]) {
          displayItems[selectedIndex].action();
        }
        break;
      case "Escape":
        e.preventDefault();
        onClose();
        break;
    }
  };

  if (!isOpen) return null;

  return (
    <div className="command-palette-overlay" onClick={onClose}>
      <div className="command-palette" onClick={(e) => e.stopPropagation()}>
        <div className="command-palette-input-wrapper">
          <svg
            className="command-palette-search-icon"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            width="20"
            height="20"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
            />
          </svg>
          <input
            ref={inputRef}
            type="text"
            className="command-palette-input"
            placeholder="Search conversations or actions..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
          />
          {isSearching && <div className="command-palette-spinner" />}
          <div className="command-palette-shortcut">
            <kbd>esc</kbd>
          </div>
        </div>

        <div className="command-palette-list" ref={listRef}>
          {displayItems.length === 0 ? (
            <div className="command-palette-empty">
              {isSearching ? "Searching..." : "No results found"}
            </div>
          ) : (
            displayItems.map((item, index) => (
              <div
                key={item.id}
                data-index={index}
                className={`command-palette-item ${index === selectedIndex ? "selected" : ""}`}
                onClick={() => item.action()}
                onMouseEnter={() => setSelectedIndex(index)}
              >
                <div className="command-palette-item-icon">{item.icon}</div>
                <div className="command-palette-item-content">
                  <div className="command-palette-item-title">{item.title}</div>
                  {item.subtitle && (
                    <div className="command-palette-item-subtitle">{item.subtitle}</div>
                  )}
                </div>
                {item.type === "action" && <div className="command-palette-item-badge">Action</div>}
              </div>
            ))
          )}
        </div>

        <div className="command-palette-footer">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> to navigate
          </span>
          <span>
            <kbd>↵</kbd> to select
          </span>
          <span>
            <kbd>esc</kbd> to close
          </span>
        </div>
      </div>
    </div>
  );
}

export default CommandPalette;
