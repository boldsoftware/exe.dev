import { Conversation, StreamResponse, ChatRequest } from "../types";

class ApiService {
  private baseUrl = "/api";

  async getConversations(): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversations`);
    if (!response.ok) {
      throw new Error(`Failed to get conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async sendMessageWithNewConversation(request: ChatRequest): Promise<{ conversation_id: string }> {
    const response = await fetch(`${this.baseUrl}/conversations/new`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to send message: ${response.statusText}`);
    }
    return response.json();
  }

  async getConversation(conversationId: string): Promise<StreamResponse> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}`);
    if (!response.ok) {
      throw new Error(`Failed to get messages: ${response.statusText}`);
    }
    return response.json();
  }

  async sendMessage(conversationId: string, request: ChatRequest): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/chat`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(request),
    });
    if (!response.ok) {
      throw new Error(`Failed to send message: ${response.statusText}`);
    }
  }

  createMessageStream(conversationId: string): EventSource {
    return new EventSource(`${this.baseUrl}/conversation/${conversationId}/stream`);
  }

  async cancelConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/cancel`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to cancel conversation: ${response.statusText}`);
    }
  }

  async validateCwd(path: string): Promise<{ valid: boolean; error?: string }> {
    const response = await fetch(`${this.baseUrl}/validate-cwd?path=${encodeURIComponent(path)}`);
    if (!response.ok) {
      throw new Error(`Failed to validate cwd: ${response.statusText}`);
    }
    return response.json();
  }

  async listDirectory(path?: string): Promise<{
    path: string;
    parent: string;
    entries: Array<{ name: string; is_dir: boolean }>;
    error?: string;
  }> {
    const url = path
      ? `${this.baseUrl}/list-directory?path=${encodeURIComponent(path)}`
      : `${this.baseUrl}/list-directory`;
    const response = await fetch(url);
    if (!response.ok) {
      throw new Error(`Failed to list directory: ${response.statusText}`);
    }
    return response.json();
  }

  async getArchivedConversations(): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversations/archived`);
    if (!response.ok) {
      throw new Error(`Failed to get archived conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async archiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/archive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to archive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async unarchiveConversation(conversationId: string): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/unarchive`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to unarchive conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async deleteConversation(conversationId: string): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/delete`, {
      method: "POST",
    });
    if (!response.ok) {
      throw new Error(`Failed to delete conversation: ${response.statusText}`);
    }
  }
}

export const api = new ApiService();
