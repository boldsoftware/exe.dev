import { test, expect } from '@playwright/test';

// Create a conversation via the API, wait for the agent to finish,
// then return the conversation slug for direct navigation.
async function createConversation(
  request: any,
  message: string,
  agentTimeout: number = 30000,
): Promise<string> {
  const newResp = await request.post('/api/conversations/new', {
    data: { message, model: 'predictable', cwd: '/tmp' },
  });
  expect(newResp.ok()).toBeTruthy();
  const { conversation_id } = await newResp.json();

  let slug = '';
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversation_id}`);
    const body = await resp.json();
    const done = body.messages?.some(
      (m: { type: string; end_of_turn?: boolean }) =>
        m.type === 'agent' && m.end_of_turn === true,
    );
    expect(done).toBeTruthy();
    slug = body.conversation?.slug || '';
    expect(slug).toBeTruthy();
  }).toPass({ timeout: agentTimeout });

  return slug;
}

test.describe('ANSI escape sequence rendering', () => {
  test('bash output with ANSI colors renders styled text, not raw escapes', async ({ page, request }) => {
    // Run a command that produces ANSI-colored output
    const slug = await createConversation(
      request,
      `bash: printf '\\033[32mGreen\\033[0m \\033[31mRed\\033[0m \\033[1mBold\\033[0m \\033[33mYellow\\033[0m plain'`,
    );

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for the bash tool to complete
    const bashTool = page.locator('.bash-tool[data-testid="tool-call-completed"]');
    await expect(bashTool).toBeVisible({ timeout: 15000 });

    // Expand the tool to see the output
    await bashTool.locator('.bash-tool-header').click();
    const details = bashTool.locator('.bash-tool-details');
    await expect(details).toBeVisible();

    // The output section is the last .bash-tool-code that is NOT .bash-tool-code-cwd
    // and NOT the command section (which doesn't have error class).
    // Find the output <pre> — it's rendered by AnsiText and should have <span> children
    // when ANSI codes are present.
    const outputPre = details.locator('.bash-tool-code').last();
    await expect(outputPre).toBeVisible();

    // The output should NOT contain raw escape characters like [0m or \033
    const textContent = await outputPre.textContent();
    expect(textContent).not.toContain('[0m');
    expect(textContent).not.toContain('[32m');
    expect(textContent).not.toContain('[31m');
    expect(textContent).not.toContain('\x1b');

    // The output SHOULD contain the readable text
    expect(textContent).toContain('Green');
    expect(textContent).toContain('Red');
    expect(textContent).toContain('Bold');
    expect(textContent).toContain('Yellow');
    expect(textContent).toContain('plain');

    // The output should use dangerouslySetInnerHTML with <span> tags for colors
    const innerHTML = await outputPre.innerHTML();
    expect(innerHTML).toContain('<span');
    expect(innerHTML).toContain('style=');
    expect(innerHTML).toContain('color');

    // Verify specific colors are applied via inline styles
    // Green = color:#0A0 (ansi-to-html default for color 2)
    // Red = color:#A00 (ansi-to-html default for color 1)
    const greenSpan = outputPre.locator('span').filter({ hasText: 'Green' });
    await expect(greenSpan).toBeVisible();
    const greenStyle = await greenSpan.getAttribute('style');
    expect(greenStyle).toContain('color');

    const redSpan = outputPre.locator('span').filter({ hasText: 'Red' });
    await expect(redSpan).toBeVisible();
    const redStyle = await redSpan.getAttribute('style');
    expect(redStyle).toContain('color');

    // Bold should be rendered as <b> tag
    const boldTag = outputPre.locator('b').filter({ hasText: 'Bold' });
    await expect(boldTag).toBeVisible();

    // Take a screenshot for visual verification
    await page.screenshot({ path: 'e2e/screenshots/ansi-colors.png', fullPage: true });
  });

  test('bash output without ANSI codes renders as plain text', async ({ page, request }) => {
    const slug = await createConversation(
      request,
      'bash: echo "just plain text with no escapes"',
    );

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Scope to the specific bash tool for this test's echo command to avoid
    // strict-mode violations if the shared test server ends up showing more
    // than one bash invocation.
    const bashTool = page
      .locator('.bash-tool[data-testid="tool-call-completed"]')
      .filter({ hasText: 'just plain text with no escapes' })
      .first();
    await expect(bashTool).toBeVisible({ timeout: 15000 });

    // Expand
    await bashTool.locator('.bash-tool-header').click();
    const details = bashTool.locator('.bash-tool-details');
    await expect(details).toBeVisible();

    const outputPre = details.locator('.bash-tool-code').last();
    await expect(outputPre).toBeVisible();

    // Plain text should be rendered as a text node, not HTML
    const textContent = await outputPre.textContent();
    expect(textContent).toContain('just plain text with no escapes');

    // Should NOT have <span> tags (plain text path)
    const innerHTML = await outputPre.innerHTML();
    expect(innerHTML).not.toContain('<span');
  });
});
