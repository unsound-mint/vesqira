import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// Channel maps and a position-velocity diagram (ADR 0049), through the real API, worker and built app on the generated
// fixtures: a grid of panels with labelled spectral coordinates and one stated colour scale, saved as a figure that stores
// its channel-grid description; then a line drawn on the map, its position-velocity diagram with offsets in arcsec, and the
// line saved with the dataset.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const datasets = (page: Page) => page.getByRole("list", { name: "Datasets" });

async function ensureRegistered(page: Page, file: string) {
  const row = datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first();
  if (await row.count()) return;
  await page.getByLabel("Register a FITS file").fill(path.join(data, file));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await expect(row).toBeVisible();
}

/** Panels read as they scroll into view; "Load all" reads the rest (it is absent once every panel is loaded). */
async function loadAll(page: Page) {
  const more = page.getByRole("button", { name: "Load all" });
  if (await more.count()) await more.click();
}

test("a grid of channel maps shares one labelled scale and is saved as a figure; a line gives a position-velocity diagram", async ({ page, request }) => {
  await page.goto("/");
  await ensureRegistered(page, "hi_cube.fits");
  await datasets(page).getByRole("button", { name: /^hi_cube\.fits/ }).first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const insp = inspector(page);

  // ----- channel maps -----
  await page.getByRole("tab", { name: "Channel maps" }).click();
  const settings = page.getByRole("form", { name: "Channel map settings" });
  const grid = page.getByTestId("channel-grid");
  await expect(grid).toBeVisible();
  await expect(grid.getByText("Channel 1 · -32000 m/s")).toBeVisible();
  await expect(page.getByText(/All 12 panels share one colour scale and the linear stretch: .* K\./)).toBeVisible();
  await expect(page.getByText(/one scale for every panel/)).toBeVisible();
  await loadAll(page);
  await expect(page.getByRole("status").filter({ hasText: "panels loaded" })).toContainText("12 of 12 panels loaded");
  await expect(grid.getByRole("img", { name: /^Channel 1 · -32000 m\/s, \d+ by \d+ pixels$/ })).toBeVisible();

  // every k-th channel, a mean of two channels per panel, labelled in km/s with the file's own (declared) convention
  await settings.getByLabel("Last channel").fill("64");
  await settings.getByLabel("Every k-th channel").fill("8");
  await settings.getByLabel("Channels averaged per panel").fill("2");
  await settings.getByLabel("Spectral labels").selectOption("km/s");
  await expect(grid.getByText(/^Mean of channels 1–2 · -32 to -31 km\/s$/)).toBeVisible();
  await expect(grid.getByText(/^Mean of channels 9–10 · -24 to -23 km\/s$/)).toBeVisible();
  await expect(page.getByText(/Each panel is the mean of 2 channels/)).toBeVisible();
  await expect(page.getByText(/Spectral labels in km\/s \(radio convention, the file's\)/)).toBeVisible();
  await expect(page.getByText(/overlay.* not drawn on the panels/)).toHaveCount(0); // no overlay besides the base's own regions
  await loadAll(page);
  await expect(page.getByRole("status").filter({ hasText: "panels loaded" })).toContainText("8 of 8 panels loaded");

  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/channel-maps.png` });
  // the grid is checked for accessibility with the controls
  const grids = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).include('[aria-label="Channel maps"]').analyze();
  expect(grids.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);

  await page.getByRole("button", { name: "Save as figure" }).click();
  await expect(page.getByText(/Saved: “hi_cube.fits, channel maps 1–64”/)).toBeVisible();
  const list = (await (await request.get(`${API}/api/figures`)).json()) as { figures: { title: string; scene?: Record<string, unknown>; provenance: { view: { kind: string }; statements: string[] } }[] };
  const fig = list.figures.find((f) => f.title === "hi_cube.fits, channel maps 1–64");
  expect(fig?.provenance.view.kind).toBe("channelgrid");
  expect(fig?.scene).toMatchObject({ schema: "channel-grid/1", first: 0, last: 63, stride: 8, average: 2, panels: 8, spectral: { unit: "km/s", convention: "radio" } });
  expect(fig?.provenance.statements.join("\n")).toContain("share one colour scale");
  expect(JSON.stringify(fig?.provenance)).not.toContain("datasetId");

  // ----- a line on the map and its position-velocity diagram -----
  await page.getByRole("tab", { name: "Map", exact: true }).click();
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  await expect(canvas).toBeVisible();
  await insp.getByRole("group", { name: "Region tool" }).getByRole("button", { name: "Line" }).click();
  const box = (await canvas.boundingBox())!;
  const y = box.y + box.height * 0.55;
  await page.mouse.move(box.x + box.width * 0.2, y);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.5, y, { steps: 5 });
  await page.mouse.move(box.x + box.width * 0.8, y, { steps: 5 });
  await page.mouse.up();
  await expect(page.locator('[data-testid="overlay-marks"] [data-role="region"]').first()).toBeAttached();

  await insp.getByRole("button", { name: /^Position-velocity/ }).click();
  await expect(insp.getByText(/Path of 2 vertices, \d+(\.\d)? pixels long/)).toBeVisible();
  await insp.getByLabel("Width", { exact: true }).fill("3");
  await insp.getByLabel("Spectral axis").selectOption("km/s");
  await insp.getByRole("button", { name: "Compute the diagram" }).click();
  const plot = insp.getByTestId("pv-plot");
  await expect(plot).toBeVisible();
  await expect(insp.getByText(/offsets are cumulative great-circle separation of the sky positions of the samples along the path \(astropy\)/)).toBeVisible();
  await expect(insp.getByText(/Interpolated: bilinear interpolation of the four surrounding pixels/)).toBeVisible();
  await expect(insp.getByText(/3 samples across at 1-pixel spacing/)).toBeVisible();
  await expect(insp.getByText(/offset along the path \(arcsec\), from the first vertex/)).toBeVisible();
  await plot.focus();
  await page.keyboard.press("ArrowRight");
  await expect(insp.getByRole("status").filter({ hasText: "offset " })).toContainText(/offset [0-9.]+ arcsec · radio velocity [-0-9.]+ km\/s \(channel \d+\) · .* · \d of 3 samples across/);
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/pv.png` });
  const pv = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).include('[aria-label="Inspector"]').analyze();
  expect(pv.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);

  // the line is a saved region: it has no area, so it offers no spectrum, and DS9 gets it as a projection
  await insp.getByLabel("Save the current region as").fill("Cut across the source");
  await insp.getByRole("button", { name: "Save the line" }).click();
  await expect(insp.getByText(/line of 2 vertices from/)).toBeVisible();
  const id = (await (await request.get(`${API}/api/datasets`)).json() as { datasets: { id: number; name: string }[] }).datasets.find((d) => d.name === "hi_cube.fits")!.id;
  const saved = (await (await request.get(`${API}/api/datasets/${id}/regions`)).json()) as { regions: { name: string; shape: { shape: string; points: number[][]; width: number; widthUnit: string } }[] };
  const cut = saved.regions.find((r) => r.name === "Cut across the source")!;
  expect(cut.shape).toMatchObject({ shape: "line", width: 3, widthUnit: "pixel" });
  expect(cut.shape.points).toHaveLength(2);
  const ds9 = await (await request.get(`${API}/api/datasets/${id}/regions.reg`)).text();
  expect(ds9).toMatch(/# projection\(\d+,\d+,\d+,\d+,3\) # text=\{Cut across the source\}/);
  expect((await request.get(`${API}/api/datasets/${id}/spectrum?shape=line&x0=0&y0=0&x1=3&y1=3`)).status()).toBe(400);
});
