import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// Review rounds (ADR 0073): publish round 1 and let a reviewer comment on a figure, then change a caption, drop one figure and add
// another, publish round 2. The reviewer opening the new link sees what changed, a marker in words on each figure and the round 1
// comment on the same figure, read-only. Revoking round 1 takes the earlier round's text and comments out of the new link at once.
// Everything the test makes is revoked and archived through the API at the end. Runs after the share-view spec (the name).

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const BOARD = "Rounds board";

// Leaves nothing on the shared instance: revokes the links, archives the board and its figures. Called before the test too, so a
// retry after a failed attempt starts from a clean state and finds no second board or figure of the same name.
async function cleanUp(page: Page) {
  const boards = (await (await page.request.get(`${API}/api/boards`)).json()) as { boards: { id: number; title: string }[] };
  for (const b of boards.boards.filter((x) => x.title === BOARD)) {
    const snaps = (await (await page.request.get(`${API}/api/boards/${b.id}/snapshots`)).json()) as { snapshots: { id: number; revoked: boolean }[] };
    for (const sn of snaps.snapshots) if (!sn.revoked) await page.request.post(`${API}/api/snapshots/${sn.id}/revoke`, { data: {} });
    await page.request.post(`${API}/api/boards/${b.id}/archive`, { data: {} });
  }
  const figs = (await (await page.request.get(`${API}/api/figures`)).json()) as { figures: { id: number; title: string }[] };
  for (const f of figs.figures.filter((x) => /^Rounds [ABC]$/.test(x.title))) await page.request.post(`${API}/api/figures/${f.id}/archive`, { data: {} });
}

async function expectNoAxeViolations(page: Page, skip: string[] = []) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).disableRules(skip).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

async function openCube(page: Page) {
  const file = "hi_cube.fits";
  const row = page.getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first();
  if (!(await row.count())) {
    await page.getByLabel("Register a FITS file").fill(path.join(data, file));
    await page.getByRole("button", { name: "Register", exact: true }).click();
    const offer = page.getByRole("status", { name: "A dataset already holds this content" });
    await expect(row.or(offer)).toBeVisible();
    if (await offer.isVisible()) await offer.getByRole("button", { name: "Register as a separate dataset" }).click();
  }
  await row.click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
}

// The title is drawn into the picture, so each title is a figure of its own even from the same view.
async function saveOnBoard(page: Page, title: string, first: boolean) {
  const section = page.getByRole("button", { name: "Figure", exact: true });
  if ((await section.getAttribute("aria-expanded")) !== "true") await section.click();
  await inspector(page).getByLabel("Title").first().fill(title);
  await inspector(page).getByRole("button", { name: "Save as figure" }).click();
  await expect(inspector(page).getByText(`Saved: “${title}”.`)).toBeVisible();
  if (first) await inspector(page).getByLabel("New board name").fill(BOARD);
  else await inspector(page).getByLabel("Add to board").selectOption({ label: BOARD });
  await inspector(page).getByRole("button", { name: "Add to board" }).click();
  await expect(inspector(page).getByText("Added to the board.")).toBeVisible();
}

// the Boards button toggles: press it only when the boards are not already shown
async function showBoards(page: Page) {
  const button = page.getByRole("button", { name: "Boards", exact: true });
  if ((await button.getAttribute("aria-pressed")) !== "true") await button.click();
  await expect(page.getByLabel("Board", { exact: true })).toBeVisible();
}

async function publishRound(page: Page, label: string): Promise<string> {
  await showBoards(page);
  await page.getByLabel("Board", { exact: true }).selectOption({ label: BOARD });
  await page.getByLabel("Label", { exact: true }).fill(label);
  await page.getByRole("button", { name: "Freeze and create link" }).click();
  const box = page.getByLabel("Review link");
  await expect(box).toBeVisible();
  return box.inputValue();
}

const figure = (page: Page, title: string) => page.getByRole("article").filter({ has: page.getByRole("heading", { name: title, level: 2 }) });

test("round 2 tells the reviewer what changed and carries round 1's comment", async ({ page, browser }) => {
  let boardId = 0;
  const contexts: { close(): Promise<void> }[] = [];
  try {
    await cleanUp(page);
    await page.goto("/");
    await openCube(page);
    await saveOnBoard(page, "Rounds A", true);
    await saveOnBoard(page, "Rounds B", false);
    const link1 = await publishRound(page, "First draft");

    // round 1: a reviewer comments on figure A
    const ctx1 = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    contexts.push(ctx1);
    const r1 = await ctx1.newPage();
    await r1.goto(link1);
    await expect(r1.getByRole("heading", { name: BOARD, level: 1 })).toBeVisible();
    await expect(r1.getByRole("heading", { name: /Changed since/ })).toHaveCount(0);
    await r1.getByLabel("Your name (shown with your comments)").fill("Supervisor");
    await figure(r1, "Rounds A").getByLabel("Comment", { exact: true }).fill("Is the stretch linear here?");
    await figure(r1, "Rounds A").getByRole("button", { name: "Send comment", exact: true }).click();
    await expect(figure(r1, "Rounds A").getByText("Is the stretch linear here?")).toBeVisible();

    // the author edits A's caption, drops B and adds C (the API for the first two: they are single calls; C is saved from the viewer)
    const boards = (await (await page.request.get(`${API}/api/boards`)).json()) as { boards: { id: number; title: string }[] };
    boardId = boards.boards.find((b) => b.title === BOARD)!.id;
    const figs = (await (await page.request.get(`${API}/api/figures`)).json()) as { figures: { id: number; title: string }[] };
    const a = figs.figures.find((f) => f.title === "Rounds A")!;
    expect((await page.request.post(`${API}/api/figures/${a.id}/text`, { data: { title: "Rounds A", caption: "Caption added for round 2" } })).ok()).toBe(true);
    const board = (await (await page.request.get(`${API}/api/boards/${boardId}`)).json()) as { items: { position: number; figure?: { title: string } }[] };
    const b = board.items.find((it) => it.figure?.title === "Rounds B")!;
    expect((await page.request.post(`${API}/api/boards/${boardId}/items/${b.position}/remove`, { data: {} })).ok()).toBe(true);
    await openCube(page);
    await saveOnBoard(page, "Rounds C", false);

    // the author sees, before publishing, which round this will be and what it will say
    await showBoards(page);
    await page.getByLabel("Board", { exact: true }).selectOption({ label: BOARD });
    await page.getByRole("button", { name: "Check what will be shared" }).click();
    const pre = page.getByRole("region", { name: "What the reviewer will see" });
    await expect(pre).toContainText("This will be round 2");
    await expect(pre.getByRole("list", { name: "Changes since the last round" })).toContainText("Removed: Rounds B");
    const link2 = await publishRound(page, "Second draft");

    // round 2: the reviewer sees what changed, in words, and the earlier comment on the same figure
    const ctx2 = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    contexts.push(ctx2);
    const r2 = await ctx2.newPage();
    await r2.goto(link2);
    const since = r2.getByRole("region", { name: /Changed since/ });
    await expect(since).toBeVisible();
    await expect(since).toContainText("Round 2, after “First draft” · 1 new, 1 unchanged, 1 removed.");
    for (const line of ["Caption changed: Rounds A", "Added: Rounds C", "Removed: Rounds B"]) await expect(since.getByText(line, { exact: true })).toBeVisible();
    await expect(figure(r2, "Rounds A")).toContainText("Unchanged picture since round 1");
    await expect(figure(r2, "Rounds A")).toContainText("Earlier caption:");
    await expect(figure(r2, "Rounds C")).toContainText("New since round 1");
    await expect(figure(r2, "Rounds B")).toHaveCount(0);
    const carried = figure(r2, "Rounds A").getByRole("region", { name: "Comments from earlier rounds on Rounds A" });
    await expect(carried).toContainText("Is the stretch linear here?");
    await expect(carried.getByText("Round 1", { exact: true })).toBeVisible();
    await expect(carried.getByRole("textbox")).toHaveCount(0);
    await expect(carried.getByRole("button")).toHaveCount(0);
    await expect(figure(r2, "Rounds C").getByRole("region", { name: /Comments from earlier rounds/ })).toHaveCount(0);
    await expectNoAxeViolations(r2);
    for (const scheme of ["dark", "light"] as const) {
      await r2.emulateMedia({ colorScheme: scheme });
      await expectNoAxeViolations(r2);
    }
    await r2.emulateMedia({ colorScheme: "dark" });
    await r2.setViewportSize({ width: 390, height: 844 });
    await r2.screenshot({ path: test.info().outputPath("review-round-2-390.png"), fullPage: true });
    await r2.setViewportSize({ width: 1280, height: 800 });

    // the author's history numbers the rounds and lists the changes
    await showBoards(page);
    await page.getByLabel("Board", { exact: true }).selectOption({ label: BOARD });
    const second = page.getByRole("listitem").filter({ hasText: "Round 2: Second draft" });
    await expect(second).toBeVisible();
    await second.getByText(/Changed since round 1/).click();
    await expect(second.getByText("Added: Rounds C", { exact: true })).toBeVisible();
    await expect(page.getByRole("listitem").filter({ hasText: "Round 1: First draft" })).toBeVisible();
    await expectNoAxeViolations(page, ["heading-order"]);
    await page.screenshot({ path: test.info().outputPath("rounds-history-dark.png") });

    // revoking round 1 takes its text and comments out of the new link at once; the new round itself stays readable
    await page.getByRole("listitem").filter({ hasText: "Round 1: First draft" }).getByRole("button", { name: "Revoke link" }).click();
    await expect(page.getByRole("listitem").filter({ hasText: "Round 1: First draft" })).toContainText("Revoked");
    await r2.reload();
    await expect(r2.getByRole("region", { name: /Changed since/ })).toContainText("The earlier round is no longer available");
    await expect(r2.getByText("Is the stretch linear here?")).toHaveCount(0);
    await expect(r2.getByText("Earlier caption")).toHaveCount(0);
    await expect(r2.getByText("Removed: Rounds B")).toHaveCount(0);
    await expect(figure(r2, "Rounds A")).toContainText("Unchanged picture since round 1");
    await expectNoAxeViolations(r2);
    await r1.reload();
    await expect(r1.getByRole("alert")).toBeVisible();
  } finally {
    for (const c of contexts) await c.close();
    await cleanUp(page);
  }
});
