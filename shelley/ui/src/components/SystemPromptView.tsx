import React, { useState } from "react";
import { Message, LLMContent } from "../types";

interface JSONSchemaProperty {
  type?: string | string[];
  description?: string;
  enum?: (string | number | boolean | null)[];
  items?: JSONSchemaProperty;
  $ref?: string;
  [key: string]: unknown;
}

interface JSONSchema {
  type?: string;
  properties?: Record<string, JSONSchemaProperty>;
  required?: string[];
  [key: string]: unknown;
}

interface ToolDescription {
  name: string;
  description: string;
  parameters?: JSONSchema;
}

interface SystemPromptDisplayData {
  tools?: ToolDescription[];
}

interface SystemPromptViewProps {
  message: Message;
}

function ToolItem({ tool }: { tool: ToolDescription }) {
  const [expanded, setExpanded] = useState(false);

  const firstLine = tool.description.trim().split("\n")[0];
  const hasDetails: boolean =
    tool.description.trim().includes("\n") ||
    Boolean(tool.parameters?.properties && Object.keys(tool.parameters.properties).length > 0);

  const required = new Set(tool.parameters?.required ?? []);
  const properties = tool.parameters?.properties ?? {};

  return (
    <div className="system-prompt-tool-item">
      <div
        className={`system-prompt-tool-header${hasDetails ? " system-prompt-tool-header--clickable" : ""}`}
        onClick={hasDetails ? () => setExpanded(!expanded) : undefined}
        onKeyDown={
          hasDetails
            ? (e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setExpanded(!expanded);
                }
              }
            : undefined
        }
        tabIndex={hasDetails ? 0 : undefined}
        role={hasDetails ? "button" : undefined}
        aria-expanded={hasDetails ? expanded : undefined}
      >
        {hasDetails && (
          <svg
            width="10"
            height="10"
            viewBox="0 0 10 10"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            className={`tool-item-chevron${expanded ? " tool-item-chevron--expanded" : ""}`}
          >
            <path
              d="M3 2L7 5L3 8"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        )}
        {!hasDetails && <span className="tool-item-chevron-spacer" />}
        <code className="system-prompt-tool-name">{tool.name}</code>
        <span className="system-prompt-tool-desc">{firstLine}</span>
      </div>

      {expanded && hasDetails && (
        <div className="system-prompt-tool-detail">
          {tool.description.trim().includes("\n") && (
            <p className="system-prompt-tool-full-desc">
              {tool.description.trim().split("\n").slice(1).join("\n").trim()}
            </p>
          )}
          {Object.keys(properties).length > 0 && (
            <div className="system-prompt-tool-params">
              <div className="system-prompt-tool-params-label">Parameters</div>
              <table className="system-prompt-tool-params-table">
                <tbody>
                  {Object.entries(properties).map(([paramName, prop]) => {
                    const isRequired = required.has(paramName);
                    const typeLabel = Array.isArray(prop.type)
                      ? prop.type.join(" | ")
                      : (prop.type ?? "");
                    return (
                      <tr key={paramName} className="system-prompt-tool-param-row">
                        <td className="system-prompt-tool-param-name">
                          <code>{paramName}</code>
                          {isRequired && (
                            <span className="system-prompt-tool-param-required">*</span>
                          )}
                        </td>
                        <td className="system-prompt-tool-param-type">
                          <code>{typeLabel}</code>
                        </td>
                        <td className="system-prompt-tool-param-desc">
                          {prop.description && <span>{prop.description}</span>}
                          {prop.enum && prop.enum.length > 0 && (
                            <span className="system-prompt-tool-param-enum">
                              {" "}
                              Allowed values:{" "}
                              {prop.enum.map((v, i) => (
                                <React.Fragment key={i}>
                                  {i > 0 && ", "}
                                  <code>{String(v)}</code>
                                </React.Fragment>
                              ))}
                            </span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function SystemPromptView({ message }: SystemPromptViewProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract system prompt text from llm_data
  let systemPromptText = "";
  if (message.llm_data) {
    try {
      const llmData =
        typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
      if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
        const textContent = llmData.Content.find((c: LLMContent) => c.Type === 2 && c.Text);
        if (textContent) {
          systemPromptText = textContent.Text;
        }
      }
    } catch (err) {
      console.error("Failed to parse system prompt:", err);
    }
  }

  // Extract tool descriptions from display_data
  let tools: ToolDescription[] = [];
  if (message.display_data) {
    try {
      const displayData: SystemPromptDisplayData =
        typeof message.display_data === "string"
          ? JSON.parse(message.display_data)
          : message.display_data;
      if (displayData && displayData.tools) {
        tools = displayData.tools;
      }
    } catch (err) {
      console.error("Failed to parse system prompt display data:", err);
    }
  }

  if (!systemPromptText) {
    return null;
  }

  // Count lines and approximate size
  const lineCount = systemPromptText.split("\n").length;
  const charCount = systemPromptText.length;
  const sizeKb = (charCount / 1024).toFixed(1);

  return (
    <div className="system-prompt-view">
      <div className="system-prompt-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="system-prompt-summary">
          <span className="system-prompt-icon">📋</span>
          <span className="system-prompt-label">System Prompt</span>
          <span className="system-prompt-meta">
            {lineCount} lines, {sizeKb} KB{tools.length > 0 && ` · ${tools.length} tools`}
          </span>
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
        <div className="system-prompt-content">
          {tools.length > 0 && (
            <div className="system-prompt-tools">
              <div className="system-prompt-tools-label">🔧 Tools ({tools.length})</div>
              <div className="system-prompt-tools-list">
                {tools.map((tool) => (
                  <ToolItem key={tool.name} tool={tool} />
                ))}
              </div>
            </div>
          )}
          <pre className="system-prompt-text">{systemPromptText}</pre>
        </div>
      )}
    </div>
  );
}

export default SystemPromptView;
