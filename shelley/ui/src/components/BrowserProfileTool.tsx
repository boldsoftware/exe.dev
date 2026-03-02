import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserProfileToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserProfileTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserProfileToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  const [copied, setCopied] = useState(false);

  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as { action?: string; categories?: string })
      : {};

  const action = input.action || "";

  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  // Detect file paths in output (for cpu_stop, trace_stop results)
  const filePathMatch = output.match(/([^\s]+\.json)/i);
  const savedFilePath = filePathMatch ? filePathMatch[1] : null;

  const summary = action || "profile";

  const handleCopyPath = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (savedFilePath) {
      navigator.clipboard.writeText(savedFilePath).then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      });
    }
  };

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>📊</span>
          <span className="tool-command">{summary}</span>
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
            <div className="tool-label">Action:</div>
            <pre className="tool-code">{action || "(none)"}</pre>
          </div>

          {input.categories && (
            <div className="tool-section">
              <div className="tool-label">Categories:</div>
              <pre className="tool-code">{input.categories}</pre>
            </div>
          )}

          {isComplete && output && (
            <div className="tool-section">
              <div className="tool-label">
                Output{hasError ? " (Error)" : ""}:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <pre className={`tool-code ${hasError ? "error" : ""}`}>{output}</pre>
            </div>
          )}

          {isComplete && savedFilePath && !hasError && (
            <div className="tool-section">
              <div className="tool-label">Profile/Trace file:</div>
              <div
                style={{ display: "flex", alignItems: "center", gap: "0.5rem", flexWrap: "wrap" }}
              >
                <code
                  style={{
                    background: "var(--bg-tertiary)",
                    padding: "0.25rem 0.5rem",
                    borderRadius: "0.25rem",
                    fontSize: "0.85rem",
                    wordBreak: "break-all",
                  }}
                >
                  {savedFilePath}
                </code>
                <button
                  onClick={handleCopyPath}
                  style={{
                    padding: "0.25rem 0.5rem",
                    fontSize: "0.75rem",
                    background: "var(--bg-secondary)",
                    border: "1px solid var(--border)",
                    borderRadius: "0.25rem",
                    cursor: "pointer",
                    color: "var(--text-primary)",
                  }}
                >
                  {copied ? "✓ Copied" : "📋 Copy path"}
                </button>
                {(action === "cpu_stop" || action === "trace_stop") && (
                  <a
                    href={`https://www.speedscope.app/#profileURL=${encodeURIComponent(window.location.origin + "/api/read?path=" + encodeURIComponent(savedFilePath))}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    onClick={(e) => e.stopPropagation()}
                    style={{
                      padding: "0.25rem 0.75rem",
                      fontSize: "0.75rem",
                      background: "var(--bg-secondary)",
                      border: "1px solid var(--border)",
                      borderRadius: "0.25rem",
                      cursor: "pointer",
                      color: "var(--text-primary)",
                      textDecoration: "none",
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "0.25rem",
                    }}
                  >
                    🔥 Open in Speedscope
                  </a>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BrowserProfileTool;
