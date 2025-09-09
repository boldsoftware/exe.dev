import React from 'react';
import { Message as MessageType, LLMMessage, LLMContent } from '../types';

interface MessageProps {
  message: MessageType;
}

function Message({ message }: MessageProps) {
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
  const getContentType = (type: number): string => {
    switch (type) {
      case 2: return 'text';
      case 3: return 'tool_use';
      case 4: return 'tool_result';
      case 5: return 'thinking';
      default: return 'unknown';
    }
  };

  const renderContent = (content: LLMContent) => {
    const contentType = getContentType(content.Type);

    switch (contentType) {
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
        return (
          <div className={`border rounded-lg p-3 my-2 ${
            hasError 
              ? 'bg-red-50 dark:bg-red-900/20 border-red-200 dark:border-red-800'
              : 'bg-green-50 dark:bg-green-900/20 border-green-200 dark:border-green-800'
          }`}>
            <div className="flex items-center space-x-2 mb-2">
              <svg 
                className={`w-4 h-4 ${
                  hasError ? 'text-red-600 dark:text-red-400' : 'text-green-600 dark:text-green-400'
                }`} 
                fill="none" 
                stroke="currentColor" 
                viewBox="0 0 24 24"
              >
                {hasError ? (
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
                ) : (
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
                )}
              </svg>
              <span className={`font-medium ${
                hasError ? 'text-red-800 dark:text-red-200' : 'text-green-800 dark:text-green-200'
              }`}>
                Tool Result {hasError ? '(Error)' : ''}
              </span>
            </div>
            <div className="space-y-2">
              {content.ToolResult?.map((result, idx) => (
                <div key={idx}>
                  {renderContent(result)}
                </div>
              ))}
            </div>
          </div>
        );
      }
      case 'thinking':
        // Hide thinking content by default
        return null;
      default:
        return (
          <div className="text-gray-500 dark:text-gray-400 italic">
            [Unsupported content type: {contentType}]
          </div>
        );
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
  const meaningfulContent = llmMessage.Content.filter(c => {
    const contentType = c.Type;
    return contentType !== 5 && (c.Text?.trim() || contentType !== 2); // 5 = thinking, 2 = text
  });

  if (meaningfulContent.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-col space-y-2">
      {/* Message header for non-user messages */}
      {!isUser && (
        <div className="flex items-center space-x-2 text-sm text-gray-500 dark:text-gray-400">
          <span className="font-medium">{getMessageLabel()}</span>
          <span className="text-xs">{formatTime(message.created_at)}</span>
        </div>
      )}
      
      {/* Message content */}
      <div className={getMessageStyles()}>
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
  return llmMessage.Content?.some(c => c.Type === 4) ?? false; // 4 = tool_result
}

function hasToolContent(llmMessage: LLMMessage | null): boolean {
  if (!llmMessage) return false;
  return llmMessage.Content?.some(c => c.Type === 3 || c.Type === 4) ?? false; // 3 = tool_use, 4 = tool_result
}

export default Message;