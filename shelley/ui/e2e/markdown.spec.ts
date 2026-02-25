import { test, expect } from "@playwright/test";

test.describe("Markdown rendering and sanitization", () => {
  test("renders markdown formatting in agent messages", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill("markdown: **bold** and *italic* and `code`");
    await page.getByTestId("send-button").click();

    // Wait for agent response
    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    // Markdown should be rendered as HTML elements
    await expect(agent.locator("strong")).toContainText("bold");
    await expect(agent.locator("em")).toContainText("italic");
    await expect(agent.locator("code")).toContainText("code");
  });

  test("strips script tags from agent messages", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: hello <script>alert("xss")</script> world',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    // The text should be there, but no script element
    await expect(agent).toContainText("hello");
    await expect(agent).toContainText("world");
    expect(await agent.locator("script").count()).toBe(0);
    // Also confirm the alert text doesn't appear anywhere in the raw HTML
    const html = await agent.innerHTML();
    expect(html).not.toContain("<script");
    expect(html).not.toContain("alert");
  });

  test("strips img tags (remote image tracking)", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      "markdown: ![tracker](https://evil.com/pixel.gif) safe text",
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    expect(await agent.locator("img").count()).toBe(0);
    const html = await agent.innerHTML();
    expect(html).not.toContain("<img");
    expect(html).not.toContain("evil.com");
  });

  test("strips iframe tags", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: <iframe src="https://evil.com"></iframe> safe',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    expect(await agent.locator("iframe").count()).toBe(0);
    await expect(agent).toContainText("safe");
  });

  test("strips event handler attributes", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: <div onclick="alert(1)">click me</div>',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("click me");
    const html = await agent.innerHTML();
    expect(html).not.toContain("onclick");
    expect(html).not.toContain("alert");
  });

  test("sanitizes javascript: href in links", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: <a href="javascript:alert(document.cookie)">steal cookies</a>',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("steal cookies");
    const html = await agent.innerHTML();
    expect(html).not.toContain("javascript:");
  });

  test("markdown links open in new tab with noopener", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      "markdown: [example](https://example.com)",
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const link = page.locator(".message-agent").last().locator("a").first();
    await expect(link).toHaveAttribute("href", "https://example.com");
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  test("user messages never render markdown", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    // Send a message with markdown syntax - user messages should show raw text
    await page.getByTestId("message-input").fill("**bold** and *italic*");
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-user", { timeout: 10000 });

    const user = page.locator(".message-user").last();
    // User message should NOT have <strong> or <em> — should be plain text
    expect(await user.locator("strong").count()).toBe(0);
    expect(await user.locator("em").count()).toBe(0);
    // The raw markdown characters should be visible
    await expect(user).toContainText("**bold**");
  });

  test("strips SVG with embedded script", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: <svg onload="alert(1)"><circle r="50"/></svg> safe',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    const html = await agent.innerHTML();
    expect(html).not.toContain("<svg");
    expect(html).not.toContain("onload");
    await expect(agent).toContainText("safe");
  });

  test("strips non-checkbox input elements (phishing prevention)", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: <input type="text" placeholder="Enter password"> <input type="password"> safe',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    // Text and password inputs should be stripped
    expect(await agent.locator('input[type="text"]').count()).toBe(0);
    expect(await agent.locator('input[type="password"]').count()).toBe(0);
    await expect(agent).toContainText("safe");
  });

  test("strips form and input[type=submit] (phishing prevention)", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.getByTestId("message-input").fill(
      'markdown: <form action="https://evil.com/steal"><button type="submit">Login</button></form> safe',
    );
    await page.getByTestId("send-button").click();

    await page.waitForSelector(".message-agent", { timeout: 10000 });

    const agent = page.locator(".message-agent").last();
    const html = await agent.innerHTML();
    expect(html).not.toContain("<form");
    expect(html).not.toContain("<button");
    expect(html).not.toContain("evil.com");
  });
});
