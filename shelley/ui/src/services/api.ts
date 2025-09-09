import { Conversation, Message, Model, ChatRequest } from '../types';

class ApiService {
  private baseUrl = '/api';

  async getConversations(): Promise<Conversation[]> {
    const response = await fetch(`${this.baseUrl}/conversations`);
    if (!response.ok) {
      throw new Error(`Failed to get conversations: ${response.statusText}`);
    }
    return response.json();
  }

  async createConversation(): Promise<Conversation> {
    const response = await fetch(`${this.baseUrl}/conversations`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
    });
    if (!response.ok) {
      throw new Error(`Failed to create conversation: ${response.statusText}`);
    }
    return response.json();
  }

  async getMessages(conversationId: string): Promise<Message[]> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}`);
    if (!response.ok) {
      throw new Error(`Failed to get messages: ${response.statusText}`);
    }
    return response.json();
  }

  async sendMessage(conversationId: string, request: ChatRequest): Promise<void> {
    const response = await fetch(`${this.baseUrl}/conversation/${conversationId}/chat`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
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

  async getModels(): Promise<Model[]> {
    const response = await fetch(`${this.baseUrl}/models`);
    if (!response.ok) {
      throw new Error(`Failed to get models: ${response.statusText}`);
    }
    return response.json();
  }
}

export const api = new ApiService();