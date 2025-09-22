import { test, expect } from '@playwright/test';

test.describe('Shelley Smoke Tests', () => {
  test('page loads successfully', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    // Just verify the page loads with a title
    const title = await page.title();
    expect(title).toBe('Shelley');
  });

  test('can find message input', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    // Find the textarea for message input
    const messageInput = page.locator('textarea').first();
    await expect(messageInput).toBeVisible();
  });

  test('can find send button', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    // Find any submit button
    const sendButton = page.locator('button[type="submit"]');
    await expect(sendButton).toBeVisible();
  });
});
