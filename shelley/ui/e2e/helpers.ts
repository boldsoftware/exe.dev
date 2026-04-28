import { expect, type APIRequestContext } from "@playwright/test";

export interface CreatedConversation {
  conversationId: string;
  slug: string;
}

interface CreateConversationOptions {
  agentTimeout?: number;
  cwd?: string;
  model?: string;
}

function sanitizeSlug(input: string): string {
  return input
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/[^a-z0-9-]+/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 60)
    .replace(/-$/g, "");
}

function buildStableTestSlug(currentSlug: string, conversationId: string): string {
  const uniqueSuffix = conversationId
    .replace(/[^a-z0-9]/gi, "")
    .toLowerCase()
    .slice(0, 8);
  const slugBase = sanitizeSlug(currentSlug) || "conversation";
  const maxBaseLength = Math.max(1, 60 - uniqueSuffix.length - 1);
  const truncatedBase = slugBase.slice(0, maxBaseLength).replace(/-$/g, "");
  return `${truncatedBase || "conversation"}-${uniqueSuffix}`;
}

async function renameConversationForTest(
  request: APIRequestContext,
  conversationId: string,
  currentSlug: string,
): Promise<string> {
  const desiredSlug = buildStableTestSlug(currentSlug, conversationId);
  const renameResp = await request.post(`/api/conversation/${conversationId}/rename`, {
    data: { slug: desiredSlug },
  });
  expect(renameResp.ok()).toBeTruthy();
  const renamedConversation = await renameResp.json();
  return renamedConversation.slug || desiredSlug;
}

/**
 * Poll a conversation until it has a slug. This is used for distillation flows
 * where there is no end_of_turn marker to wait on.
 */
export async function waitForConversationSlug(
  request: APIRequestContext,
  conversationId: string,
  timeout = 30000,
): Promise<string> {
  let slug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversationId}`);
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    slug = body.conversation?.slug || "";
    expect(slug).toBeTruthy();
  }).toPass({ timeout });
  return slug;
}

/**
 * Rename a conversation to a stable unique test slug after background slug
 * generation has completed. This avoids collisions when many tests create
 * predictable-model conversations with the same prompts.
 */
export async function stabilizeConversationSlug(
  request: APIRequestContext,
  conversationId: string,
  currentSlug: string,
): Promise<string> {
  return renameConversationForTest(request, conversationId, currentSlug);
}

/**
 * Create a conversation via the API, wait for the agent to finish, then rename
 * it to a stable unique slug for deterministic direct navigation.
 *
 * This avoids two sources of flake:
 * 1. The SSE subscribe-vs-publish race when the browser opens a brand new
 *    conversation while the first turn is still being recorded.
 * 2. Slug collisions when many predictable-model tests create similar prompts.
 */
export async function createConversationViaAPIWithDetails(
  request: APIRequestContext,
  message: string,
  opts: CreateConversationOptions = {},
): Promise<CreatedConversation> {
  const { agentTimeout = 30000, cwd = "/tmp", model = "predictable" } = opts;
  const newResp = await request.post("/api/conversations/new", {
    data: { message, model, cwd },
  });
  expect(newResp.ok()).toBeTruthy();
  const { conversation_id: conversationId } = await newResp.json();

  let currentSlug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversationId}`);
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    const done = body.messages?.some(
      (m: { type: string; end_of_turn?: boolean }) => m.type === "agent" && m.end_of_turn === true,
    );
    expect(done).toBeTruthy();
    currentSlug = body.conversation?.slug || "";
    expect(currentSlug).toBeTruthy();
  }).toPass({ timeout: agentTimeout });

  const slug = await stabilizeConversationSlug(request, conversationId, currentSlug);
  return { conversationId, slug };
}

export async function createConversationViaAPI(
  request: APIRequestContext,
  message: string,
  opts: CreateConversationOptions = {},
): Promise<string> {
  const { slug } = await createConversationViaAPIWithDetails(request, message, opts);
  return slug;
}
