import { test, expect } from '@playwright/test';

test.describe('Shelley Conversation Tests', () => {
  test('can send Hello and get greeting response', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    // Wait for the message input using improved selector
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    
    // Send "Hello" and expect specific predictable response
    await messageInput.fill('Hello');
    
    // Find and click the send button using improved selector
    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    await sendButton.click();
    
    // Wait for the response from the predictable model
    // The predictable model responds to "Hello" with "Hello! I'm Shelley, your AI assistant. How can I help you today?"
    await page.waitForFunction(
      () => {
        const text = "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 }
    );
    
    // Verify both the user message and assistant response are visible
    await expect(page.locator('text=Hello').first()).toBeVisible();
    await expect(page.locator('text=Hello! I\'m Shelley, your AI assistant. How can I help you today?').first()).toBeVisible();
  });
  
  test('can use echo command', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send "echo: test message" and expect echo response
    await messageInput.fill('echo: test message');
    await sendButton.click();
    
    // The predictable model should echo back "test message"
    await page.waitForFunction(
      () => document.body.textContent?.includes('test message') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Verify both input and output messages are visible
    await expect(page.locator('text=echo: test message')).toBeVisible();
  });
  
  test('responds differently to lowercase hello', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send "hello" (lowercase) and expect different response
    await messageInput.fill('hello');
    await sendButton.click();
    
    // The predictable model responds to "hello" with "Well, hi there!"
    await page.waitForFunction(
      () => document.body.textContent?.includes('Well, hi there!') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Verify the hello message and response are both visible
    await expect(page.getByText('Well, hi there!').first()).toBeVisible();
  });
  
  test('can use bash tool', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send a message that triggers tool use
    await messageInput.fill('bash: echo "hello world"');
    await sendButton.click();
    
    // The predictable model should use the bash tool and show the response
    await page.waitForFunction(
      () => {
        const text = 'I\'ll run the command: echo "hello world"';
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 }
    );
    
    // Verify tool usage appears in the UI with tool indicator
    await expect(page.locator('text=Tool: bash')).toBeVisible({ timeout: 10000 });
  });
  
  test('gives default response for undefined messages', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send an undefined message and expect default response
    await messageInput.fill('this is an undefined message');
    await sendButton.click();
    
    // The predictable model responds to undefined inputs with "edit predictable.go to add a response for that one..."
    await page.waitForFunction(
      () => {
        const text = 'edit predictable.go to add a response for that one...';
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 }
    );
    
    // Verify the undefined message and default response are visible
    await expect(page.locator('text=this is an undefined message')).toBeVisible();
  });
  
  test('conversation persists and displays correctly', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send first message
    await messageInput.fill('Hello');
    await sendButton.click();
    
    // Wait for first response
    await page.waitForFunction(
      () => {
        const text = "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 }
    );
    
    // Send second message
    await messageInput.fill('echo: second message');
    await sendButton.click();
    
    // Wait for second response
    await page.waitForFunction(
      () => document.body.textContent?.includes('second message') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Verify both responses are still visible (conversation persists)
    await expect(page.locator('text=Hello! I\'m Shelley, your AI assistant. How can I help you today?').first()).toBeVisible();
    await expect(page.locator('text=second message').first()).toBeVisible();
  });
  
  test('can send message with Enter key', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    
    // Type message and press Enter
    await messageInput.fill('Hello');
    await messageInput.press('Enter');
    
    // Verify response
    await page.waitForFunction(
      () => {
        const text = "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 }
    );
    
    // Verify the Hello message and response are visible
    await expect(page.locator('text=Hello! I\'m Shelley, your AI assistant. How can I help you today?').first()).toBeVisible();
  });
  
  test('handles think tool correctly', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send a message that triggers think tool
    await messageInput.fill('think: I need to analyze this problem');
    await sendButton.click();
    
    // The predictable model should use the think tool
    await page.waitForFunction(
      () => document.body.textContent?.includes('Let me think about this.') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Verify think tool usage appears in the UI
    await expect(page.locator('text=Tool: think')).toBeVisible({ timeout: 10000 });
  });
  
  test('handles patch tool correctly', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send a message that triggers patch tool
    await messageInput.fill('patch: test.txt');
    await sendButton.click();
    
    // The predictable model should use the patch tool
    await page.waitForFunction(
      () => document.body.textContent?.includes('I\'ll patch the file: test.txt') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Verify patch tool usage appears in the UI
    await expect(page.locator('text=Tool: patch')).toBeVisible({ timeout: 10000 });
  });
  
  test('displays tool results with collapsible details', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Send a bash command that will show tool results
    await messageInput.fill('bash: echo "testing tool results"');
    await sendButton.click();
    
    // Wait for the tool use to appear
    await page.waitForFunction(
      () => document.body.textContent?.includes('Tool: bash') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Check for collapsible tool details (details element)
    const toolDetails = page.locator('details');
    await expect(toolDetails.first()).toBeVisible({ timeout: 10000 });
  });
  
  test('handles multiple consecutive tool calls', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // First tool call: bash
    await messageInput.fill('bash: echo "first command"');
    await sendButton.click();
    
    await page.waitForFunction(
      () => document.body.textContent?.includes('Tool: bash') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Second tool call: think
    await messageInput.fill('think: analyzing the output');
    await sendButton.click();
    
    await page.waitForFunction(
      () => document.body.textContent?.includes('Tool: think') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Third tool call: patch
    await messageInput.fill('patch: example.txt');
    await sendButton.click();
    
    await page.waitForFunction(
      () => document.body.textContent?.includes('Tool: patch') ?? false,
      undefined,
      { timeout: 30000 }
    );
    
    // Verify all the specific messages we sent are visible
    await expect(page.locator('text=bash: echo "first command"')).toBeVisible();
    await expect(page.locator('text=think: analyzing the output')).toBeVisible();
    await expect(page.locator('text=patch: example.txt')).toBeVisible();
    
    // Verify all tool types are visible (just check one instance of each)
    await expect(page.locator('text=Tool: bash').first()).toBeVisible();
    await expect(page.locator('text=Tool: think').first()).toBeVisible();
    await expect(page.locator('text=Tool: patch').first()).toBeVisible();
  });
});
