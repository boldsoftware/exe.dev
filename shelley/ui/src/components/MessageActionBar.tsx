import React, { useState } from "react";

interface MessageActionBarProps {
  onCopy?: () => void;
  onShowUsage?: () => void;
}

function MessageActionBar({ onCopy, onShowUsage }: MessageActionBarProps) {
  const [copyFeedback, setCopyFeedback] = useState(false);

  const handleCopy = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onCopy) {
      onCopy();
      setCopyFeedback(true);
      setTimeout(() => setCopyFeedback(false), 1500);
    }
  };

  const handleShowUsage = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (onShowUsage) {
      onShowUsage();
    }
  };

  return (
    <div
      className="message-action-bar"
      data-action-bar
      style={{
        position: "absolute",
        top: "-28px",
        right: "8px",
        display: "flex",
        gap: "2px",
        background: "var(--bg-base)",
        border: "1px solid var(--border)",
        borderRadius: "4px",
        padding: "2px",

        zIndex: 10,
      }}
    >
      {onCopy && (
        <button
          onClick={handleCopy}
          title="Copy"
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            width: "24px",
            height: "24px",
            borderRadius: "4px",
            border: "none",
            background: copyFeedback ? "var(--success-bg)" : "transparent",
            cursor: "pointer",
            color: copyFeedback ? "var(--success-text)" : "var(--text-secondary)",
            transition: "background-color 0.15s, color 0.15s",
          }}
          onMouseEnter={(e) => {
            if (!copyFeedback) {
              e.currentTarget.style.backgroundColor = "var(--bg-tertiary)";
            }
          }}
          onMouseLeave={(e) => {
            if (!copyFeedback) {
              e.currentTarget.style.backgroundColor = "transparent";
            }
          }}
        >
          {copyFeedback ? (
            <svg
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <polyline points="20 6 9 17 4 12"></polyline>
            </svg>
          ) : (
            <svg
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
              <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
            </svg>
          )}
        </button>
      )}
      {onShowUsage && (
        <button
          onClick={handleShowUsage}
          title="Usage Details"
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            width: "24px",
            height: "24px",
            borderRadius: "4px",
            border: "none",
            background: "transparent",
            cursor: "pointer",
            color: "var(--text-secondary)",
            transition: "background-color 0.15s",
          }}
          onMouseEnter={(e) => {
            e.currentTarget.style.backgroundColor = "var(--bg-tertiary)";
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.backgroundColor = "transparent";
          }}
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <circle cx="12" cy="12" r="10"></circle>
            <line x1="12" y1="16" x2="12" y2="12"></line>
            <line x1="12" y1="8" x2="12.01" y2="8"></line>
          </svg>
        </button>
      )}
    </div>
  );
}

export default MessageActionBar;
