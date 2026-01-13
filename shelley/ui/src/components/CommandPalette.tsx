import React, { useState, useEffect, useRef, useMemo } from "react";
import { Conversation } from "../types";

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
  onSelectConversation: (id: string) => void;
  onOpenDiffViewer: () => void;
  hasCwd: boolean;
}

// Simple fuzzy match - returns score (higher is better), -1 if no match
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
  hasCwd,
}: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Build list of command items
  const allItems: CommandItem[] = useMemo(() => {
    const items: CommandItem[] = [];

    // Actions
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

    // Add conversations
    conversations.forEach((conv) => {
      items.push({
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
          onSelectConversation(conv.conversation_id);
          onClose();
        },
        keywords: [conv.slug || "", conv.cwd || ""].filter(Boolean),
      });
    });

    return items;
  }, [conversations, onNewConversation, onSelectConversation, onOpenDiffViewer, onClose, hasCwd]);

  // Filter and sort items based on query
  const filteredItems = useMemo(() => {
    if (!query.trim()) {
      // No query - show actions first, then recent conversations
      return allItems;
    }

    // Score and filter items
    const scored = allItems
      .map((item) => {
        let maxScore = fuzzyMatch(query, item.title);

        // Check subtitle
        if (item.subtitle) {
          const subtitleScore = fuzzyMatch(query, item.subtitle);
          if (subtitleScore > maxScore) maxScore = subtitleScore * 0.8; // Slightly lower weight
        }

        // Check keywords
        if (item.keywords) {
          for (const keyword of item.keywords) {
            const keywordScore = fuzzyMatch(query, keyword);
            if (keywordScore > maxScore) maxScore = keywordScore * 0.7;
          }
        }

        return { item, score: maxScore };
      })
      .filter(({ score }) => score > 0)
      .sort((a, b) => b.score - a.score);

    return scored.map(({ item }) => item);
  }, [allItems, query]);

  // Reset selection when query changes
  useEffect(() => {
    setSelectedIndex(0);
  }, [query]);

  // Focus input when opened
  useEffect(() => {
    if (isOpen) {
      setQuery("");
      setSelectedIndex(0);
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
        setSelectedIndex((prev) => Math.min(prev + 1, filteredItems.length - 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        setSelectedIndex((prev) => Math.max(prev - 1, 0));
        break;
      case "Enter":
        e.preventDefault();
        if (filteredItems[selectedIndex]) {
          filteredItems[selectedIndex].action();
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
          <div className="command-palette-shortcut">
            <kbd>esc</kbd>
          </div>
        </div>

        <div className="command-palette-list" ref={listRef}>
          {filteredItems.length === 0 ? (
            <div className="command-palette-empty">No results found</div>
          ) : (
            filteredItems.map((item, index) => (
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
