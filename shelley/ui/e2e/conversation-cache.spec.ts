import { test, expect } from '@playwright/test';

/**
 * Helper: create a conversation via the API and wait for the agent to respond.
 * Returns { conversation_id, slug }.
 */
async function createConversation(
  request: import('@playwright/test').APIRequestContext,
  message: string,
): Promise<{ conversation_id: string; slug: string }> {
  const resp = await request.post('/api/conversations/new', {
    data: { message, model: 'predictable' },
  });
  expect(resp.ok()).toBeTruthy();
  const { conversation_id } = await resp.json();

  // Wait for the agent to finish responding and get a slug
  let slug = '';
  await expect(async () => {
    const conv = await request.get(`/api/conversation/${conversation_id}`);
    const body = await conv.json();
    const hasAgent = body.messages?.some((m: { type: string }) => m.type === 'agent');
    expect(hasAgent).toBeTruthy();
    slug = body.conversation?.slug || '';
    expect(slug).toBeTruthy();
  }).toPass({ timeout: 15000 });

  return { conversation_id, slug };
}

/**
 * Helper: wait for text to appear on the page.
 */
async function waitForText(page: import('@playwright/test').Page, text: string, timeout = 15000) {
  await page.waitForFunction(
    (t) => document.body.textContent?.includes(t) ?? false,
    text,
    { timeout },
  );
}

/**
 * Helper: select a conversation by clicking its item in the drawer.
 * Uses exact slug text matching to find the right item.
 */
async function selectConversation(
  page: import('@playwright/test').Page,
  slug: string,
) {
  // Open drawer (mobile: hamburger button)
  const drawerButton = page.locator('button[aria-label="Open conversations"]');
  await drawerButton.click();
  // Wait for drawer to animate open
  await page.waitForTimeout(400);
  // Click the conversation title with exact slug text
  const titleEl = page.locator('.conversation-title').getByText(slug, { exact: true });
  await expect(titleEl).toBeVisible({ timeout: 5000 });
  await titleEl.click();
  // Wait for drawer to close
  await page.waitForTimeout(400);
}

test.describe('Conversation cache', () => {
  test('switching conversations uses cache (no extra fetch on second visit)', async ({
    page,
    request,
  }) => {
    // Create two conversations with distinct messages
    const conv1 = await createConversation(request, 'Hello');
    const conv2 = await createConversation(request, 'hello');

    // Navigate directly to conv1 by slug
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Wait for conversation 1's response
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Switch to conversation 2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Now intercept network requests to verify cache hit.
    // We specifically watch for the full conversation load endpoint
    // (GET /api/conversation/<id> without any further path segments).
    const conversationLoadFetches: string[] = [];
    // Match exactly the full-load endpoint: /api/conversation/<id> with no sub-path
    const loadPattern = new RegExp(`/api/conversation/${conv1.conversation_id}$`);
    page.on('request', (req) => {
      if (loadPattern.test(new URL(req.url()).pathname)) {
        conversationLoadFetches.push(req.url());
      }
    });

    // Switch back to conversation 1
    await selectConversation(page, conv1.slug);

    // Conversation 1 messages should be visible from cache
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Verify no new fetch was made for the full conversation load
    expect(conversationLoadFetches).toHaveLength(0);
  });

  test('cached conversation shows correct messages after streaming updates', async ({
    page,
    request,
  }) => {
    // Create a conversation
    const conv1 = await createConversation(request, 'Hello');

    // Navigate to it
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Send a follow-up message
    await messageInput.fill('echo: follow up message');
    const sendButton = page.getByTestId('send-button');
    await sendButton.click();
    await waitForText(page, 'follow up message');

    // Create a second conversation and switch to it
    const conv2 = await createConversation(request, 'hello');

    // Reload to pick up the new conversation in the list
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Navigate to conv2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Switch back to conv1 — cache should have both original + follow-up
    await selectConversation(page, conv1.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await waitForText(page, 'follow up message');
  });

  test('cache serves messages instantly without loading spinner', async ({
    page,
    request,
  }) => {
    // Create two conversations
    const conv1 = await createConversation(request, 'Hello');
    const conv2 = await createConversation(request, 'hello');

    // Navigate to conv1
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Switch to conv2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Switch back to conv1 — should be instant (cache hit)
    await selectConversation(page, conv1.slug);

    // Messages should already be visible — no spinner
    await page.waitForTimeout(100);
    await expect(
      page.locator("text=Hello! I'm Shelley, your AI assistant.").first(),
    ).toBeVisible();
    // Verify no loading spinner is shown
    await expect(page.locator('.spinner')).toHaveCount(0);
  });
});
