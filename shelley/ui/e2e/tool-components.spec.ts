import { test, expect } from '@playwright/test';

test.describe('Tool Component Verification', () => {
  test('all tools use custom components, not GenericTool', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');

    // Send the tool smorgasbord message to trigger all tool types
    await messageInput.fill('tool smorgasbord');
    await sendButton.click();

    // Wait for the response text to appear
    await page.waitForFunction(
      () => document.body.textContent?.includes('Here\'s a sample of all the tools:') ?? false,
      undefined,
      { timeout: 30000 }
    );

    // Wait for all tool calls to complete
    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 9,
      undefined,
      { timeout: 30000 }
    );

    // Verify bash tool uses BashTool component (has bash-tool class)
    const bashTool = page.locator('.bash-tool').first();
    await expect(bashTool).toBeVisible();
    await expect(bashTool.locator('.bash-tool-emoji')).toBeVisible();
    await expect(bashTool.locator('.bash-tool-command')).toBeVisible();

    // Verify thinking content appears (has thinking-content class with 💭 emoji)
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: 'I\'m thinking about the best approach' });
    await expect(thinkingContent.first()).toBeVisible();
    await expect(thinkingContent.locator('text=💭').first()).toBeVisible();

    // Verify patch tool uses PatchTool component (has patch-tool class)
    const patchTool = page.locator('.patch-tool').first();
    await expect(patchTool).toBeVisible();
    await expect(patchTool.locator('.patch-tool-emoji')).toBeVisible();

    // Verify screenshot tool uses ScreenshotTool component (has screenshot-tool class)
    const screenshotTool = page.locator('.screenshot-tool').first();
    await expect(screenshotTool).toBeVisible();
    await expect(screenshotTool.locator('.screenshot-tool-emoji').filter({ hasText: '📷' })).toBeVisible();

    // Verify keyword_search tool uses KeywordSearchTool component (has tool class with search emoji)
    const keywordTool = page.locator('.tool').filter({ hasText: 'find all references' });
    await expect(keywordTool.first()).toBeVisible();
    await expect(keywordTool.locator('.tool-emoji').filter({ hasText: '🔍' }).first()).toBeVisible();

    // Verify browser_navigate tool uses BrowserNavigateTool component (has tool class with globe emoji and URL)
    const navigateTool = page.locator('.tool').filter({ hasText: 'https://example.com' });
    await expect(navigateTool.first()).toBeVisible();
    await expect(navigateTool.locator('.tool-emoji').filter({ hasText: '🌐' }).first()).toBeVisible();

    // Verify browser_eval tool uses BrowserEvalTool component (has tool class with lightning emoji)
    const evalTool = page.locator('.tool').filter({ hasText: 'document.title' });
    await expect(evalTool.first()).toBeVisible();
    await expect(evalTool.locator('.tool-emoji').filter({ hasText: '⚡' }).first()).toBeVisible();

    // Verify read_image tool uses ReadImageTool component (has screenshot-tool class with frame emoji)
    const readImageTool = page.locator('.screenshot-tool').filter({ hasText: '/tmp/image.png' });
    await expect(readImageTool.first()).toBeVisible();
    await expect(readImageTool.locator('.screenshot-tool-emoji').filter({ hasText: '🖼️' }).first()).toBeVisible();

    // Verify browser_recent_console_logs tool uses BrowserConsoleLogsTool component (has tool class with clipboard emoji)
    const consoleTool = page.locator('.tool').filter({ hasText: 'console logs' });
    await expect(consoleTool.first()).toBeVisible();
    await expect(consoleTool.locator('.tool-emoji').filter({ hasText: '📋' }).first()).toBeVisible();

    // CRITICAL: Verify that GenericTool (gear emoji ⚙️) is NOT used for any of these tools
    // We check that NO tool has the generic gear icon
    const genericToolGearEmojis = page.locator('.tool-emoji').filter({ hasText: '⚙️' });
    expect(await genericToolGearEmojis.count()).toBe(0);
  });

  test('bash tool shows command in header', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');

    await messageInput.fill('bash: unique-test-command-xyz123');
    await sendButton.click();

    // Wait for and verify the specific bash tool we just created
    await page.waitForFunction(
      () => document.body.textContent?.includes('unique-test-command-xyz123') ?? false,
      undefined,
      { timeout: 30000 }
    );

    // Verify bash tool shows the command in the header (collapsed state)
    const bashToolWithOurCommand = page.locator('.bash-tool').filter({ hasText: 'unique-test-command-xyz123' });
    await expect(bashToolWithOurCommand).toBeVisible();
    const commandElement = bashToolWithOurCommand.locator('.bash-tool-command');
    await expect(commandElement).toBeVisible();
    const commandText = await commandElement.textContent();
    expect(commandText).toContain('unique-test-command-xyz123');
  });

  test('think tool shows thought prefix in header', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');

    await messageInput.fill('think: This is a long thought that should be truncated in the header display');
    await sendButton.click();

    // Wait for the thinking content to appear
    await page.waitForFunction(
      () => document.body.textContent?.includes("I've considered my approach.") ?? false,
      undefined,
      { timeout: 30000 }
    );

    // Verify thinking content shows the thought text with 💭 emoji
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: 'This is a long thought' }).first();
    await expect(thinkingContent).toBeVisible();
    // The thinking text should be visible
    const thinkingText = await thinkingContent.textContent();
    expect(thinkingText).toContain('This is a long thought');
  });

  test('browser navigate tool shows URL in header', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');

    await messageInput.fill('tool smorgasbord');
    await sendButton.click();

    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 9,
      undefined,
      { timeout: 30000 }
    );

    // Verify browser_navigate tool shows URL in the header
    const navigateTool = page.locator('.tool').filter({ hasText: 'https://example.com' }).first();
    await expect(navigateTool.locator('.tool-command').filter({ hasText: 'https://example.com' })).toBeVisible();
  });

  test('patch tool can be collapsed and expanded without errors', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');

    // Trigger a successful patch tool (uses overwrite operation which always succeeds)
    await messageInput.fill('patch success');
    await sendButton.click();

    // Wait for successful patch tool with Monaco editor
    // Use specific locator to find the successful patch (not the failed ones from other tests)
    const patchTool = page.locator('.patch-tool[data-testid="tool-call-completed"]').filter({ hasText: 'test-patch-success.txt' }).first();
    await expect(patchTool).toBeVisible({ timeout: 30000 });
    // Wait for details section to be fully rendered (starts expanded for successful patches)
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Get console errors before toggling
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    const header = patchTool.locator('.patch-tool-header');

    // Collapse
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeHidden();

    // Expand - diff should reinitialize
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Collapse again
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeHidden();

    // Expand again
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Check no Monaco model errors occurred
    const modelErrors = errors.filter(e => e.includes('model') && e.includes('already exists'));
    expect(modelErrors).toHaveLength(0);
  });

  test('emoji sizes are consistent across all tools', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');

    await messageInput.fill('tool smorgasbord');
    await sendButton.click();

    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 9,
      undefined,
      { timeout: 30000 }
    );

    // Get all tool emojis and check their computed font-size
    const emojiSizes = await page.$$eval(
      '.tool-emoji, .bash-tool-emoji, .patch-tool-emoji, .screenshot-tool-emoji',
      (elements) => elements.map(el => window.getComputedStyle(el).fontSize)
    );

    // All emojis should be 1rem (16px by default)
    // Check that all sizes are the same
    const uniqueSizes = new Set(emojiSizes);
    expect(uniqueSizes.size).toBe(1);

    // Verify the size is 16px (1rem)
    expect(emojiSizes[0]).toBe('16px');
  });
});
