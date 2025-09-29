import React from 'react';
import { Message as MessageType, LLMMessage, LLMContent } from '../types';

interface MessageProps {
  message: MessageType;
  // Tool use information from previous messages to correlate with results
  toolUseMap?: Record<string, {name: string, input: unknown}>;
}

function Message({ message, toolUseMap }: MessageProps) {
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
  const isAssistant = message.type === 'agent';
  const isTool = message.type === 'tool' || hasToolContent(llmMessage);

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
          <div className="bg-orange-50 dark:bg-orange-900/20 border border-orange-200 dark:border-orange-800 rounded p-2 text-sm">
            <div className="text-orange-800 dark:text-orange-200 font-mono">
              [Unexpected message role content: {contentType}]
            </div>
            <div className="text-gray-700 dark:text-gray-300 mt-1">
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
        return (
          <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-lg p-3 my-2">
            <div className="flex items-center space-x-2 mb-2">
              <svg className="w-4 h-4 text-blue-600 dark:text-blue-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
              </svg>
              <span className="font-medium text-blue-800 dark:text-blue-200">
                Tool: {content.ToolName}
              </span>
            </div>
            <div className="text-sm font-mono bg-gray-100 dark:bg-gray-800 rounded p-2 overflow-x-auto">
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
        
        return (
          <details className={`border rounded-lg my-2 ${
            hasError 
              ? 'border-red-200 dark:border-red-800'
              : 'border-gray-200 dark:border-gray-700'
          }`}>
            <summary className={`cursor-pointer px-3 py-2 rounded-lg transition-colors hover:bg-gray-50 dark:hover:bg-gray-800 ${
              hasError 
                ? 'bg-red-50 dark:bg-red-900/20 text-red-800 dark:text-red-200'
                : 'bg-gray-50 dark:bg-gray-800 text-gray-800 dark:text-gray-200'
            }`}>
              <div className="flex items-center justify-between">
                <div className="flex items-center space-x-2">
                  <svg className="w-4 h-4 text-blue-600 dark:text-blue-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                  </svg>
                  <span className="text-sm font-medium text-blue-800 dark:text-blue-200">
                    {toolName}
                  </span>
                  <span className={`text-xs ${
                    hasError ? 'text-red-600 dark:text-red-400' : 'text-green-600 dark:text-green-400'
                  }`}>
                    {hasError ? '✗' : '✓'} {summary}
                  </span>
                </div>
                <div className="text-xs text-gray-500 dark:text-gray-400">
                  {executionTime && <span>{executionTime}</span>}
                </div>
              </div>
            </summary>
            <div className="p-3 pt-0 space-y-3">
              {/* Show tool input */}
              <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded p-2">
                <div className="text-xs font-medium text-blue-800 dark:text-blue-200 mb-1">Input:</div>
                <div className="text-sm font-mono text-blue-900 dark:text-blue-100 overflow-x-auto">
                  {toolInput ? (
                    typeof toolInput === 'string' 
                      ? toolInput 
                      : JSON.stringify(toolInput, null, 2)
                  ) : (
                    <span className="text-gray-500 dark:text-gray-400 italic">No input data</span>
                  )}
                </div>
              </div>
              
              {/* Show tool output with header */}
              <div className={`rounded p-2 ${
                hasError 
                  ? 'bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800'
                  : 'bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800'
              }`}>
                <div className={`text-xs font-medium mb-1 ${
                  hasError ? 'text-red-800 dark:text-red-200' : 'text-green-800 dark:text-green-200'
                }`}>Output{hasError ? ' (Error)' : ''}:</div>
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
          <div className="text-gray-400 dark:text-gray-600 italic text-sm">
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
          <div className="bg-gray-50 dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded p-3">
            <div className="text-xs text-gray-600 dark:text-gray-400 mb-2 font-mono">
              Unknown content type: {contentType} (value: {content.Type})
            </div>
            
            {/* Show media content if available */}
            {hasMediaType && (
              <div className="mb-2">
                <div className="text-xs text-gray-500 dark:text-gray-400 mb-1">Media Type: {content.MediaType}</div>
                {content.MediaType?.startsWith('image/') && content.Data && (
                  <img 
                    src={`data:${content.MediaType};base64,${content.Data}`}
                    alt="Tool output image"
                    className="max-w-full h-auto rounded border"
                    style={{ maxHeight: '300px' }}
                  />
                )}
              </div>
            )}
            
            {/* Show text content if available */}
            {displayText && (
              <div className="text-sm whitespace-pre-wrap break-words text-gray-700 dark:text-gray-300">
                {displayText}
              </div>
            )}
            
            {/* Show raw JSON for debugging if no text content */}
            {!displayText && hasOtherData && (
              <details className="text-xs">
                <summary className="text-gray-500 dark:text-gray-400 cursor-pointer hover:text-gray-700 dark:hover:text-gray-200">
                  Show raw content
                </summary>
                <pre className="mt-2 p-2 bg-gray-100 dark:bg-gray-700 rounded text-xs overflow-x-auto">
                  {JSON.stringify(content, null, 2)}
                </pre>
              </details>
            )}
          </div>
        );
      }
    }
  };

  const getMessageLabel = () => {
    if (isUser) return 'You';
    if (isAssistant) return 'Shelley';
    if (isTool) return 'Tool';
    return 'System';
  };



  const getMessageStyles = () => {
    if (isUser) {
      return 'ml-auto max-w-[80%] bg-primary text-white rounded-lg px-4 py-2';
    }
    return 'mr-auto max-w-full bg-white dark:bg-gray-800 border dark:border-gray-700 rounded-lg px-4 py-3';
  };

  const formatTime = (timestamp: string) => {
    return new Date(timestamp).toLocaleTimeString([], { 
      hour: '2-digit', 
      minute: '2-digit' 
    });
  };

  // Don't render messages with no meaningful content
  if (!llmMessage || !llmMessage.Content || llmMessage.Content.length === 0) {
    return null;
  }

  // Filter out thinking content and empty content
  const meaningfulContent = llmMessage?.Content?.filter(c => {
    const contentType = c.Type;
    // Filter out thinking (3) and redacted thinking (4), and empty text content
    return contentType !== 3 && contentType !== 4 && (c.Text?.trim() || contentType !== 2); // 3 = thinking, 4 = redacted_thinking, 2 = text
  }) || [];

  if (meaningfulContent.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-col space-y-2" data-testid="message" role="article" aria-label={`Message from ${getMessageLabel()}`}>
      {/* Message header for non-user messages */}
      {!isUser && (
        <div className="flex items-center space-x-2 text-sm text-gray-500 dark:text-gray-400">
          <span className="font-medium">{getMessageLabel()}</span>
          <span className="text-xs">{formatTime(message.created_at)}</span>
        </div>
      )}
      
      {/* Message content */}
      <div className={getMessageStyles()} data-testid="message-content">
        {meaningfulContent.map((content, index) => (
          <div key={index}>
            {renderContent(content)}
          </div>
        ))}
      </div>
      
      {/* Timestamp for user messages */}
      {isUser && (
        <div className="text-right">
          <span className="text-xs text-gray-500 dark:text-gray-400">
            {formatTime(message.created_at)}
          </span>
        </div>
      )}
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