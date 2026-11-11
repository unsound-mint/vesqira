import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// The board layout grid (ADR 0060) through the real API, worker and built app: resize from the keyboard, drag with the mouse so the
// panel snaps and the others settle, undo, reload (the layout is saved), the presentation, the printed page, the reviewer's
// read-only copy, and a narrow screen that stacks in reading order. Axe on the board and on the reviewer's page.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });

async function ensureOpen(page: Page, file: string) {
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

async function saveFigureOnBoard(page: Page, title: string, board: string | null) {
  const section = page.getByRole("button", { name: "Figure", exact: true });
  if ((await section.getAttribute("aria-expanded")) !== "true") await section.click();
  await inspector(page).getByLabel("Title").first().fill(title);
  await inspector(page).getByRole("button", { name: "Save as figure" }).click();
  await expect(inspector(page).getByText(`Saved: “${title}”.`)).toBeVisible();
  if (board === null) await inspector(page).getByLabel("Add to board").selectOption({ label: "Grid board" });
  else await inspector(page).getByLabel("New board name").fill(board);
  await inspector(page).getByRole("button", { name: "Add to board" }).click();
  await expect(inspector(page).getByText("Added to the board.")).toBeVisible();
}

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

const handle = (page: Page, title: string) => page.getByRole("button", { name: `Move or resize “${title}”` });
const live = (page: Page) => page.getByRole("status").filter({ hasText: /moved to|resized to|Cannot/ });

test("arrange a board with the keyboard and the mouse, undo, reload, present, print, share read-only, and stack on a phone", async ({ page, browser }) => {
  await page.goto("/");
  await ensureOpen(page, "hi_cube.fits");
  await saveFigureOnBoard(page, "Grid A", "Grid board");
  await saveFigureOnBoard(page, "Grid B", null);
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await page.getByLabel("Board", { exact: true }).selectOption({ label: "Grid board" });
  await expect(page.getByRole("heading", { name: "Grid B" })).toBeVisible();

  // a board nobody arranged is one panel per row, and nothing was written by looking at it
  await expect(handle(page, "Grid A")).toContainText("column 1, row 1 · 12 × 8");
  await expect(handle(page, "Grid B")).toContainText("column 1, row 9 · 12 × 8");

  // keyboard: resize both to half width, one cell at a time, with the result spoken
  for (const t of ["Grid A", "Grid B"]) {
    await handle(page, t).focus();
    for (let i = 0; i < 6; i++) await page.keyboard.press("Shift+ArrowLeft");
  }
  await expect(handle(page, "Grid A")).toContainText("column 1, row 1 · 6 × 8");
  await expect(handle(page, "Grid B")).toContainText("column 1, row 9 · 6 × 8");
  await expect(live(page)).toContainText("“Grid B” resized to column 1, row 9; 6 columns wide, 8 rows tall.");
  await expect(handle(page, "Grid B")).toBeFocused();

  // mouse: drag B to the right of A; the column guides show only while dragging
  const grid = page.getByRole("list", { name: "Figures on the board" });
  await grid.scrollIntoViewIfNeeded();
  const g = (await grid.boundingBox())!;
  const h = (await handle(page, "Grid B").boundingBox())!;
  await page.mouse.move(h.x + 8, h.y + h.height / 2);
  await page.mouse.down();
  await page.mouse.move(h.x + 8 + g.width / 2, h.y + h.height / 2 - 300, { steps: 8 });
  await expect(grid).toHaveAttribute("data-active", "move");
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/board-grid-drag.png` });
  await page.mouse.up();
  await expect(grid).not.toHaveAttribute("data-active", /.+/);
  await expect(handle(page, "Grid B")).toContainText("column 7, row 1 · 6 × 8");
  await expect(handle(page, "Grid A")).toContainText("column 1, row 1 · 6 × 8");

  // mouse: resize A from its corner, snapping to whole cells
  const cell = page.locator('[data-panel="0"]');
  const c = (await cell.boundingBox())!;
  await cell.hover();
  await page.mouse.move(c.x + c.width - 6, c.y + c.height - 6);
  await page.mouse.down();
  await page.mouse.move(c.x + c.width - 6, c.y + c.height - 6 + 200, { steps: 6 });
  await page.mouse.up();
  await expect(handle(page, "Grid A")).toContainText(/column 1, row 1 · 6 × (10|11|12)/);

  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/board-grid.png` });
  await expectNoAxeViolations(page);

  // one undo per change: the resize, then the drag
  const boardUndo = page.getByRole("group", { name: "Undo and redo for the board" });
  await boardUndo.getByRole("button", { name: "Undo: resize panel" }).click();
  await expect(handle(page, "Grid A")).toContainText("column 1, row 1 · 6 × 8");
  await page.keyboard.press("Control+Z");
  await expect(handle(page, "Grid B")).toContainText("column 1, row 9 · 6 × 8");
  await page.keyboard.press("Control+Shift+Z");
  await expect(handle(page, "Grid B")).toContainText("column 7, row 1 · 6 × 8");

  // the layout is the board's saved state
  await page.reload();
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await page.getByLabel("Board", { exact: true }).selectOption({ label: "Grid board" });
  await expect(handle(page, "Grid B")).toContainText("column 7, row 1 · 6 × 8");

  // presentation and print keep working, in the board's reading order
  await page.getByRole("button", { name: "Present" }).click();
  await expect(page.getByRole("dialog", { name: "Presenting Grid board" })).toBeVisible();
  await expect(page.getByText("1 / 2")).toBeVisible();
  await expect(page.getByRole("dialog").getByText("Grid A").first()).toBeVisible();
  await page.keyboard.press("Escape");
  const printHref = await page.getByRole("link", { name: /Print or save as PDF/ }).getAttribute("href");
  const print = await page.context().newPage();
  await print.goto(`/${printHref}`);
  await expect(print.getByRole("heading", { level: 2 }).first()).toContainText("1. Grid A");
  await print.close();

  // share, and the reviewer sees the saved layout and cannot change it
  await page.getByRole("button", { name: "Freeze and create link" }).click();
  const link = await page.getByLabel("Review link").inputValue();
  const supCtx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const sup = await supCtx.newPage();
  await sup.goto(link);
  await expect(sup.getByRole("heading", { name: "Grid board" })).toBeVisible();
  const [a, b] = [await sup.getByRole("article").filter({ hasText: "Grid A" }).boundingBox(), await sup.getByRole("article").filter({ hasText: "Grid B" }).boundingBox()];
  expect(b!.x).toBeGreaterThan(a!.x + a!.width - 2); // B is to the right of A, as saved
  expect(Math.abs(b!.y - a!.y)).toBeLessThan(2);
  await expect(sup.getByRole("button", { name: /Move or resize/ })).toHaveCount(0);
  await expectNoAxeViolations(sup);
  await sup.setViewportSize({ width: 390, height: 844 });
  const [a2, b2] = [await sup.getByRole("article").filter({ hasText: "Grid A" }).boundingBox(), await sup.getByRole("article").filter({ hasText: "Grid B" }).boundingBox()];
  expect(Math.abs(b2!.x - a2!.x)).toBeLessThan(2); // stacked in reading order on a phone
  expect(b2!.y).toBeGreaterThan(a2!.y);
  await supCtx.close();

  // the owner's board on a phone: stacked, in reading order, with no handles to drag
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await page.getByLabel("Board", { exact: true }).selectOption({ label: "Grid board" });
  await expect(page.getByRole("heading", { name: "Grid B" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Move or resize/ })).toHaveCount(0);
  const cells = page.getByRole("list", { name: "Figures on the board" }).getByRole("listitem");
  const [p0, p1] = [await cells.nth(0).boundingBox(), await cells.nth(1).boundingBox()];
  expect(Math.abs(p1!.x - p0!.x)).toBeLessThan(2);
  expect(p1!.y).toBeGreaterThan(p0!.y);
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/board-grid-narrow.png` });
});
