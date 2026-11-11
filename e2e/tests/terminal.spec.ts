import { expect, test } from "@playwright/test";
import { API, REVIEW } from "../ports";

// ADR 0029, step 2: the integrated terminal, through the built app, the Vite preview proxy (a WebSocket upgrade that must
// keep the page's Host and Origin) and the real API started with --enable-terminal. Local shell only.
test("the terminal opens a local shell, runs a command, and says plainly when the session ends", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Terminal" }).click();
  const dialog = page.getByRole("dialog", { name: "Terminal" });
  await expect(dialog.getByText("No session", { exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Open session" }).click();
  await expect(dialog.getByText("Live", { exact: true })).toBeVisible();
  // type into the terminal itself, the way a person does; the sum is only in the output, not in what was typed
  await page.locator(".xterm-helper-textarea").focus();
  await page.keyboard.type("echo vq-$((6*7))-done");
  await page.keyboard.press("Enter");
  await expect(dialog.locator(".xterm-rows")).toContainText("vq-42-done");
  await page.keyboard.type("exit");
  await page.keyboard.press("Enter");
  await expect(dialog.getByRole("status")).toContainText("Session ended: shell exited");
  await expect(dialog.getByRole("button", { name: "New session" })).toBeVisible();
  await dialog.getByRole("button", { name: "Close" }).click();
  await expect(dialog).toBeHidden();
});

test("the terminal is on the owner address only, and refuses a foreign origin", async ({ request }) => {
  const caps = await (await request.get(`${API}/api/capabilities`)).json();
  expect(caps.features.terminal).toBe(true);
  const onReview = await request.post(`${REVIEW}/api/terminal/sessions`, { data: { tab: "tab-aaaaaaaa" } });
  expect(onReview.status()).not.toBe(200);
  const foreign = await request.post(`${API}/api/terminal/sessions`, { data: { tab: "tab-aaaaaaaa" }, headers: { Origin: "https://evil.example" } });
  expect(foreign.status()).toBe(403);
});
