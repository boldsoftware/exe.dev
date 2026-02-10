import { test, expect } from "@playwright/test";

// Test that URLs in agent responses are properly linkified
// This test requires the shelley server to be running with predictable model

test("URLs in agent responses should be linkified", async ({ page }) => {
  // Navigate to the app
  await page.goto("/");

  // Wait for the app to load
  await expect(page.locator("[data-testid='message-input']")).toBeVisible();

  // Type a message that will trigger a predictable response with URLs
  await page.locator("[data-testid='message-input']").fill("echo: Check https://example.com and https://test.com");
  
  // Click send
  await page.getByRole("button", { name: "Send message" }).click();
  
  // Wait for response
  await page.waitForSelector(".message-agent", { timeout: 5000 });
  
  // Check that URLs are linkified
  const agentMessage = page.locator(".message-agent .text-link").first();
  await expect(agentMessage).toBeVisible();
  await expect(agentMessage).toHaveAttribute("href", "https://example.com");
  await expect(agentMessage).toHaveAttribute("target", "_blank");
  await expect(agentMessage).toHaveAttribute("rel", "noopener noreferrer");
  
  // Check second URL
  const secondLink = page.locator(".message-agent .text-link").nth(1);
  await expect(secondLink).toBeVisible();
  await expect(secondLink).toHaveAttribute("href", "https://test.com");
});

test("URLs are linkified in user messages too", async ({ page }) => {
  // Navigate to the app
  await page.goto("/");

  // Wait for the app to load
  await expect(page.locator("[data-testid='message-input']")).toBeVisible();

  // Type a message with URLs
  await page.locator("[data-testid='message-input']").fill("echo: Visit https://example.com");
  
  // Click send
  await page.getByRole("button", { name: "Send message" }).click();
  
  // Wait for the user message to appear
  await page.waitForSelector(".message-user", { timeout: 5000 });
  
  // User messages should also be linkified (the UI linkifies all text content)
  const userMessage = page.locator(".message-user").filter({ hasText: 'echo: Visit https://example.com' });
  await expect(userMessage).toContainText("echo: Visit https://example.com");
  
  // URLs in user messages should also be linked
  const link = userMessage.locator("a.text-link");
  await expect(link).toHaveCount(1);
  await expect(link).toHaveAttribute("href", "https://example.com");
});
