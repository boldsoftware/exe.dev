import React, { useState, useRef } from 'react';
import { Message as MessageType, LLMMessage, LLMContent, Usage } from '../types';
import BashTool from './BashTool';
import PatchTool from './PatchTool';

// Display data types from different tools
interface ToolDisplay {
  tool_use_id: string;
  tool_name?: string;
  display: any;
}

// Patch tool display is a unified diff string
type PatchDisplay = string;

interface MessageProps {
  message: MessageType;
}

function Message({ message }: MessageProps) {
  // Hide system messages from the UI
  if (message.type === 'system') {
    return null;
  }

  // Check if we have display_data to render
  const [showTooltip, setShowTooltip] = useState(false);
  const [hoverTimer, setHoverTimer] = useState<NodeJS.Timeout | null>(null);
  // Track cursor for tooltip positioning
  const [cursor, setCursor] = useState<{ x: number; y: number }>({ x: 0, y: 0 });
  const messageRef = useRef<HTMLDivElement | null>(null);

  // Parse usage data if available (only for agent messages)
  let usage: Usage | null = null;
  if (message.type === 'agent' && message.usage_data) {
    try {
      usage = typeof message.usage_data === 'string' ? JSON.parse(message.usage_data) : message.usage_data;
    } catch (err) {
      console.error('Failed to parse usage data:', err);
    }
  }

  // Calculate duration if we have timing info
  let durationMs: number | null = null;
  if (usage?.start_time && usage?.end_time) {
    const start = new Date(usage.start_time).getTime();
    const end = new Date(usage.end_time).getTime();
    durationMs = end - start;
  }

  const handleMouseEnter = () => {
    // Only show tooltip for agent messages with usage data
    if (message.type === 'agent' && usage) {
      const timer = setTimeout(() => {
        setShowTooltip(true);
      }, 500); // Show after 500ms hover
      setHoverTimer(timer);
    }
  };

  const handleMouseLeave = () => {
    if (hoverTimer) {
      clearTimeout(hoverTimer);
      setHoverTimer(null);
    }
    setShowTooltip(false);
  };

  const handleMouseMove: React.MouseEventHandler<HTMLDivElement> = (e) => {
    // Update cursor position for tooltip to follow
    setCursor({ x: e.clientX, y: e.clientY });
  };

  // Format duration in human-readable format
  const formatDuration = (ms: number): string => {
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(2)}s`;
    return `${(ms / 60000).toFixed(2)}m`;
  };

  // Render tooltip with usage information
  const renderTooltip = () => {
    if (!showTooltip || !usage) return null;

    // Clamp tooltip within viewport with some padding
    const vw = typeof window !== 'undefined' ? window.innerWidth : 0;
    const vh = typeof window !== 'undefined' ? window.innerHeight : 0;
    const pad = 12;
    const left = Math.max(4, Math.min(cursor.x + pad, vw - 360)); // assume max width 360
    const top = Math.max(4, Math.min(cursor.y + pad, vh - 200));  // rough height cap

    return (
      <div style={{
        position: 'fixed',
        left: `${left}px`,
        top: `${top}px`,
        backgroundColor: '#1f2937',
        color: '#f9fafb',
        padding: '8px 12px',
        borderRadius: '6px',
        fontSize: '12px',
        lineHeight: '1.5',
        zIndex: 1000,
        minWidth: '200px',
        maxWidth: 'min(60vw, 360px)',
        boxShadow: '0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -1px rgba(0, 0, 0, 0.06)',
        pointerEvents: 'none',
      }}>
        <div style={{ fontWeight: '600', marginBottom: '4px' }}>Token Usage</div>
        <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 8px' }}>
          {usage.model && (
            <>
              <div style={{ color: '#9ca3af' }}>Model:</div>
              <div>{usage.model}</div>
            </>
          )}
          <div style={{ color: '#9ca3af' }}>Input:</div>
          <div>{usage.input_tokens.toLocaleString()}</div>
          {usage.cache_read_input_tokens > 0 && (
            <>
              <div style={{ color: '#9ca3af' }}>Cache Read:</div>
              <div>{usage.cache_read_input_tokens.toLocaleString()}</div>
            </>
          )}
          {usage.cache_creation_input_tokens > 0 && (
            <>
              <div style={{ color: '#9ca3af' }}>Cache Write:</div>
              <div>{usage.cache_creation_input_tokens.toLocaleString()}</div>
            </>
          )}
          <div style={{ color: '#9ca3af' }}>Output:</div>
          <div>{usage.output_tokens.toLocaleString()}</div>
          {usage.cost_usd > 0 && (
            <>
              <div style={{ color: '#9ca3af' }}>Cost:</div>
              <div>\${usage.cost_usd.toFixed(4)}</div>
            </>
          )}
          {durationMs !== null && (
            <>
              <div style={{ color: '#9ca3af' }}>Duration:</div>
              <div>{formatDuration(durationMs)}</div>
            </>
          )}
        </div>
      </div>
    );
  };

  let displayData: ToolDisplay[] | null = null;
  if (message.display_data) {
    try {
      displayData = typeof message.display_data === 'string' ? JSON.parse(message.display_data) : message.display_data;
    } catch (err) {
      console.error('Failed to parse display data:', err);
    }
  }

  // Parse LLM data if available
  let llmMessage: LLMMessage | null = null;
  if (message.llm_data) {
    try {
      llmMessage = typeof message.llm_data === 'string' ? JSON.parse(message.llm_data) : message.llm_data;
    } catch (err) {
      console.error('Failed to parse LLM data:', err);
    }
  }



  const isUser = message.type === 'user' && !hasToolResult(llmMessage);
  const isTool = message.type === 'tool' || hasToolContent(llmMessage);
  const isError = message.type === 'error';

  // Build a map of tool use IDs to their inputs for linking tool_result back to tool_use
  const toolUseMap: Record<string, { name: string; input: any }> = {};
  if (llmMessage && llmMessage.Content) {
    llmMessage.Content.forEach(content => {
      if (content.Type === 5 && content.ID && content.ToolName) { // tool_use
        toolUseMap[content.ID] = {
          name: content.ToolName,
          input: content.ToolInput
        };
      }
    });
  }

  // Convert Go struct Type field (number) to string type
  // Based on llm/llm.go constants (iota continues across types in same const block):
  // MessageRoleUser = 0, MessageRoleAssistant = 1,
  // ContentTypeText = 2, ContentTypeThinking = 3, ContentTypeRedactedThinking = 4, 
  // ContentTypeToolUse = 5, ContentTypeToolResult = 6
  const getContentType = (type: number): string => {
    switch (type) {
      case 0: return 'message_role_user';    // Should not occur in Content, but handle gracefully
      case 1: return 'message_role_assistant'; // Should not occur in Content, but handle gracefully
      case 2: return 'text';
      case 3: return 'thinking';
      case 4: return 'redacted_thinking';
      case 5: return 'tool_use';
      case 6: return 'tool_result';
      default: return 'unknown';
    }
  };

  const renderContent = (content: LLMContent) => {
    const contentType = getContentType(content.Type);

    switch (contentType) {
      case 'message_role_user':
      case 'message_role_assistant':
        // These shouldn't occur in Content objects, but display as text if they do
        return (
          <div style={{background: '#fff7ed', border: '1px solid #fed7aa', borderRadius: '0.25rem', padding: '0.5rem', fontSize: '0.875rem'}}>
            <div style={{color: '#9a3412', fontFamily: 'monospace'}}>
              [Unexpected message role content: {contentType}]
            </div>
            <div style={{marginTop: '0.25rem'}}>
              {content.Text || JSON.stringify(content)}
            </div>
          </div>
        );
      case 'text':
        return (
          <div className="whitespace-pre-wrap break-words">
            {content.Text || ''}
          </div>
        );
      case 'tool_use':
        // Use specialized component for bash tool
        if (content.ToolName === 'bash') {
          return (
            <BashTool
              toolInput={content.ToolInput}
              isRunning={true}
            />
          );
        }
        // Use specialized component for patch tool
        if (content.ToolName === 'patch') {
          return (
            <PatchTool
              toolInput={content.ToolInput}
              isRunning={true}
            />
          );
        }
        // Default rendering for other tools
        return (
          <div className="tool-use">
            <div className="tool-header">
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem', color: 'var(--blue-text)'}}>
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
              </svg>
              <span className="tool-name">
                Tool: {content.ToolName}
              </span>
            </div>
            <div className="tool-input">
              {typeof content.ToolInput === 'string' 
                ? content.ToolInput 
                : JSON.stringify(content.ToolInput, null, 2)
              }
            </div>
          </div>
        );
      case 'tool_result': {
        const hasError = content.ToolError;
        const toolUseId = content.ToolUseID;
        const startTime = content.ToolUseStartTime;
        const endTime = content.ToolUseEndTime;
        
        // Calculate execution time if available
        let executionTime = '';
        if (startTime && endTime) {
          const start = new Date(startTime).getTime();
          const end = new Date(endTime).getTime();
          const diffMs = end - start;
          if (diffMs < 1000) {
            executionTime = `${diffMs}ms`;
          } else {
            executionTime = `${(diffMs / 1000).toFixed(1)}s`;
          }
        }

        // Get a short summary of the tool result for mobile-friendly display
        const getToolResultSummary = (results: LLMContent[]) => {
          if (!results || results.length === 0) return 'No output';
          
          const firstResult = results[0];
          if (firstResult.Type === 2 && firstResult.Text) { // text content
            const text = firstResult.Text.trim();
            if (text.length <= 50) return text;
            return text.substring(0, 47) + '...';
          }
          
          return `${results.length} result${results.length > 1 ? 's' : ''}`;
        };
        
        const summary = content.ToolResult ? getToolResultSummary(content.ToolResult) : 'No output';
        
        // Get tool information from the toolUseMap or fallback to content
        const toolInfo = toolUseId && toolUseMap && toolUseMap[toolUseId];
        const toolName = (toolInfo && typeof toolInfo === 'object' && toolInfo.name) || content.ToolName || 'Unknown Tool';
        const toolInput = (toolInfo && typeof toolInfo === 'object') ? toolInfo.input : undefined;
        
        // Use specialized component for bash tool
        if (toolName === 'bash') {
          return (
            <BashTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }
        
        // Use specialized component for patch tool
        if (toolName === 'patch') {
          return (
            <PatchTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }
        
        // Default rendering for other tools
        return (
          <details className={`tool-result-details ${hasError ? 'error' : ''}`}>
            <summary className="tool-result-summary">
              <div className="tool-result-meta">
                <div className="flex items-center space-x-2">
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem', color: 'var(--blue-text)'}}>
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                  </svg>
                  <span className="text-sm font-medium text-blue">
                    {toolName}
                  </span>
                  <span className={`tool-result-status text-xs ${hasError ? 'error' : 'success'}`}>
                    {hasError ? '✗' : '✓'} {summary}
                  </span>
                </div>
                <div className="tool-result-time">
                  {executionTime && <span>{executionTime}</span>}
                </div>
              </div>
            </summary>
            <div className="tool-result-content">
              {/* Show tool input */}
              <div className="tool-result-section">
                <div className="tool-result-label">Input:</div>
                <div className="tool-result-data">
                  {toolInput ? (
                    typeof toolInput === 'string' 
                      ? toolInput 
                      : JSON.stringify(toolInput, null, 2)
                  ) : (
                    <span className="text-secondary italic">No input data</span>
                  )}
                </div>
              </div>
              
              {/* Show tool output with header */}
              <div className={`tool-result-section output ${hasError ? 'error' : ''}`}>
                <div className="tool-result-label">Output{hasError ? ' (Error)' : ''}:</div>
                <div className="space-y-2">
                  {content.ToolResult?.map((result, idx) => (
                    <div key={idx}>
                      {renderContent(result)}
                    </div>
                  ))}
                </div>
              </div>
            </div>
          </details>
        );
      }
      case 'redacted_thinking':
        return (
          <div className="text-tertiary italic text-sm">
            [Thinking content hidden]
          </div>
        );
      case 'thinking':
        // Hide thinking content by default in main flow, but could be made expandable
        return null;
      default: {
        // For unknown content types, show the type and try to display useful content
        const displayText = content.Text || content.Data || '';
        const hasMediaType = content.MediaType;
        const hasOtherData = Object.keys(content).some(key => 
          key !== 'Type' && key !== 'ID' && content[key as keyof typeof content]
        );
        
        return (
          <div style={{background: 'var(--bg-tertiary)', border: '1px solid var(--border)', borderRadius: '0.25rem', padding: '0.75rem'}}>
            <div className="text-xs text-secondary" style={{marginBottom: '0.5rem', fontFamily: 'monospace'}}>
              Unknown content type: {contentType} (value: {content.Type})
            </div>
            
            {/* Show media content if available */}
            {hasMediaType && (
              <div style={{marginBottom: '0.5rem'}}>
                <div className="text-xs text-secondary" style={{marginBottom: '0.25rem'}}>Media Type: {content.MediaType}</div>
                {content.MediaType?.startsWith('image/') && content.Data && (
                  <img 
                    src={`data:${content.MediaType};base64,${content.Data}`}
                    alt="Tool output image"
                    className="rounded border"
                    style={{ maxWidth: '100%', height: 'auto', maxHeight: '300px' }}
                  />
                )}
              </div>
            )}
            
            {/* Show text content if available */}
            {displayText && (
              <div className="text-sm whitespace-pre-wrap break-words">
                {displayText}
              </div>
            )}
            
            {/* Show raw JSON for debugging if no text content */}
            {!displayText && hasOtherData && (
              <details className="text-xs">
                <summary className="text-secondary" style={{cursor: 'pointer'}}>
                  Show raw content
                </summary>
                <pre style={{marginTop: '0.5rem', padding: '0.5rem', background: 'var(--bg-base)', borderRadius: '0.25rem', fontSize: '0.75rem', overflow: 'auto'}}>
                  {JSON.stringify(content, null, 2)}
                </pre>
              </details>
            )}
          </div>
        );
      }
    }
  };

  // Render display data for tool-specific rendering
  const renderDisplayData = (toolDisplay: ToolDisplay, toolName?: string) => {
    const display = toolDisplay.display;

    // If this is a screenshot display payload, render the image from the server route
    if (display && typeof display === 'object' && (display as any).type === 'screenshot') {
      const d = display as { id?: string; url?: string; path?: string; selector?: string };
      const url = d.url || (d.path ? `/api/read?path=${encodeURIComponent(d.path)}` : (d.id ? `/api/read?path=${encodeURIComponent(d.id)}` : undefined));
      return (
        <div className="tool-result-details">
          <details open>
            <summary className="tool-result-summary">
              <div className="tool-result-meta">
                <div className="flex items-center space-x-2">
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{ width: '1rem', height: '1rem', color: 'var(--blue-text)' }}>
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M3 7h4l2-2h6l2 2h4v12H3z" />
                  </svg>
                  <span className="text-sm font-medium text-blue">screenshot</span>
                  {d.selector && (
                    <span className="text-xs text-secondary">selector: {d.selector}</span>
                  )}
                </div>
              </div>
            </summary>
            <div className="tool-result-content">
              {url ? (
                <a href={url} target="_blank" rel="noreferrer">
                  <img
                    src={url}
                    alt={d.selector ? `Screenshot of ${d.selector}` : 'Screenshot'}
                    className="rounded border"
                    style={{ maxWidth: '100%', height: 'auto' }}
                  />
                </a>
              ) : (
                <div className="text-secondary text-sm italic">screenshot available</div>
              )}
            </div>
          </details>
        </div>
      );
    }

    // Infer tool type from display content if tool name not provided
    const inferredToolName = toolName || (typeof display === 'string' && display.includes('---') && display.includes('+++') ? 'patch' : undefined);

    // Render based on tool name if available
    if (inferredToolName === 'patch' && typeof display === 'string') {
      // It's likely a unified diff from the patch tool
      return (
        <div className="tool-result-details">
          <details open>
            <summary className="tool-result-summary">
              <div className="tool-result-meta">
                <div className="flex items-center space-x-2">
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem', color: 'var(--blue-text)'}}>
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                  </svg>
                  <span className="text-sm font-medium text-blue">{inferredToolName}</span>
                </div>
              </div>
            </summary>
            <div className="tool-result-content">
              <pre className="diff-display">
                {display}
              </pre>
            </div>
          </details>
        </div>
      );
    }

    // For other types of display data, render as JSON
    return (
      <div className="tool-result-details">
        <details open>
          <summary className="tool-result-summary">
            <div className="tool-result-meta">
              <div className="flex items-center space-x-2">
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" style={{width: '1rem', height: '1rem', color: 'var(--blue-text)'}}>
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                </svg>
                <span className="text-sm font-medium text-blue">{inferredToolName || 'Tool output'}</span>
              </div>
            </div>
          </summary>
          <div className="tool-result-content">
            <pre style={{whiteSpace: 'pre-wrap', wordBreak: 'break-word'}}>
              {JSON.stringify(display, null, 2)}
            </pre>
          </div>
        </details>
      </div>
    );
  };

  const getMessageClasses = () => {
    if (isUser) {
      return 'message message-user';
    }
    if (isError) {
      return 'message message-error';
    }
    if (isTool) {
      return 'message message-tool';
    }
    return 'message message-agent';
  };

  // Special rendering for error messages
  if (isError) {
    let errorText = 'An error occurred';
    if (llmMessage && llmMessage.Content && llmMessage.Content.length > 0) {
      const textContent = llmMessage.Content.find(c => c.Type === 2);
      if (textContent && textContent.Text) {
        errorText = textContent.Text;
      }
    }
    return (
      <div ref={messageRef} className={getMessageClasses()} onMouseEnter={handleMouseEnter} onMouseLeave={handleMouseLeave} onMouseMove={handleMouseMove} style={{position: "relative"}} data-testid="message" role="alert" aria-label="Error message">
        {renderTooltip()}
        <div className="message-content" data-testid="message-content">
          <div className="whitespace-pre-wrap break-words">
            {errorText}
          </div>
        </div>
      </div>
    );
  }

  // If we have display_data, use that for rendering (more compact, tool-specific)
  if (displayData && displayData.length > 0) {
    return (
      <div ref={messageRef} className={getMessageClasses()} onMouseEnter={handleMouseEnter} onMouseLeave={handleMouseLeave} onMouseMove={handleMouseMove} style={{position: "relative"}} data-testid="message" role="article">
        {renderTooltip()}
        <div className="message-content" data-testid="message-content">
          {displayData.map((toolDisplay, index) => (
            <div key={index}>
              {renderDisplayData(toolDisplay, toolDisplay.tool_name)}
            </div>
          ))}
        </div>
      </div>
    );
  }

  // Don't render messages with no meaningful content
  if (!llmMessage || !llmMessage.Content || llmMessage.Content.length === 0) {
    return null;
  }

  // Filter out thinking content, empty content, tool_use, and tool_result
  const meaningfulContent = llmMessage?.Content?.filter(c => {
    const contentType = c.Type;
    // Filter out thinking (3), redacted thinking (4), tool_use (5), tool_result (6), and empty text content
    return contentType !== 3 && contentType !== 4 && contentType !== 5 && contentType !== 6 && (c.Text?.trim() || contentType !== 2); // 3 = thinking, 4 = redacted_thinking, 5 = tool_use, 6 = tool_result, 2 = text
  }) || [];

  if (meaningfulContent.length === 0) {
    return null;
  }

  return (
    <div ref={messageRef} className={getMessageClasses()} onMouseEnter={handleMouseEnter} onMouseLeave={handleMouseLeave} onMouseMove={handleMouseMove} style={{position: "relative"}} data-testid="message" role="article">
      {renderTooltip()}
      {/* Message content */}
      <div className="message-content" data-testid="message-content">
        {meaningfulContent.map((content, index) => (
          <div key={index}>
            {renderContent(content)}
          </div>
        ))}
      </div>
    </div>
  );
}

// Helper functions
function hasToolResult(llmMessage: LLMMessage | null): boolean {
  if (!llmMessage) return false;
  return llmMessage.Content?.some(c => c.Type === 6) ?? false; // 6 = tool_result
}

function hasToolContent(llmMessage: LLMMessage | null): boolean {
  if (!llmMessage) return false;
  return llmMessage.Content?.some(c => c.Type === 5 || c.Type === 6) ?? false; // 5 = tool_use, 6 = tool_result
}

export default Message;
