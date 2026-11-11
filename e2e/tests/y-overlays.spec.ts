import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// Overlays from other datasets on a base map (ADR 0042, section 3), through the real API, worker and built app, on the
// generated fixtures: contours of a rotated copy of the cube at k x sigma, a Galactic-frame cube elsewhere on the sky
// (refused: no overlap), the base's beam and a Galactic grid, orientation lines of a declared map, then a figure that
// stores them. Named y- so it runs after the walk-through, which counts figures from 1.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const datasets = (page: Page) => page.getByRole("list", { name: "Datasets" });

async function register(page: Page, file: string) {
  await page.getByLabel("Register a FITS file").fill(path.join(data, file));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await expect(datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first()).toBeVisible();
  await expect(page.getByLabel("Register a FITS file")).toHaveValue("");
}

async function open(page: Page, file: string) {
  await datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
}

const rows = (page: Page) => inspector(page).getByRole("list", { name: /^Overlays/ }).getByRole("listitem");

async function add(page: Page, fill: () => Promise<void>) {
  await inspector(page).getByRole("button", { name: "Add overlay" }).click();
  await fill();
  await inspector(page).getByRole("button", { name: "Add", exact: true }).click();
}

test("overlays from other datasets are placed by WCS, listed with their basis, and saved in a figure", async ({ page, request }) => {
  await page.goto("/");
  for (const f of ["hi_cube.fits", "hi_cube_rotated.fits", "hi_cube_galactic.fits", "vgt_tangential_deg.fits"]) await register(page, f);
  const insp = inspector(page);

  // an orientation map declared with its frame and quantity, so it can be carried onto another grid
  await open(page, "vgt_tangential_deg.fits");
  await insp.getByLabel("Treat this map as").selectOption("axial");
  await insp.getByLabel("Angle unit (required)").selectOption("deg");
  await insp.getByLabel("Angle frame").selectOption("pixel");
  await insp.getByLabel("What the angle is").selectOption("b_field");
  await insp.getByRole("button", { name: "Declare axial" }).click();
  await expect(insp.getByText(/Declared by you as axial orientation/)).toBeVisible();

  await open(page, "hi_cube.fits");
  await page.getByRole("slider", { name: "Channel" }).focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight"); // channel 41: the synthetic source

  // contours of the rotated cube at 3, 5, 10 sigma: traced on its own grid, placed by WCS
  await add(page, async () => {
    await insp.getByLabel("From dataset").selectOption({ label: "hi_cube_rotated.fits" });
    await insp.getByLabel(/^Channel \(1 to 64\)/).fill("41");
  });
  const co = rows(page).filter({ hasText: "Contours of hi_cube_rotated.fits" });
  await expect(co).toContainText(/channel 41 \(8 km\/s\) at 3, 5, 10 × σ \(σ = [0-9.]+ K, 1\.4826 × MAD of that channel, signal included\)/);
  await expect(co).toContainText("placed by WCS geometry (astropy): never resampled onto the base grid; both in ICRS");
  await expect(page.locator('[data-testid="overlay-marks"] [data-role="contour"]').first()).toBeAttached();

  // a cube in the Galactic frame elsewhere on the sky: refused, saying why
  await add(page, async () => {
    await insp.getByLabel("From dataset").selectOption({ label: "hi_cube_galactic.fits" });
    await insp.getByLabel(/^Channel \(1 to 64\)/).fill("41");
  });
  await expect(rows(page).filter({ hasText: "Contours of hi_cube_galactic.fits" })).toContainText(/Not drawn: the contoured map does not overlap the base map on the sky/);

  // the base's own beam from BMAJ, BMIN and BPA, and a Galactic coordinate grid with labels
  await add(page, async () => {
    await insp.getByLabel("Kind").selectOption("beam");
    await insp.getByLabel("Beam of").selectOption({ label: "hi_cube.fits (this map)" });
  });
  await expect(rows(page).filter({ hasText: "Beam of this map" })).toContainText("108″ × 72″, PA 45° from BMAJ, BMIN and BPA of the header of the base map");
  await add(page, async () => {
    await insp.getByLabel("Kind").selectOption("grid");
    await insp.getByLabel("Grid frame").selectOption("galactic");
  });
  await expect(rows(page).filter({ hasText: "Coordinate grid (Galactic)" })).toContainText("after conversion to its ICRS frame");
  await expect(page.locator('text[data-role="grid"]').first()).toBeAttached();

  // orientation lines of the declared map, sampled on its own grid and turned into this map's pixel frame
  await add(page, async () => {
    await insp.getByLabel("Kind").selectOption("orientation");
    await insp.getByLabel("From dataset").selectOption({ label: "vgt_tangential_deg.fits" });
  });
  await expect(rows(page).filter({ hasText: "Orientation lines of vgt_tangential_deg.fits" })).toContainText(/B-field orientation, sampled at every (\d+(st|nd|rd|th) )?pixel of its grid/);

  // reorder, hide and remove from the list
  const names = async () => (await rows(page).allInnerTexts()).map((t) => t.split("\n")[0]!.trim());
  await insp.getByRole("button", { name: "Draw Beam of this map lower (earlier)" }).click();
  expect((await names()).indexOf("Beam of this map")).toBeLessThan((await names()).indexOf("Contours of hi_cube_galactic.fits"));
  await insp.getByRole("button", { name: "Remove Contours of hi_cube_galactic.fits" }).click();
  await expect(rows(page).filter({ hasText: "hi_cube_galactic" })).toHaveCount(0);
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/overlays.png` });

  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).include('[aria-label="Inspector"]').analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);

  // a figure stores the overlays as drawn (the sigma and the step it used) and names the other file in its provenance
  await page.getByRole("button", { name: "Figure", exact: true }).click();
  await insp.getByLabel("Title").first().fill("H I with CO-like contours");
  await insp.getByRole("button", { name: "Save as figure" }).click();
  await expect(insp.getByText(/Saved: “H I with CO-like contours”/)).toBeVisible();
  const list = (await (await request.get(`${API}/api/figures`)).json()) as { figures: { title: string; scene?: { overlays: Record<string, unknown>[] }; provenance: { statements: string[] } }[] };
  const fig = list.figures.find((f) => f.title === "H I with CO-like contours")!;
  const kinds = fig.scene!.overlays.map((o) => o.kind);
  expect(kinds).toEqual(expect.arrayContaining(["contours", "beam", "grid", "orientation"]));
  const contour = fig.scene!.overlays.find((o) => o.kind === "contours") as { levels: { sigma?: number } };
  expect(contour.levels.sigma).toBeGreaterThan(0);
  const lines = fig.scene!.overlays.find((o) => o.kind === "orientation") as { sampling: { mode: string } };
  expect(lines.sampling.mode).toBe("step");
  expect(fig.provenance.statements.join(" ")).toContain("Overlay drawn from another file, hi_cube_rotated.fits");
  const png = await request.get(`${API}/api/figures/${(fig as unknown as { id: number }).id}/preview`);
  expect(png.status()).toBe(200);
  if (process.env.VESQIRA_SHOTS) {
    writeFileSync(`${process.env.VESQIRA_SHOTS}/overlays-figure.png`, await png.body());
    await page.emulateMedia({ colorScheme: "light" });
    await page.setViewportSize({ width: 1280, height: 800 });
    await insp.getByRole("button", { name: "Edit Contours of hi_cube_rotated.fits" }).click();
    await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/overlays-light-1280.png` });
  }
});
