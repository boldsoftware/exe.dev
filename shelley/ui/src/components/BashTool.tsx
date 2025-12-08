import React, { useState } from "react";
import { LLMContent } from "../types";

interface BashToolProps {
  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BashTool({ toolInput, isRunning, toolResult, hasError, executionTime }: BashToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract command from toolInput
  const command =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "command" in toolInput &&
    typeof toolInput.command === "string"
      ? toolInput.command
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Check if this was a cancelled operation
  const isCancelled = hasError && output.toLowerCase().includes("cancel");

  // Truncate command for display
  const truncateCommand = (cmd: string, maxLen: number = 300) => {
    if (cmd.length <= maxLen) return cmd;
    return cmd.substring(0, maxLen) + "...";
  };

  const displayCommand = truncateCommand(command);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div
      className="bash-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="bash-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="bash-tool-summary">
          <span className={`bash-tool-emoji ${isRunning ? "running" : ""}`}>🛠️</span>
          <span className="bash-tool-command">{displayCommand}</span>
          {isComplete && isCancelled && <span className="bash-tool-cancelled">✗ cancelled</span>}
          {isComplete && hasError && !isCancelled && <span className="bash-tool-error">✗</span>}
          {isComplete && !hasError && <span className="bash-tool-success">✓</span>}
        </div>
        <button
          className="bash-tool-toggle"
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
        <div className="bash-tool-details">
          <div className="bash-tool-section">
            <div className="bash-tool-label">Command:</div>
            <pre className="bash-tool-code">{command}</pre>
          </div>

          {isComplete && (
            <div className="bash-tool-section">
              <div className="bash-tool-label">
                Output{hasError ? " (Error)" : ""}:
                {executionTime && <span className="bash-tool-time">{executionTime}</span>}
              </div>
              <pre className={`bash-tool-code ${hasError ? "error" : ""}`}>
                {output || "(no output)"}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BashTool;
