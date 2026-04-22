import { test, expect } from '@playwright/test';
import { createConversationViaAPI } from './helpers';

test.describe('Scroll behavior', () => {
  test('shows scroll-to-bottom button when scrolled up, auto-scrolls when at bottom', async ({ page, request }) => {
    // Seed a conversation with enough content via the API so we don't race
    // with other tests on the shared server (page.goto('/') used to pick up
    // whichever conversation was most recent, often mid-stream).
    const slug = await createConversationViaAPI(request, 'echo message 0');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    const input = page.locator('[data-testid="message-input"]');
    const sendButton = page.locator('[data-testid="send-button"]');
    await expect(input).toBeVisible({ timeout: 30000 });

    // Add more messages to ensure we have scrollable content.
    for (let i = 1; i < 4; i++) {
      await input.fill(`echo message ${i}`);
      await sendButton.click();
      // Wait for the agent reply for this specific message to appear.
      await expect(page.locator(`text=echo message ${i}`).last()).toBeVisible({ timeout: 30000 });
      await expect(page.getByTestId('agent-thinking')).toBeHidden({ timeout: 30000 });
    }

    // Get the messages container
    const messagesContainer = page.locator('.messages-container');

    // Scroll up to the top
    await messagesContainer.evaluate((el) => {
      el.scrollTop = 0;
    });

    // Verify scroll-to-bottom button appears
    const scrollButton = page.locator('.scroll-to-bottom-button');
    await expect(scrollButton).toBeVisible();

    // Click the button
    await scrollButton.click();

    // Button should disappear once we're back at bottom
    await expect(scrollButton).not.toBeVisible();

    // Send another message - should auto-scroll since we're at bottom
    await input.fill('echo final message');
    await sendButton.click();

    // Wait for the user message to appear (predictable is fast, so don't
    // race on the transient agent-thinking indicator).
    await expect(page.locator('text=echo final message').last()).toBeVisible({ timeout: 30000 });

    // Button should not appear since we're following the conversation
    await expect(scrollButton).not.toBeVisible();
  });
});
