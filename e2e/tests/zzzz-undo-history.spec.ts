import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Undo and the exploration history (ADR 0057), through the real API, worker and built app on the generated fixtures: draw a
// region and move a board panel, undo both with the buttons and the keyboard, redo; and continue from an earlier history
// step, which restores that view, branches, and survives a reload.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const channel = (page: Page) => page.getByRole("region", { name: "Channels" }).locator("strong").first();
const spectrum = (page: Page) => page.getByRole("img", { name: /Mean spectrum of the region over 64 channels/ });

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

async function pixelAt(page: Page, w: number, h: number, x: number, y: number) {
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  let box = (await canvas.boundingBox())!;
  for (let i = 0; i < 40; i++) {
    await page.waitForTimeout(100);
    const next = (await canvas.boundingBox())!;
    const stable = Math.abs(next.width - box.width) < 0.5 && Math.abs(next.height - box.height) < 0.5 && next.width > 100;
    box = next;
    if (stable) break;
  }
  return { x: box.x + ((x + 0.5) / w) * box.width, y: box.y + (1 - (y + 0.5) / h) * box.height };
}

async function drawCircle(page: Page) {
  await page.getByRole("button", { name: "Circle", exact: true }).click();
  const a = await pixelAt(page, 32, 24, 20, 9);
  const b = await pixelAt(page, 32, 24, 23, 9);
  await page.mouse.move(a.x, a.y);
  await page.mouse.down();
  await page.mouse.move(b.x, b.y, { steps: 4 });
  await page.mouse.up();
  await expect(spectrum(page)).toBeVisible();
}

async function saveFigureOnBoard(page: Page, title: string, board: string | null) {
  const section = page.getByRole("button", { name: "Figure", exact: true });
  if ((await section.getAttribute("aria-expanded")) !== "true") await section.click();
  await inspector(page).getByLabel("Title").first().fill(title);
  await inspector(page).getByRole("button", { name: "Save as figure" }).click();
  await expect(inspector(page).getByText(`Saved: “${title}”.`)).toBeVisible();
  if (board === null) await inspector(page).getByLabel("Add to board").selectOption({ label: "Undo board" });
  else await inspector(page).getByLabel("New board name").fill(board);
  await inspector(page).getByRole("button", { name: "Add to board" }).click();
  await expect(inspector(page).getByText("Added to the board.")).toBeVisible();
}

const panelTitles = (page: Page) => page.getByRole("list", { name: "Figures on the board" }).getByRole("heading", { level: 3 }).allTextContents();

test("draw a region and move a board panel, undo both, redo, from the buttons and the keyboard", async ({ page }) => {
  await page.goto("/");
  await ensureOpen(page, "hi_cube.fits");
  await drawCircle(page);
  const undo = page.getByRole("group", { name: "Undo and redo for the map" });
  await expect(undo.getByRole("button", { name: "Undo: draw region" })).toBeEnabled();

  // two figures on one board (the first creates it)
  await saveFigureOnBoard(page, "Undo A", "Undo board");
  await saveFigureOnBoard(page, "Undo B", null);

  // the board: adding is a command too; move A down
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await page.getByLabel("Board", { exact: true }).selectOption({ label: "Undo board" });
  await expect(page.getByRole("heading", { name: "Undo B" })).toBeVisible();
  expect(await panelTitles(page)).toEqual(["Undo A", "Undo B"]);
  await page.getByRole("button", { name: "Move down" }).first().click();
  await expect.poll(() => panelTitles(page)).toEqual(["Undo B", "Undo A"]);
  const boardUndo = page.getByRole("group", { name: "Undo and redo for the board" });
  await expect(boardUndo.getByRole("button", { name: "Undo: move panel" })).toBeEnabled();

  // undo the move with the button, redo it with the keyboard, undo again with the keyboard
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/board-undo.png` });
  await boardUndo.getByRole("button", { name: "Undo: move panel" }).click();
  await expect.poll(() => panelTitles(page)).toEqual(["Undo A", "Undo B"]);
  await page.keyboard.press("Control+Shift+Z");
  await expect.poll(() => panelTitles(page)).toEqual(["Undo B", "Undo A"]);
  await page.keyboard.press("Control+Z");
  await expect.poll(() => panelTitles(page)).toEqual(["Undo A", "Undo B"]);
  // removing a panel is undone with the same note and place; a board's own undo does not touch the viewer's
  await page.getByRole("button", { name: "Remove from board" }).first().click();
  await expect.poll(() => panelTitles(page)).toEqual(["Undo B"]);
  await page.keyboard.press("Control+Z");
  await expect.poll(() => panelTitles(page)).toEqual(["Undo A", "Undo B"]);

  // back in the viewer: the region is still the last command of the pane; undo removes it and its spectrum, redo brings it back
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await expect(spectrum(page)).toBeVisible();
  await page.keyboard.press("Control+Z");
  await expect(spectrum(page)).toHaveCount(0);
  await expect(undo.getByRole("button", { name: "Redo: draw region" })).toBeEnabled();
  await page.keyboard.press("Control+Shift+Z");
  await expect(spectrum(page)).toBeVisible();
  await undo.getByRole("button", { name: "Undo: draw region" }).click();
  await expect(spectrum(page)).toHaveCount(0);
  await undo.getByRole("button", { name: "Redo: draw region" }).click();
  await expect(spectrum(page)).toBeVisible();
});

test("continue from an earlier history step: it restores that view, keeps the later steps as a branch, is undoable and survives a reload", async ({ page }) => {
  await page.goto("/");
  await ensureOpen(page, "hi_cube.fits");
  const slider = page.getByRole("slider", { name: "Channel" });
  await slider.focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 20; i++) await page.keyboard.press("ArrowRight");
  const held = (await channel(page).textContent())!.trim(); // the channel the person stays on
  await page.waitForTimeout(1900); // held for more than 1.5 s: a step
  for (let i = 0; i < 12; i++) await page.keyboard.press("ArrowRight");
  const later = (await channel(page).textContent())!.trim();
  expect(later).not.toBe(held);
  await page.waitForTimeout(1900);
  await drawCircle(page);

  await inspector(page).getByRole("button", { name: /^History/ }).click();
  const list = inspector(page).getByRole("list", { name: /Exploration steps/ });
  await expect(list.getByText(`Channel ${Number(held)}`).first()).toBeVisible();
  await expect(list.getByText(`Channel ${Number(later)}`).first()).toBeVisible();
  await expect(list.getByText(/Drew a circle/).first()).toBeVisible();
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/history.png` });
  const before = await list.getByRole("listitem").count();

  // continue from the earlier channel step
  await list.getByRole("button", { name: `Continue from here: Channel ${Number(held)}` }).first().click();
  await expect.poll(async () => (await channel(page).textContent())!.trim()).toBe(held);
  await expect(spectrum(page)).toHaveCount(0); // that step had no region
  await expect(list.getByText(`Continued from “Channel ${Number(held)}”`).first()).toBeVisible();
  expect(await list.getByRole("listitem").count()).toBeGreaterThan(before); // nothing was deleted
  // the next step branches from the new one
  await drawCircle(page);
  await expect(list.getByText(/branched from “Channel/).first()).toBeVisible();

  // continuing is itself undoable: Ctrl+Z (focus out of the slider) goes back to where the person was
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Undo: draw region" })).toBeEnabled();
  await page.getByRole("button", { name: "Undo: draw region" }).click();
  await page.getByRole("button", { name: "Undo: continue from history" }).click();
  await expect.poll(async () => (await channel(page).textContent())!.trim()).toBe(later);

  // the history is stored: after a reload and opening the dataset again, the earlier steps are all there (plus the new opening)
  const count = await list.getByRole("listitem").count();
  await page.reload();
  await ensureOpen(page, "hi_cube.fits");
  await inspector(page).getByRole("button", { name: /^History/ }).click();
  await expect.poll(() => page.getByRole("list", { name: /Exploration steps/ }).getByRole("listitem").count(), { timeout: 15_000 }).toBeGreaterThanOrEqual(count);
  await expect(page.getByRole("list", { name: /Exploration steps/ }).getByText(`Continued from “Channel ${Number(held)}”`).first()).toBeVisible();
});
