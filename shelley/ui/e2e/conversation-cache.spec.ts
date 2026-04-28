import { test, expect } from '@playwright/test';
import { createConversationViaAPIWithDetails } from './helpers';

/**
 * Helper: wait for text to appear on the page.
 */
async function waitForText(page: import('@playwright/test').Page, text: string, timeout = 15000) {
  await page.waitForFunction((t) => document.body.textContent?.includes(t) ?? false, text, {
    timeout
  });
}

/**
 * Helper: select a conversation by clicking its item in the drawer.
 * Uses exact slug text matching to find the right item.
 */
async function selectConversation(page: import('@playwright/test').Page, slug: string) {
  // Open drawer (mobile: hamburger button)
  const drawerButton = page.locator('button[aria-label="Open conversations"]');
  await drawerButton.click();
  const drawer = page.locator('.drawer.open');
  await expect(drawer).toBeVisible({ timeout: 5000 });
  // Click the conversation title with exact slug text
  const titleEl = drawer.locator('.conversation-title').getByText(slug, { exact: true });
  await expect(titleEl).toBeVisible({ timeout: 5000 });
  await titleEl.click();
  await expect(drawer).toBeHidden({ timeout: 5000 });
}

test.describe('Conversation cache', () => {
  test('switching conversations uses cache (no extra fetch on second visit)', async ({ page, request }) => {
    // Create two conversations with distinct messages
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

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
    const loadPattern = new RegExp(`/api/conversation/${conv1.conversationId}$`);
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

  test('cached conversation shows correct messages after streaming updates', async ({ page, request }) => {
    // Create a conversation
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');

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
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

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

  test('cache serves messages instantly without loading spinner', async ({ page, request }) => {
    // Create two conversations
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

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

    // Verify no loading spinner is shown
    await expect(page.locator('.spinner')).toHaveCount(0);
    await expect(page.locator("text=Hello! I'm Shelley, your AI assistant.").first()).toBeVisible();
  });
});
