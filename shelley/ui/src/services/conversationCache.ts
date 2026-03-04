import { Message, StreamResponse, Conversation } from "../types";

/**
 * Cached data for a single conversation.
 * Stores everything needed to render the conversation without a network fetch.
 */
export interface CachedConversation {
  messages: Message[];
  contextWindowSize: number;
  conversation: Conversation;
  /** The highest sequence_id we've seen, used for SSE resume */
  lastSequenceId: number;
}

/**
 * LRU cache for conversation data. Keeps up to `maxSize` conversations
 * in memory so switching between them doesn't require a server round-trip.
 *
 * Access order is tracked: the most recently accessed conversation is at
 * the end of the internal Map (Map preserves insertion order).
 */
export class ConversationCache {
  private cache = new Map<string, CachedConversation>();
  private maxSize: number;

  constructor(maxSize = 20) {
    this.maxSize = maxSize;
  }

  /** Get cached data for a conversation, or undefined if not cached. Promotes to MRU. */
  get(conversationId: string): CachedConversation | undefined {
    const entry = this.cache.get(conversationId);
    if (!entry) return undefined;
    // Move to end (most recently used)
    this.cache.delete(conversationId);
    this.cache.set(conversationId, entry);
    return entry;
  }

  /** Check if a conversation is in the cache without promoting it. */
  has(conversationId: string): boolean {
    return this.cache.has(conversationId);
  }

  /** Get cached data without promoting to MRU. Use for updates to existing entries. */
  peek(conversationId: string): CachedConversation | undefined {
    return this.cache.get(conversationId);
  }

  /**
   * Store conversation data from an initial load (full fetch).
   * Evicts the LRU entry if the cache is full.
   */
  set(conversationId: string, response: StreamResponse, lastSequenceId: number): void {
    // Remove first so re-insert puts it at the end
    this.cache.delete(conversationId);
    this.evictIfNeeded();
    this.cache.set(conversationId, {
      messages: response.messages ?? [],
      contextWindowSize: response.context_window_size ?? 0,
      conversation: response.conversation,
      lastSequenceId,
    });
  }

  /**
   * Update cached messages from a streaming update.
   * Merges new messages into existing ones (same logic as ChatInterface).
   * Returns the updated messages array, or undefined if not cached.
   */
  updateMessages(conversationId: string, incomingMessages: Message[]): Message[] | undefined {
    const entry = this.peek(conversationId);
    if (!entry) return undefined;

    const byId = new Map<string, Message>();
    for (const m of entry.messages) byId.set(m.message_id, m);
    for (const m of incomingMessages) byId.set(m.message_id, m);

    const result: Message[] = [];
    for (const m of entry.messages) result.push(byId.get(m.message_id)!);
    for (const m of incomingMessages) {
      if (!entry.messages.find((p) => p.message_id === m.message_id)) result.push(m);
    }

    entry.messages = result;

    // Update lastSequenceId
    if (incomingMessages.length > 0) {
      const maxSeqId = Math.max(...incomingMessages.map((m) => m.sequence_id));
      if (maxSeqId > entry.lastSequenceId) {
        entry.lastSequenceId = maxSeqId;
      }
    }

    return result;
  }

  /** Update context window size for a cached conversation. */
  updateContextWindowSize(conversationId: string, size: number): void {
    const entry = this.peek(conversationId);
    if (entry) {
      entry.contextWindowSize = size;
    }
  }

  /** Update the conversation metadata for a cached conversation. */
  updateConversation(conversationId: string, conversation: Conversation): void {
    const entry = this.peek(conversationId);
    if (entry) {
      entry.conversation = conversation;
    }
  }

  /** Remove a conversation from the cache. */
  delete(conversationId: string): void {
    this.cache.delete(conversationId);
  }

  /** Clear the entire cache. */
  clear(): void {
    this.cache.clear();
  }

  /** Current number of cached conversations. */
  get size(): number {
    return this.cache.size;
  }

  /** Evict the least recently used entry if we're at capacity. */
  private evictIfNeeded(): void {
    while (this.cache.size >= this.maxSize) {
      // Map iterator yields in insertion order; first key is LRU
      const lruKey = this.cache.keys().next().value;
      if (lruKey !== undefined) {
        this.cache.delete(lruKey);
      } else {
        break;
      }
    }
  }
}

// Singleton instance shared across the app
export const conversationCache = new ConversationCache(20);
