import { test, expect } from '@playwright/test';

// Helper: navigate to root, start a fresh conversation
async function newConversation(page: import('@playwright/test').Page) {
  await page.goto('/new');
  await page.waitForLoadState('domcontentloaded');
  const messageInput = page.getByTestId('message-input');
  await expect(messageInput).toBeVisible({ timeout: 30000 });

  // Click the "+" button in the top header bar (always visible on mobile)
  // to ensure a clean conversation. There may be two, pick the one in the main area.
  const newBtn = page.locator('button[aria-label="New Conversation"]');
  // Only click if it exists (first load has no conversations, so already fresh)
  const count = await newBtn.count();
  if (count > 0) {
    // Click the last one (main header, not sidebar)
    await newBtn.last().click();
    await expect(messageInput).toBeVisible({ timeout: 10000 });
  }

  return messageInput;
}

// Helper: fill input and click send, then wait for the "working..." status.
// On narrow viewports the status text is "working..." (no "Agent " prefix).
async function sendAndWaitForWorking(page: import('@playwright/test').Page, text: string) {
  const messageInput = page.getByTestId('message-input');
  await messageInput.fill(text);
  const sendButton = page.getByTestId('send-button');
  await expect(sendButton).toBeEnabled({ timeout: 5000 });
  await sendButton.tap();
  await page.waitForFunction(
    () => document.body.textContent?.includes('working...') ?? false,
    undefined,
    { timeout: 30000 },
  );
}

// Helper: queue a message via the split-button dropdown
async function queueMessage(page: import('@playwright/test').Page, text: string) {
  const messageInput = page.getByTestId('message-input');
  await messageInput.fill(text);
  const chevron = page.getByTestId('send-options-button');
  await expect(chevron).toBeVisible({ timeout: 5000 });
  await chevron.tap();
  const queueOption = page.getByTestId('queue-option');
  await expect(queueOption).toBeVisible({ timeout: 5000 });
  await queueOption.tap();
}

test.describe('Queue Messages', () => {
  test('split button appears when agent is working', async ({ page }) => {
    await newConversation(page);

    // Send a slow message so the agent stays working
    await sendAndWaitForWorking(page, 'delay: 15');

    // The chevron (send-options-button) should be visible
    const chevron = page.getByTestId('send-options-button');
    await expect(chevron).toBeVisible({ timeout: 5000 });
  });

  test('chevron becomes inactive when agent finishes', async ({ page }) => {
    await newConversation(page);

    // Use a short delay so it finishes quickly
    await sendAndWaitForWorking(page, 'delay: 2');

    const chevron = page.getByTestId('send-options-button');

    // Wait for agent to finish ("Delayed for 2 seconds" response)
    await page.waitForFunction(
      () => document.body.textContent?.includes('Delayed for 2 seconds') ?? false,
      undefined,
      { timeout: 30000 },
    );

    // Type something so the send button is enabled
    const input = page.getByTestId('message-input');
    await input.fill('test');

    // The split button should still be there (constant width),
    // but the chevron should be disabled (agent not working, no queue available)
    await expect(chevron).toBeVisible();
    await expect(chevron).toBeDisabled({ timeout: 10000 });

    // Send button should still be present and enabled
    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).toBeEnabled();
  });

  test('can queue a message via dropdown', async ({ page }) => {
    await newConversation(page);
    await sendAndWaitForWorking(page, 'delay: 15');

    // Queue a message
    await queueMessage(page, 'echo: queued hello');

    // Verify the queued badge appears
    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });
  });

  test('queued message has cancel button', async ({ page }) => {
    await newConversation(page);
    await sendAndWaitForWorking(page, 'delay: 15');

    await queueMessage(page, 'echo: test cancel');

    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });

    const cancelButton = page.getByTestId('cancel-queued');
    await expect(cancelButton).toBeVisible();
  });

  test('can cancel a queued message', async ({ page }) => {
    await newConversation(page);
    await sendAndWaitForWorking(page, 'delay: 60');

    await queueMessage(page, 'echo: to be cancelled');

    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });

    // Click cancel and wait for the server to acknowledge deletion
    const cancelButton = page.getByTestId('cancel-queued');
    await expect(cancelButton).toBeVisible();
    const [cancelResp] = await Promise.all([
      page.waitForResponse(
        (resp) => resp.url().includes('/cancel-queued') && resp.status() === 200,
        { timeout: 10000 },
      ),
      cancelButton.tap(),
    ]);

    // The server deleted the message from the DB.
    // Reload the page to pick up the new state (the SSE stream sends
    // a metadata-only update which doesn't trigger a message list refresh).
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });

    // After reload, the cancelled message and its badge should be gone
    await expect(page.getByTestId('queued-badge')).toHaveCount(0, { timeout: 10000 });
    await expect(page.locator('text=to be cancelled')).toHaveCount(0, { timeout: 10000 });
  });

  test('queued message drains after agent finishes', async ({ page }) => {
    await newConversation(page);

    // Agent busy for ~10s
    await sendAndWaitForWorking(page, 'delay: 10');

    // Queue a message
    await queueMessage(page, 'echo: queued drain test');

    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });

    // Wait for the first agent response (delay finishes)
    await page.waitForFunction(
      () => document.body.textContent?.includes('Delayed for 10 seconds') ?? false,
      undefined,
      { timeout: 30000 },
    );

    // After drain, queued badge should disappear
    await expect(queuedBadge).toBeHidden({ timeout: 15000 });

    // The agent processes the queued message — predictable echoes it back
    await page.waitForFunction(
      () => document.body.textContent?.includes('queued drain test') ?? false,
      undefined,
      { timeout: 30000 },
    );
  });

  test('send button still works normally during agent working', async ({ page }) => {
    await newConversation(page);
    await sendAndWaitForWorking(page, 'delay: 15');

    // Type text and click the MAIN send button (not the dropdown)
    const messageInput = page.getByTestId('message-input');
    await messageInput.fill('echo: immediate send');
    const sendButton = page.getByTestId('send-button');
    await sendButton.tap();

    // Message appears as a normal user message
    await page.waitForFunction(
      () => document.body.textContent?.includes('echo: immediate send') ?? false,
      undefined,
      { timeout: 10000 },
    );

    // No queued badge should exist
    const queuedBadges = page.getByTestId('queued-badge');
    await expect(queuedBadges).toHaveCount(0, { timeout: 3000 });
  });
});
