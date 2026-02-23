import { test, expect } from '@playwright/test';

test.describe('Distill conversation', () => {
  test('spinner transitions to complete after distillation finishes', async ({ page, request }) => {
    // 1. Create a source conversation by sending a message through the API
    const newResp = await request.post('/api/conversations/new', {
      data: { message: 'Hello', model: 'predictable' },
    });
    expect(newResp.ok()).toBeTruthy();
    const { conversation_id: sourceId } = await newResp.json();

    // Wait for the agent to finish responding
    await expect(async () => {
      const conv = await request.get(`/api/conversation/${sourceId}`);
      const body = await conv.json();
      const hasAgent = body.messages?.some((m: { type: string }) => m.type === 'agent');
      expect(hasAgent).toBeTruthy();
    }).toPass({ timeout: 15000 });

    // 2. Distill the conversation via the API
    const distillResp = await request.post('/api/conversations/distill', {
      data: {
        source_conversation_id: sourceId,
        model: 'predictable',
      },
    });
    expect(distillResp.ok()).toBeTruthy();
    const { conversation_id: distilledId } = await distillResp.json();
    expect(distilledId).toBeTruthy();

    // 3. Navigate to the root page. The app auto-selects the most recent
    //    conversation, which is the freshly-created distilled one.
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // 4. Wait for the distill-complete indicator to appear.
    //    The distillation may already be done or still in progress;
    //    either way, we should eventually see "Distilled from".
    const completeIndicator = page.getByTestId('distill-complete');
    await expect(completeIndicator).toBeVisible({ timeout: 30000 });
    await expect(completeIndicator).toContainText('Distilled from');

    // 5. Verify the spinner is gone
    await expect(page.getByTestId('distill-in-progress')).toHaveCount(0);

    // 6. Verify the distilled user message appeared
    await expect(
      page.locator('text=edit predictable.go to add a response for that one...'),
    ).toBeVisible({ timeout: 10000 });
  });
});
