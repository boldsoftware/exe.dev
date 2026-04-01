import { test, expect } from "@playwright/test";

async function getGitRoot(
  request: import("@playwright/test").APIRequestContext,
): Promise<string> {
  const resp = await request.get("/api/git/diffs?cwd=.");
  expect(resp.ok()).toBeTruthy();
  const data = await resp.json();
  expect(data.gitRoot).toBeTruthy();
  return data.gitRoot;
}

async function createConversation(
  request: import("@playwright/test").APIRequestContext,
  message: string,
  cwd: string,
): Promise<{ conversation_id: string; slug: string }> {
  const resp = await request.post("/api/conversations/new", {
    data: { message, model: "predictable", cwd },
  });
  expect(resp.ok()).toBeTruthy();
  const { conversation_id } = await resp.json();

  let slug = "";
  await expect(async () => {
    const conv = await request.get(`/api/conversation/${conversation_id}`);
    const body = await conv.json();
    const hasAgent = body.messages?.some((m: { type: string }) => m.type === "agent");
    expect(hasAgent).toBeTruthy();
    slug = body.conversation?.slug || "";
    expect(slug).toBeTruthy();
  }).toPass({ timeout: 15000 });

  return { conversation_id, slug };
}

test.describe("Conversation grouping", () => {
  test("active conversation should not be grouped under Other when grouped by git repo", async ({
    page,
    request,
  }) => {
    const gitRoot = await getGitRoot(request);
    await createConversation(request, "Hello from conversation A", gitRoot);
    const active = await createConversation(request, "Hello from conversation B", gitRoot);

    await page.goto(`/c/${active.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    // Open drawer and enable grouping by git repo.
    await page.locator('button[aria-label="Open conversations"]').click();
    await expect(page.locator(".drawer.open")).toBeVisible();
    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Git Repo" }).click();

    // The group containing the active conversation should not be "Other".
    const activeGroup = page.locator(".conversation-group").filter({
      has: page.locator(".conversation-item.active"),
    });
    await expect(activeGroup).toHaveCount(1);

    const activeGroupLabel = (await activeGroup.locator(".conversation-group-label").innerText()).trim();
    expect(activeGroupLabel).not.toBe("Other");
  });
});
