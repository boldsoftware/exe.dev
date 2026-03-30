import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserScreencastToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}

function getInputField(input: unknown, field: string): string | undefined {
  if (typeof input === "object" && input !== null && field in input) {
    const val = (input as Record<string, unknown>)[field];
    return typeof val === "string" ? val : undefined;
  }
  return undefined;
}

function getAction(input: unknown): string {
  return getInputField(input, "action") || "screencast";
}

function BrowserScreencastTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  display,
}: BrowserScreencastToolProps) {
  const [isExpanded, setIsExpanded] = useState(true);

  const action = getAction(toolInput);

  // Determine emoji and label based on action
  let emoji = "🎬";
  let label = "screencast";
  switch (action) {
    case "screencast_start":
      emoji = "🔴";
      label = "recording";
      break;
    case "screencast_stop":
      emoji = "🎬";
      label = "screencast";
      break;
    case "screencast_status":
      emoji = "📊";
      label = "screencast status";
      break;
  }

  // Extract output text from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Extract video URL from display data
  let videoUrl: string | undefined;
  if (display && typeof display === "object" && display !== null) {
    const d = display as Record<string, unknown>;
    if (d.type === "screencast") {
      if (typeof d.url === "string") {
        videoUrl = d.url;
      } else if (typeof d.path === "string") {
        videoUrl = `/api/read?path=${encodeURIComponent(d.path as string)}`;
      }
    }
  }

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div
      className="screencast-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="screencast-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="screencast-tool-summary">
          <span className={`screencast-tool-emoji ${isRunning ? "running" : ""}`}>{emoji}</span>
          <span className="screencast-tool-label">{label}</span>
          {isComplete && hasError && <span className="screencast-tool-error">✗</span>}
          {isComplete && !hasError && <span className="screencast-tool-success">✓</span>}
        </div>
        <button
          className="screencast-tool-toggle"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            className={`tool-chevron${isExpanded ? " tool-chevron-expanded" : ""}`}
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
        <div className="screencast-tool-details">
          {isRunning && (
            <div className="screencast-tool-section">
              <div className="screencast-tool-status">
                {action === "screencast_start" && "Starting screencast recording..."}
                {action === "screencast_stop" && "Stopping screencast..."}
                {action === "screencast_status" && "Checking screencast status..."}
              </div>
            </div>
          )}

          {isComplete && !hasError && videoUrl && (
            <div className="screencast-tool-section">
              {executionTime && (
                <div className="screencast-tool-meta">
                  <span>{executionTime}</span>
                </div>
              )}
              <div className="screencast-tool-video-container">
                <video controls preload="metadata" className="screencast-tool-video">
                  <source src={videoUrl} type="video/mp4" />
                  Your browser does not support the video tag.
                </video>
              </div>
            </div>
          )}

          {isComplete && !hasError && !videoUrl && output && (
            <div className="screencast-tool-section">
              {executionTime && (
                <div className="screencast-tool-meta">
                  <span>{executionTime}</span>
                </div>
              )}
              <pre className="screencast-tool-output">{output}</pre>
            </div>
          )}

          {isComplete && hasError && (
            <div className="screencast-tool-section">
              {executionTime && (
                <div className="screencast-tool-meta">
                  <span>{executionTime}</span>
                </div>
              )}
              <pre className="screencast-tool-error-message">
                {output || "Screencast operation failed"}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BrowserScreencastTool;
