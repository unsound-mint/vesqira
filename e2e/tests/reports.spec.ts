import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Reports (ADR 0030): the owner writes and publishes in the app, the supervisor reads and comments on the publish
// server from a named link, and the comment comes back to the owner. Both sides run the real servers and the built
// web app; the publish server is a separate process (start-publish.sh) holding only what was published.
const here = path.dirname(fileURLToPath(import.meta.url));
const PUBLISH = "http://127.0.0.1:8790";

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("write, publish, read from a named link, comment, and answer", async ({ page, browser }) => {
  const key = fs.readFileSync(path.resolve(here, "../.run-publish/key"), "utf8").trim();
  await page.goto("/");
  await page.getByRole("button", { name: "Reports", exact: true }).click();

  // a new report: rename, then write two parts in place
  await page.getByRole("button", { name: "New report" }).first().click();
  const title = page.getByRole("textbox", { name: "Title" });
  await title.fill("Block size 32 vs 64");
  await title.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "Block size 32 vs 64" })).toBeVisible();
  await page.getByRole("button", { name: "Text", exact: true }).click();
  await page.getByRole("textbox", { name: /Edit this part/ }).fill("## Question\n\nDoes the alignment $\\langle \\cos 2\\theta \\rangle$ change with block size?");
  await page.getByRole("textbox", { name: /Edit this part/ }).press("Control+Enter");
  await expect(page.locator("article .katex").first()).toBeVisible();
  await expect(page.getByRole("heading", { level: 2, name: "Question" })).toBeVisible(); // the title is the only h1
  await expectNoAxeViolations(page);

  // publishing needs the publish server: set it up from Share, as a person would
  await page.getByRole("button", { name: "Share" }).click();
  await page.getByLabel("Publish server address").fill(PUBLISH);
  await page.getByLabel("Publisher key").fill(key);
  await page.getByLabel("Your name, as readers see it").fill("Denis");
  await page.getByLabel("Title of the space readers see").fill("LMC reports");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Nobody has a link yet.")).toBeVisible();

  // the preview says what goes out, then publish
  await page.getByRole("button", { name: "Publish…" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("First publication.")).toBeVisible();
  await expect(dialog.getByText(/No data, paths, hosts or scripts/)).toBeVisible();
  await dialog.getByRole("button", { name: "Publish revision 1" }).click();
  await expect(dialog.getByRole("heading", { name: "Published" })).toBeVisible();
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByText("Published · revision 1")).toBeVisible();

  // a link for the supervisor, shown once
  await page.getByLabel("Reader name").fill("Supervisor");
  await page.getByRole("button", { name: "Make link" }).click();
  const link = await page.getByLabel("Reader link").inputValue();
  expect(link.startsWith(`${PUBLISH}/read/`)).toBe(true);

  // ---- the supervisor, in another browser: the report, nothing else ----
  const reader = await (await browser.newContext({ colorScheme: "light" })).newPage();
  const seen: string[] = [];
  reader.on("response", (r) => { if (r.url().includes("/read-api/")) void r.text().then((t) => seen.push(t)).catch(() => {}); });
  await reader.goto(link);
  await expect(reader.getByRole("heading", { level: 1, name: "Block size 32 vs 64" })).toBeVisible();
  await expect(reader.getByText("By Denis")).toBeVisible();
  await expect(reader.locator("article .katex").first()).toBeVisible();
  await expectNoAxeViolations(reader);
  // the keyboard reaches a part's comment button; Escape closes the panel
  await reader.getByRole("button", { name: /Comment on this part/ }).last().focus();
  await reader.keyboard.press("Enter");
  const panel = reader.getByRole("complementary", { name: "Comments" });
  await expect(panel.getByRole("heading", { name: "Comments" })).toBeFocused();
  await panel.getByLabel(/Comment on:/).fill("Is the difference larger than the uncertainty?");
  await panel.getByRole("button", { name: "Comment", exact: true }).click();
  await expect(panel.getByText("Is the difference larger than the uncertainty?")).toBeVisible();
  await expect(panel.getByText("Supervisor", { exact: true })).toBeVisible(); // the link's name, not a typed one
  await reader.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  for (const body of seen) for (const leak of [".run", "/home/", "publisher", key]) expect(body).not.toContain(leak);

  // ---- back to the owner: the comment arrives, the owner answers ----
  await page.getByRole("button", { name: "Close sharing" }).click();
  await page.getByRole("button", { name: /^Comments\s*\d*$/ }).click();
  const comments = page.getByRole("region", { name: "Comments" }); // the shell's canvas is the main landmark, so the panel is a region inside it
  await comments.getByRole("button", { name: "Sync now" }).click();
  await expect(comments.getByText("Is the difference larger than the uncertainty?")).toBeVisible();
  await expect(page.getByRole("button", { name: /1 comment on this part/ })).toBeVisible(); // placed on the paragraph
  await comments.getByRole("textbox", { name: "Reply to Supervisor" }).fill("Not yet: 0.05 against 0.04. I will add block 48.");
  await comments.getByRole("button", { name: "Reply", exact: true }).click();
  await expect(comments.getByText(/I will add block 48/)).toBeVisible();

  // the supervisor sees the answer
  await reader.reload();
  await reader.getByRole("button", { name: /1 comment on this part/ }).click();
  await expect(reader.getByRole("complementary", { name: "Comments" }).getByText(/I will add block 48/)).toBeVisible();
});
