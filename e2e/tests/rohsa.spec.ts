import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// A ROHSA decomposition (ADR 0046) on the generated fixture (fixtures/generate.py: write_rohsa_run): import the fit, open
// its crop, pin a pixel, switch the Gaussian components on, and read the numbers the fixture defines in closed form.
// Crop: 6 x 4 pixels, 40 channels, three Gaussians per pixel; at pixel (x=2, y=1) the first has amplitude 1.21, centre
// -6.4 km/s and sigma 1.04 km/s (channel step 0.8 km/s, CRPIX3 = 3, no CUNIT3).

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");

const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });

test("a ROHSA fit is imported and its Gaussian components are read at a pinned pixel", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Runs", exact: false }).first().click().catch(() => undefined);
  await page.getByLabel("Import a run (VGT or ROHSA)").fill(path.join(data, "rohsa_run_demo", "region_a"));
  await page.getByRole("button", { name: "Import run" }).click();
  await expect(page.getByRole("list", { name: "Runs" }).getByRole("listitem").filter({ hasText: "rohsa_n3" })).toBeVisible();

  // the run says where each fact comes from, and the product maps keep the file's own unit with a note when the name disagrees
  await expect(inspector(page).getByText("3 (declared by the run trace)")).toBeVisible();
  await expect(inspector(page).getByText("region_a (declared)")).toBeVisible();

  // open the crop the fit used and pin the pixel
  await page.getByRole("button", { name: /^region_a\.fits/ }).click();
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  await expect(canvas).toBeVisible();
  let box = (await canvas.boundingBox())!;
  for (let i = 0; i < 40; i++) {
    await page.waitForTimeout(100);
    const next = (await canvas.boundingBox())!;
    const stable = Math.abs(next.width - box.width) < 0.5 && next.width > 100;
    box = next;
    if (stable) break;
  }
  const at = { x: box.x + ((2 + 0.5) / 6) * box.width, y: box.y + (1 - (1 + 0.5) / 4) * box.height };
  await page.mouse.click(at.x, at.y);

  const toggle = page.getByRole("switch", { name: "Gaussian components" });
  await expect(toggle).toBeEnabled();
  await toggle.check();
  const table = page.getByRole("region", { name: "Fitted components" });
  await expect(table).toBeVisible();
  const rows = table.getByRole("row");
  await expect(rows).toHaveCount(4); // header + three components
  const first = rows.nth(1).getByRole("cell");
  await expect(first.nth(0)).toHaveText("1.21");   // amplitude, K
  await expect(first.nth(1)).toHaveText("-6.4");   // centre, km/s
  await expect(first.nth(2)).toHaveText("1.04");   // sigma, km/s
  await expect(first.nth(5)).toHaveText("CNM");    // 1.04 < 2.03
  await expect(rows.nth(3).getByRole("cell").nth(5)).toHaveText("others"); // sigma 2.16
  await expect(page.getByText(/residual = data − model/)).toBeVisible();
  await expect(page.getByText(/FITS default for/)).toBeVisible(); // the crop has no CUNIT3: the assumption is stated

  // the cut is the person's: raise it above the third component's width and the group changes
  await page.getByLabel("CNM width cut σ (km/s)").fill("2.5");
  await expect(rows.nth(3).getByRole("cell").nth(5)).toHaveText("CNM");

  await page.screenshot({ path: path.resolve(here, "../.run/rohsa-components.png"), fullPage: false });
  const axe = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(axe.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
});

// Reviewing the whole decomposition (#131): the sigma-v diagram, the class maps, and the pixels of a cell marked on them.
test("the decomposition's sigma-v diagram marks the pixels of a cell on the width-class maps", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Runs", exact: false }).first().click().catch(() => undefined);
  const runs = page.getByRole("list", { name: "Runs" });
  if (!(await runs.getByRole("listitem").filter({ hasText: "rohsa_n3" }).count())) {
    await page.getByLabel("Import a run (VGT or ROHSA)").fill(path.join(data, "rohsa_run_demo", "region_a"));
    await page.getByRole("button", { name: "Import run" }).click();
  }
  await page.getByRole("button", { name: /^region_a\.fits/ }).click();
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  await expect(canvas).toBeVisible();
  await page.waitForTimeout(500);
  const box = (await canvas.boundingBox())!;
  await page.mouse.click(box.x + (2.5 / 6) * box.width, box.y + (1 - 1.5 / 4) * box.height);
  await page.getByRole("switch", { name: "Gaussian components" }).check();

  const review = page.getByRole("region", { name: "Review of the decomposition" });
  await review.getByRole("button", { name: "Draw the diagram" }).click();
  await expect(review.getByRole("button", { name: "Draw the diagram again" })).toBeVisible({ timeout: 60_000 });
  await expect(review.getByText(/components are inside the diagram/)).toBeVisible();
  await review.getByRole("button", { name: "Make the class maps" }).click();
  await expect(review.getByRole("button", { name: "Make the maps again" })).toBeVisible({ timeout: 60_000 });
  await review.getByRole("button", { name: "Mark pixels" }).first().click();
  await expect(review.getByText(/carry stripes on the class maps/)).toBeVisible({ timeout: 30_000 });

  const axe = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(axe.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
  // pictures for the visual review (not assertions): a tall window so the whole review fits in the inspector
  await page.setViewportSize({ width: 1440, height: 2600 });
  for (const scheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await review.scrollIntoViewIfNeeded();
    await review.screenshot({ path: path.resolve(here, `../test-results/rohsa-review-${scheme}.png`) });
  }
});
