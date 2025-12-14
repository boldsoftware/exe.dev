// Types for Shelley UI
import {
  Conversation as GeneratedConversation,
  ApiMessageForTS,
  StreamResponseForTS,
  Usage as GeneratedUsage,
  MessageType as GeneratedMessageType,
} from "./generated-types";

// Re-export generated types
export type Conversation = GeneratedConversation;
export type Usage = GeneratedUsage;
export type MessageType = GeneratedMessageType;

// Extend the generated Message type with parsed data
export interface Message extends Omit<ApiMessageForTS, "type"> {
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
  max_context_tokens?: number;
}

export interface ChatRequest {
  message: string;
  model?: string;
  cwd?: string;
}
// StreamResponse represents the streaming response format
export interface StreamResponse extends Omit<StreamResponseForTS, "messages"> {
  messages: Message[];
  total_tokens_used?: number;
}

// Link represents a custom link that can be added to the UI
export interface Link {
  title: string;
  icon_svg?: string; // SVG path data for the icon
  url: string;
}

// InitData is injected into window by the server
export interface InitData {
  models: Model[];
  default_model: string;
  default_cwd?: string;
  hostname?: string;
  terminal_url?: string;
  links?: Link[];
}

// Extend Window interface to include our init data
declare global {
  interface Window {
    __SHELLEY_INIT__?: InitData;
  }
}
