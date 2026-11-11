import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { copyFileSync, openSync, readFileSync, readSync, closeSync, statSync, utimesSync, writeSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// ADR 0042, section 5: a stored figure is drawn again from its scene with the viewer's own renderer, says what changed in
// its files before it does, never replaces the original, and exports SVG whose vector layers are the same marks as the
// screen. The CPU renderer is forced for the pixel comparison, as for the renderer-equivalence test (the GPU path agrees
// with it to one colour step, ADR 0034). Named zzzz- so it runs after the specs that count figures.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const shot = async (page: Page, name: string) => { if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/${name}.png`, fullPage: true }); };
const noAxeViolations = async (page: Page, region: string) => {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).include(`[aria-label="${region}"]`).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
};
const datasets = (page: Page) => page.getByRole("list", { name: "Datasets" });

type Fig = { id: number; title: string; revision: string; previewSha: string; scene?: Record<string, unknown>; identity?: { redrawnFrom?: { figureId: number; revision: string } } };
const figures = async (request: import("@playwright/test").APIRequestContext) => ((await (await request.get(`${API}/api/figures`)).json()) as { figures: Fig[] }).figures;

async function open(page: Page, file: string) {
  await datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
}

/** The SVG of the figure: the vector layers are the same marks as on screen, over the embedded base raster, with units and provenance in text. */
async function expectFigureSvg(page: Page, download: import("@playwright/test").Download, onScreenContours: number, data: string) {
  expect(download.suggestedFilename()).toBe("Redraw-check.svg");
  const text = readFileSync((await download.path())!, "utf8");
  const parsed = await page.evaluate((svg) => {
    const doc = new DOMParser().parseFromString(svg, "image/svg+xml");
    const q = (s: string) => doc.querySelectorAll(s).length;
    return {
      error: doc.querySelector("parsererror")?.textContent ?? null,
      contours: q('[data-role="contour"]'),
      beam: q('path[data-role="beam"]'),
      beamText: doc.querySelector('text[data-role="beam"]')?.textContent ?? "",
      image: doc.querySelector("image")?.getAttributeNS("http://www.w3.org/1999/xlink", "href")?.slice(0, 22) ?? null,
      captions: Array.from(doc.querySelectorAll("text")).map((t) => t.textContent ?? ""),
      meta: doc.querySelector("metadata")?.textContent ?? "",
      title: doc.querySelector("title")?.textContent ?? "",
    };
  }, text);
  expect(parsed.error).toBeNull();
  expect(parsed.contours).toBe(onScreenContours); // one marks path for the screen, the PNG and the SVG
  expect(parsed.beam).toBe(1);
  expect(parsed.beamText).toMatch(/108″ × 72″/);
  expect(parsed.image).toBe("data:image/png;base64,");
  expect(parsed.title).toBe("Redraw check");
  expect(parsed.captions.join("\n")).toMatch(/contours of channel 41 \(8 km\/s\) at 3, 5, 10 × σ/);
  expect(parsed.captions.join("\n")).toContain("K"); // units are in the caption and the colour bar
  const meta = JSON.parse(parsed.meta) as { provenance: { source: { name: string } } };
  expect(meta.provenance.source.name).toBe("redraw_copy.fits");
  expect(text).not.toContain("datasetId");
  expect(text).not.toContain(data); // no path
  expect(text).not.toMatch(/<script|\son[a-z]+=/i);
}

test("a figure is drawn again pixel for pixel, announces a changed file first, keeps the original, and exports SVG", async ({ page, request }) => {
  const copy = path.join(data, "redraw_copy.fits");
  copyFileSync(path.join(data, "hi_cube.fits"), copy);
  for (const f of ["hi_cube.fits", "hi_cube_rotated.fits"]) await request.post(`${API}/api/datasets`, { data: { path: path.join(data, f) } });
  // a byte-for-byte copy of a registered file is offered as a location of it; the figure needs a dataset of its own
  expect((await request.post(`${API}/api/datasets`, { data: { path: copy, asNew: true } })).ok()).toBe(true);
  await page.addInitScript(() => localStorage.setItem("vesqira.renderer", "cpu"));
  await page.goto("/");
  await open(page, "redraw_copy.fits");
  const insp = inspector(page);
  await page.getByRole("slider", { name: "Channel" }).focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight"); // channel 41: the synthetic source

  // contours of the rotated cube (traced on its own grid, placed by WCS) over the copy, and its beam
  await insp.getByRole("button", { name: "Add overlay" }).click();
  await insp.getByLabel("From dataset").selectOption({ label: "hi_cube_rotated.fits" });
  await insp.getByLabel(/^Channel \(1 to 64\)/).fill("41");
  await insp.getByRole("button", { name: "Add", exact: true }).click();
  await expect(insp.getByRole("list", { name: /^Overlays/ }).getByRole("listitem").filter({ hasText: "Contours of hi_cube_rotated.fits" })).toContainText(/σ = [0-9.]+ K/);
  await insp.getByRole("button", { name: "Add overlay" }).click();
  await insp.getByLabel("Kind").selectOption("beam");
  await insp.getByLabel("Beam of").selectOption({ label: "redraw_copy.fits (this map)" });
  await insp.getByRole("button", { name: "Add", exact: true }).click();
  await expect(page.locator('text[data-role="beam"]').first()).toBeAttached();
  const onScreenContours = await page.locator('[data-testid="overlay-marks"] [data-role="contour"]').count();
  expect(onScreenContours).toBeGreaterThan(0);

  // save it as a figure and put it on a board
  await page.getByRole("button", { name: "Figure", exact: true }).click();
  await insp.getByLabel("Title").first().fill("Redraw check");
  await insp.getByRole("button", { name: "Save as figure" }).click();
  await expect(insp.getByText(/Saved: “Redraw check”/)).toBeVisible();
  await insp.getByLabel("New board name").fill("Redraw board");
  await insp.getByRole("button", { name: "Add to board" }).click();
  await expect(insp.getByText("Added to the board.")).toBeVisible();
  const original = (await figures(request)).find((f) => f.title === "Redraw check")!;
  const scene = original.scene as { render?: { canvas: { width: number; height: number }; nodata: number[][]; figureWidth: number } };
  expect(scene.render?.figureWidth).toBe(720);
  expect(scene.render?.canvas.width).toBeGreaterThan(0);
  expect(scene.render?.nodata).toHaveLength(2);

  // the SVG export from the figure panel, right after saving (it asks first like a redrawing; nothing changed, so no question)
  await expect(insp.getByRole("button", { name: "Draw again" })).toHaveCount(0); // a figure just saved has nothing new to draw
  const [panelDownload] = await Promise.all([page.waitForEvent("download"), insp.getByRole("button", { name: "Export SVG" }).click()]);
  await expectFigureSvg(page, panelDownload, onScreenContours, data);
  await expect(insp.getByText("SVG exported.")).toBeVisible();

  // ---- from the board item: unchanged data is drawn again with no question, and the picture has the same pixels as the saved one ----
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  const card = page.getByRole("listitem").filter({ has: page.getByRole("heading", { name: "Redraw check" }) }).first();
  await card.getByRole("button", { name: "Draw again" }).click();
  const drawn = card.getByRole("region", { name: "The figure drawn again" });
  await expect(drawn).toBeVisible({ timeout: 60_000 });
  await expect(drawn).toContainText("Drawn again from unchanged data: the picture has the same pixels as the saved one.");
  await expect(card.getByRole("region", { name: "What changed since this figure was drawn" })).toHaveCount(0);
  await expect(drawn.getByRole("list")).toHaveCount(0); // no note: the scene drawn now is the stored one, sigma included
  await noAxeViolations(page, "The figure drawn again");
  await shot(page, "redraw-same-1440");
  const [download] = await Promise.all([page.waitForEvent("download"), drawn.getByRole("button", { name: "Download SVG" }).click()]);
  await expectFigureSvg(page, download, onScreenContours, data);

  // saving it makes a new revision linked to the original, and the original is untouched
  await drawn.getByRole("button", { name: "Save as a new revision" }).click();
  await expect(drawn.getByText(/Saved as a new revision/)).toBeVisible();
  const after = await figures(request);
  const revised = after.find((f) => f.id !== original.id && f.title === "Redraw check")!;
  expect(revised.identity?.redrawnFrom).toEqual({ figureId: original.id, revision: original.revision });
  expect(revised.revision).not.toBe(original.revision);
  expect(revised.previewSha).toBe(original.previewSha); // the same picture byte for byte: the check does not rest on the app's own sentence
  expect(revised.scene).toEqual(original.scene); // drawn from the same scene, sigma included
  const still = after.find((f) => f.id === original.id)!;
  expect(still).toEqual(original);
  expect(still.identity?.redrawnFrom).toBeUndefined();
  await drawn.getByRole("button", { name: "Close" }).click();

  // ---- changed data: the file is read again with other bytes, and the owner is told per file before anything is drawn ----
  const fd = openSync(copy, "r+");
  // the peak pixel of channel 41, the one the figure shows: header 2880 bytes, 40 channels of 32 x 24 float32, then (x 20, y 9)
  const at = 2880 + 40 * 24 * 32 * 4 + (9 * 32 + 20) * 4;
  expect(statSync(copy).size).toBeGreaterThan(at + 8);
  const b = Buffer.alloc(8);
  readSync(fd, b, 0, 8, at);
  writeSync(fd, Buffer.from(b.map((x) => x ^ 0x5a)), 0, 8, at); // the same size, other bytes
  closeSync(fd);
  const later = new Date(Date.now() + 3_600_000);
  utimesSync(copy, later, later);
  // the file changed on disk and has not been read again: it cannot be drawn, and it says so
  await card.getByRole("button", { name: "Draw again" }).click();
  let region = card.getByRole("region", { name: "What changed since this figure was drawn" });
  await expect(region).toContainText("redraw_copy.fits: the file on disk changed after it was registered (time and fingerprint differ); register it again, then draw again.");
  await expect(region).toContainText("This figure cannot be drawn again until every file it was drawn from can be read. The original PNG is kept.");
  await expect(card.getByRole("button", { name: "Draw anyway" })).toHaveCount(0);
  await noAxeViolations(page, "What changed since this figure was drawn");
  await shot(page, "redraw-blocked-1440");
  await region.getByRole("button", { name: "Close" }).click();

  // read again: now it can be drawn, after the owner agrees
  expect((await request.post(`${API}/api/datasets`, { data: { path: copy } })).ok()).toBe(true);
  await card.getByRole("button", { name: "Draw again" }).click();
  region = card.getByRole("region", { name: "What changed since this figure was drawn" });
  await expect(region).toContainText("redraw_copy.fits: changed since the figure was drawn (time and fingerprint differ).");
  await expect(region).toContainText("Drawing from changed data makes a different figure; the original PNG is kept. Draw it again anyway?");
  await expect(card.getByRole("region", { name: "The figure drawn again" })).toHaveCount(0); // nothing is drawn before the owner agrees
  await noAxeViolations(page, "What changed since this figure was drawn");
  await shot(page, "redraw-ask-1440");
  await page.setViewportSize({ width: 390, height: 844 });
  await shot(page, "redraw-ask-390");
  await page.setViewportSize({ width: 1440, height: 900 });
  await region.getByRole("button", { name: "Cancel" }).click();
  await expect(region).toHaveCount(0);
  await card.getByRole("button", { name: "Draw again" }).click();
  await card.getByRole("button", { name: "Draw anyway" }).click();
  const changed = card.getByRole("region", { name: "The figure drawn again" });
  await expect(changed).toBeVisible({ timeout: 60_000 });
  await expect(changed).toContainText("The picture drawn now is not the same as the saved one.");
  await noAxeViolations(page, "The figure drawn again");
  await shot(page, "redraw-changed-1440");
  await page.setViewportSize({ width: 390, height: 844 });
  await shot(page, "redraw-changed-390");
  await page.setViewportSize({ width: 1440, height: 900 });
  // the new revision can be put on this board, next to the original, and says what it is a redrawing of
  await changed.getByRole("button", { name: "Save as a new revision" }).click();
  await changed.getByRole("button", { name: "Add to this board" }).click();
  await expect(changed.getByText(/Added to this board, after the original/)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Redraw check" })).toHaveCount(2);
  await page.getByText("Where this came from").nth(1).click();
  await expect(page.getByText(/a redrawing of figure \d+ \(revision [0-9a-f]{12}\), which is kept as it was/)).toBeVisible();
  // the original is still the figure on the board, with its picture
  expect((await figures(request)).find((f) => f.id === original.id)).toEqual(original);
  expect((await request.get(`${API}/api/figures/${original.id}/preview`)).status()).toBe(200);
});
