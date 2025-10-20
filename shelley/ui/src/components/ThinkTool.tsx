import React, { useState } from "react";
import { LLMContent } from "../types";

interface ThinkToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { thoughts: string }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function ThinkTool({ toolInput, isRunning, toolResult, hasError, executionTime }: ThinkToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract thoughts from toolInput
  const thoughts =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "thoughts" in toolInput &&
    typeof toolInput.thoughts === "string"
      ? toolInput.thoughts
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Truncate thoughts for display - get first 50 chars
  const truncateThoughts = (text: string, maxLen: number = 50) => {
    if (!text) return "";
    if (text.length <= maxLen) return text;
    return text.substring(0, maxLen) + "...";
  };

  const displayThoughts = truncateThoughts(thoughts);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>💭</span>
          <span className="tool-command">
            {displayThoughts || (isRunning ? "thinking..." : "thinking...")}
          </span>
          {isComplete && hasError && <span className="tool-error">✗</span>}
          {isComplete && !hasError && <span className="tool-success">✓</span>}
        </div>
        <button
          className="tool-toggle"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            style={{
              transform: isExpanded ? "rotate(90deg)" : "rotate(0deg)",
              transition: "transform 0.2s",
            }}
          >
            <path
              d="M4.5 3L7.5 6L4.5 9"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </button>
      </div>

      {isExpanded && (
        <div className="tool-details">
          <div className="tool-section">
            <div className="tool-label">
              Thoughts:
              {executionTime && <span className="tool-time">{executionTime}</span>}
            </div>
            <div className={`tool-code ${hasError ? "error" : ""}`}>
              {thoughts || "(no thoughts)"}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export default ThinkTool;
