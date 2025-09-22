// Types for Shelley UI
import { Conversation as GeneratedConversation, Message as GeneratedMessage, Usage as GeneratedUsage, MessageType as GeneratedMessageType } from './generated-types';

// Re-export generated types
export type Conversation = GeneratedConversation;
export type Usage = GeneratedUsage;
export type MessageType = GeneratedMessageType;

// Extend the generated Message type with parsed data
export interface Message extends Omit<GeneratedMessage, 'type'> {
  type: MessageType;
}

// Go backend LLM struct format (capitalized field names)
export interface LLMMessage {
  Role: number; // 0 = user, 1 = assistant
  Content: LLMContent[];
  ToolUse?: unknown;
}

export interface LLMContent {
  ID: string;
  Type: number; // 2 = text, 3 = tool_use, 4 = tool_result, 5 = thinking
  Text?: string;
  ToolName?: string;
  ToolInput?: unknown;
  ToolResult?: LLMContent[];
  ToolError?: boolean;
  // Other fields from Go struct
  MediaType?: string;
  Thinking?: string;
  Data?: string;
  Signature?: string;
  ToolUseID?: string;
  ToolUseStartTime?: string | null;
  ToolUseEndTime?: string | null;
  Display?: unknown;
  Cache?: boolean;
}

// API types
export interface Model {
  id: string;
  ready: boolean;
}

export interface ChatRequest {
  message: string;
  model?: string;
}
// StreamResponse represents the streaming response format
export interface StreamResponse {
  messages: Message[];
  conversation: Conversation;
}