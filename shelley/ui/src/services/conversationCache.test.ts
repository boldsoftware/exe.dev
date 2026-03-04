import { ConversationCache } from "./conversationCache";
import type { StreamResponse, Message, Conversation, MessageType } from "../types";

function makeMessage(id: string, seqId: number, text = "hello"): Message {
  return {
    message_id: id,
    conversation_id: "conv1",
    type: "agent" as MessageType,
    sequence_id: seqId,
    llm_data: text,
    user_data: "",
    created_at: new Date().toISOString(),
  } as Message;
}

function makeConversation(id: string): Conversation {
  return {
    conversation_id: id,
    slug: id,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  } as Conversation;
}

function makeResponse(convId: string, messages: Message[]): StreamResponse {
  return {
    conversation: makeConversation(convId),
    messages,
    context_window_size: 1000,
  } as StreamResponse;
}

interface TestResult {
  passed: number;
  failed: number;
  failures: string[];
}

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

export function runTests(): TestResult {
  const tests: Array<{ name: string; fn: () => void }> = [];
  const results: TestResult = { passed: 0, failed: 0, failures: [] };

  function test(name: string, fn: () => void) {
    tests.push({ name, fn });
  }

  // --- Tests ---

  test("get returns undefined for missing entry", () => {
    const cache = new ConversationCache(5);
    assert(cache.get("missing") === undefined, "should be undefined");
  });

  test("set and get basic round-trip", () => {
    const cache = new ConversationCache(5);
    const msgs = [makeMessage("m1", 1), makeMessage("m2", 2)];
    const resp = makeResponse("conv1", msgs);
    cache.set("conv1", resp, 2);

    const cached = cache.get("conv1");
    assert(cached !== undefined, "should be cached");
    assert(cached!.messages.length === 2, "should have 2 messages");
    assert(cached!.contextWindowSize === 1000, "should have correct context window size");
    assert(cached!.lastSequenceId === 2, "should have correct lastSequenceId");
    assert(cached!.conversation.conversation_id === "conv1", "should have correct conversation");
  });

  test("has returns true for cached, false for missing", () => {
    const cache = new ConversationCache(5);
    assert(!cache.has("conv1"), "should not have conv1");
    cache.set("conv1", makeResponse("conv1", []), 0);
    assert(cache.has("conv1"), "should have conv1");
  });

  test("LRU eviction at capacity", () => {
    const cache = new ConversationCache(3);
    cache.set("a", makeResponse("a", []), 0);
    cache.set("b", makeResponse("b", []), 0);
    cache.set("c", makeResponse("c", []), 0);

    // Cache is full (3). Adding d should evict a (LRU).
    cache.set("d", makeResponse("d", []), 0);
    assert(!cache.has("a"), "a should be evicted");
    assert(cache.has("b"), "b should still be cached");
    assert(cache.has("c"), "c should still be cached");
    assert(cache.has("d"), "d should be cached");
    assert(cache.size === 3, `size should be 3, got ${cache.size}`);
  });

  test("get promotes to MRU", () => {
    const cache = new ConversationCache(3);
    cache.set("a", makeResponse("a", []), 0);
    cache.set("b", makeResponse("b", []), 0);
    cache.set("c", makeResponse("c", []), 0);

    // Access a, making it MRU. Now b is LRU.
    cache.get("a");

    // Adding d should evict b (now LRU).
    cache.set("d", makeResponse("d", []), 0);
    assert(cache.has("a"), "a should still be cached (was promoted)");
    assert(!cache.has("b"), "b should be evicted (was LRU)");
    assert(cache.has("c"), "c should still be cached");
    assert(cache.has("d"), "d should be cached");
  });

  test("updateMessages merges new messages", () => {
    const cache = new ConversationCache(5);
    const msgs = [makeMessage("m1", 1, "first")];
    cache.set("conv1", makeResponse("conv1", msgs), 1);

    const newMsgs = [makeMessage("m2", 2, "second")];
    const result = cache.updateMessages("conv1", newMsgs);

    assert(result !== undefined, "should return merged messages");
    assert(result!.length === 2, `should have 2 messages, got ${result!.length}`);
    assert(result![0].message_id === "m1", "first message preserved");
    assert(result![1].message_id === "m2", "second message appended");

    // lastSequenceId should be updated
    const cached = cache.get("conv1");
    assert(cached!.lastSequenceId === 2, "lastSequenceId should be updated to 2");
  });

  test("updateMessages updates existing messages in place", () => {
    const cache = new ConversationCache(5);
    const msgs = [makeMessage("m1", 1, "original")];
    cache.set("conv1", makeResponse("conv1", msgs), 1);

    const updatedMsg = makeMessage("m1", 1, "updated");
    const result = cache.updateMessages("conv1", [updatedMsg]);

    assert(result !== undefined, "should return merged messages");
    assert(result!.length === 1, "should still have 1 message");
    assert(result![0].llm_data === "updated", "message should be updated");
  });

  test("updateMessages returns undefined for uncached conversation", () => {
    const cache = new ConversationCache(5);
    const result = cache.updateMessages("missing", [makeMessage("m1", 1)]);
    assert(result === undefined, "should return undefined");
  });

  test("updateContextWindowSize updates cached value", () => {
    const cache = new ConversationCache(5);
    cache.set("conv1", makeResponse("conv1", []), 0);
    cache.updateContextWindowSize("conv1", 5000);

    const cached = cache.get("conv1");
    assert(cached!.contextWindowSize === 5000, "should be updated to 5000");
  });

  test("updateConversation updates metadata", () => {
    const cache = new ConversationCache(5);
    cache.set("conv1", makeResponse("conv1", []), 0);

    const updated = makeConversation("conv1");
    updated.slug = "new-slug";
    cache.updateConversation("conv1", updated);

    const cached = cache.get("conv1");
    assert(cached!.conversation.slug === "new-slug", "slug should be updated");
  });

  test("delete removes from cache", () => {
    const cache = new ConversationCache(5);
    cache.set("conv1", makeResponse("conv1", []), 0);
    assert(cache.has("conv1"), "should have conv1");

    cache.delete("conv1");
    assert(!cache.has("conv1"), "should not have conv1 after delete");
    assert(cache.size === 0, "size should be 0");
  });

  test("clear removes all entries", () => {
    const cache = new ConversationCache(5);
    cache.set("a", makeResponse("a", []), 0);
    cache.set("b", makeResponse("b", []), 0);
    cache.set("c", makeResponse("c", []), 0);
    assert(cache.size === 3, "should have 3 entries");

    cache.clear();
    assert(cache.size === 0, "should be empty after clear");
    assert(!cache.has("a"), "a should be gone");
  });

  test("set overwrites existing entry and promotes to MRU", () => {
    const cache = new ConversationCache(3);
    cache.set("a", makeResponse("a", [makeMessage("m1", 1)]), 1);
    cache.set("b", makeResponse("b", []), 0);
    cache.set("c", makeResponse("c", []), 0);

    // Re-set a with new data
    cache.set("a", makeResponse("a", [makeMessage("m1", 1), makeMessage("m2", 2)]), 2);

    const cached = cache.get("a");
    assert(cached!.messages.length === 2, "should have updated messages");
    assert(cached!.lastSequenceId === 2, "should have updated lastSequenceId");

    // Adding d should evict b (now LRU) since a was just re-set
    cache.set("d", makeResponse("d", []), 0);
    assert(cache.has("a"), "a should still be cached");
    assert(!cache.has("b"), "b should be evicted");
  });

  test("peek does not promote to MRU", () => {
    const cache = new ConversationCache(3);
    cache.set("a", makeResponse("a", []), 0);
    cache.set("b", makeResponse("b", []), 0);
    cache.set("c", makeResponse("c", []), 0);

    // Peek at a (should NOT promote it)
    const peeked = cache.peek("a");
    assert(peeked !== undefined, "should find a");
    assert(peeked!.conversation.conversation_id === "a", "should return correct entry");

    // Adding d should evict a (still LRU since peek doesn't promote)
    cache.set("d", makeResponse("d", []), 0);
    assert(!cache.has("a"), "a should be evicted (peek didn't promote)");
    assert(cache.has("b"), "b should still be cached");
  });

  test("capacity of 20 (default)", () => {
    const cache = new ConversationCache();
    for (let i = 0; i < 25; i++) {
      cache.set(`conv${i}`, makeResponse(`conv${i}`, []), 0);
    }
    // Should have at most 20
    assert(cache.size === 20, `size should be 20, got ${cache.size}`);
    // First 5 should be evicted
    for (let i = 0; i < 5; i++) {
      assert(!cache.has(`conv${i}`), `conv${i} should be evicted`);
    }
    // Last 20 should be present
    for (let i = 5; i < 25; i++) {
      assert(cache.has(`conv${i}`), `conv${i} should be present`);
    }
  });

  // --- Run tests ---
  for (const t of tests) {
    try {
      t.fn();
      results.passed++;
    } catch (err: unknown) {
      results.failed++;
      results.failures.push(
        `FAIL: ${t.name}\n  ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  }

  return results;
}
