import { expect, type APIRequestContext } from "@playwright/test";

/**
 * Create a conversation via the API, wait for the agent to finish,
 * then return the conversation slug for direct navigation.
 *
 * This avoids a subtle race in the SSE stream handler where a message
 * published between the initial DB query and the Subscribe call can be
 * missed by the browser stream, leaving the UI without the agent reply.
 * By creating the conversation via API and polling until end_of_turn is
 * recorded, tests can then navigate directly to /c/<slug> and rely on
 * the DB-backed initial fetch to deliver all messages.
 */
export async function createConversationViaAPI(
  request: APIRequestContext,
  message: string,
  opts: { agentTimeout?: number; cwd?: string; model?: string } = {},
): Promise<string> {
  const { agentTimeout = 30000, cwd = "/tmp", model = "predictable" } = opts;
  const newResp = await request.post("/api/conversations/new", {
    data: { message, model, cwd },
  });
  expect(newResp.ok()).toBeTruthy();
  const { conversation_id } = await newResp.json();

  let slug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversation_id}`);
    const body = await resp.json();
    const done = body.messages?.some(
      (m: { type: string; end_of_turn?: boolean }) =>
        m.type === "agent" && m.end_of_turn === true,
    );
    expect(done).toBeTruthy();
    slug = body.conversation?.slug || "";
    expect(slug).toBeTruthy();
  }).toPass({ timeout: agentTimeout });

  return slug;
}
