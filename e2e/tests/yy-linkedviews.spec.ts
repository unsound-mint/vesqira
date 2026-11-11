import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Linked views (ADR 0042, section 4), through the real API, worker and built app on the generated fixtures: a cube on an ICRS
// grid compared with the same field on a Galactic grid (different grids, so not linked until the person links them by
// WCS), the cursor marker in the other pane, "outside the footprint" at the edge, a channel followed by spectral value,
// a channel link refused when the spectral frames differ, and leaving and rejoining the group.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const datasets = (page: Page) => page.getByRole("list", { name: "Datasets" });

async function ensureRegistered(page: Page, file: string) {
  const row = datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first();
  if (await row.count()) return;
  await page.getByLabel("Register a FITS file").fill(path.join(data, file));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await expect(row).toBeVisible();
}

const pane = (page: Page, name: string) => page.getByRole("region", { name: `Map: ${name}` });
const marker = (p: ReturnType<typeof pane>) => p.locator('span[aria-hidden="true"][style*="left"][style*="top"]');

/** Move the pointer to a fraction of a pane's canvas. */
async function hover(page: Page, name: string, fx: number, fy: number) {
  const box = (await pane(page, name).locator('canvas[aria-label*="pixels"]').first().boundingBox())!;
  await page.mouse.move(box.x + box.width * fx, box.y + box.height * fy);
}

test("different grids link by WCS: cursor marker, footprint, channel by value, a refused frame, and unlinking", async ({ page }) => {
  await page.goto("/");
  for (const f of ["hi_cube.fits", "hi_cube_galactic_overlap.fits", "hi_cube_topocentric.fits"]) await ensureRegistered(page, f);
  await datasets(page).getByRole("button", { name: /^hi_cube\.fits/ }).first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();

  // different grids: not linked, nothing is resampled, until the person asks for the WCS link
  await page.getByLabel("Compare with").selectOption({ label: "hi_cube_galactic_overlap.fits" });
  await expect(page.getByText(/not linked, nothing is resampled/)).toBeVisible();
  await page.getByRole("button", { name: "Link by WCS" }).click();
  await expect(page.getByText(/Linked by WCS: frames ICRS↔Galactic \(astropy\), cursor within [0-9<. ]+ px in this view/)).toBeVisible();
  await expect(page.getByText("Channel linked by spectral value")).toBeVisible();
  await expect(page.getByLabel("Group name")).toHaveValue("Compare");

  // the cursor of one pane is a marker in the other, on the pixel the worker's transform gives
  const a = "hi_cube.fits";
  const b = "hi_cube_galactic_overlap.fits";
  await expect(async () => {
    await hover(page, a, 0.5, 0.5);
    await expect(marker(pane(page, b))).toBeVisible({ timeout: 1000 });
  }).toPass();
  await expect(page.getByText("Other map").first()).toBeVisible();
  await expect(page.getByText("computing the link…")).toHaveCount(0);

  // the field of B reaches past A on every side, so a corner of B is outside A's footprint, and is said so in A's header
  await hover(page, b, 0.02, 0.02);
  await expect(pane(page, a).getByText("Cursor is outside this map's footprint")).toBeVisible();
  await expect(marker(pane(page, a))).toHaveCount(0);
  await hover(page, b, 0.5, 0.5);
  await expect(pane(page, a).getByText("Cursor is outside this map's footprint")).toHaveCount(0);

  // a channel moved in A is followed by spectral value in B, with the residual stated
  await page.getByRole("slider", { name: "Channel" }).focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight");
  await expect(page.getByRole("status").filter({ hasText: /^B channel 41 is 0 km\/s from A's 8 km\/s$/ })).toBeVisible();
  await expect(pane(page, b).getByText(/Channel 41 of 64/)).toBeVisible();

  // facets and unlinking: a pane that leaves stops following, and can be linked again
  await page.getByRole("button", { name: "Unlink map B" }).click();
  await expect(page.getByText("Not linked: a pane left the group")).toBeVisible();
  await expect(page.getByLabel("Group name")).toHaveCount(0);
  await hover(page, a, 0.5, 0.5);
  await expect(marker(pane(page, b))).toHaveCount(0);
  await page.getByRole("button", { name: "Link again" }).click();
  await expect(page.getByText(/Linked by WCS: frames ICRS↔Galactic/)).toBeVisible();

  // spectral frames that differ refuse the channel link, and the cursor link still works
  await page.getByLabel("Compare with").selectOption({ label: "hi_cube_topocentric.fits" });
  await page.getByRole("button", { name: "Link by WCS" }).click();
  await expect(page.getByText("Channel link refused: spectral frames differ (LSRK vs TOPOCENT); changing frame is not offered")).toBeVisible();
  await expect(page.getByText(/Linked by WCS: frame ICRS \(astropy\)/)).toBeVisible();

  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).include('[aria-label="Link group"]').analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/linked-views.png` });
});
