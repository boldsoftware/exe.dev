export interface Conversation {
  conversation_id: string;
  slug?: string;
  created_at: string;
  updated_at: string;
}

export interface Message {
  message_id: string;
  conversation_id: string;
  type: 'user' | 'agent' | 'tool';
  llm_data?: unknown;
  user_data?: unknown;
  usage_data?: unknown;
  created_at: string;
}

// Go backend struct format (capitalized field names)
export interface LLMMessage {
  Role: number; // 0 = user, 1 = assistant
  Content: LLMContent[];
  ToolUse?: unknown;
}

export interface LLMContent {
  ID: string;
  Type: number; // 2 = text, etc.
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

export interface Model {
  id: string;
  ready: boolean;
}

export interface ChatRequest {
  message: string;
  model?: string;
}