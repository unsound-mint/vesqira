import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// A decomposition written by a tool that is neither VGT nor ROHSA (ADR 0068, slice 4): its components are FITS maps and a test-only
// profile (services/api/internal/runs/testdata/synthetic_maps_profile.json, loaded by e2e/start-api.sh) declares how to read them.
// The fixture is closed form (fixtures/generate.py decomp_truth), 7 x 5 pixels, three slots per pixel, the third not fitted where
// (x + y) % 4 == 0. At pixel (x=3, y=2): amplitudes 2.4, 2.9, 3.4 K; centres -9.6, -0.6, 8.4 km/s; FWHM 3.1, 5.1, 7.1 km/s, so sigma
// 1.316, 2.166, 3.015 km/s (FWHM / 2.3548). At (x=1, y=3) the third slot is absent.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const out = path.resolve(here, "../test-results");
const NX = 7;
const NY = 5;

const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });

async function importRun(page: Page, folder: string, name: string) {
  await page.goto("/");
  await page.getByRole("button", { name: "Runs", exact: false }).first().click().catch(() => undefined);
  await page.getByLabel("Import a run (VGT or ROHSA)").fill(path.join(data, "synth_decomp_demo", folder));
  await page.getByRole("button", { name: "Import run" }).click();
  await expect(page.getByRole("list", { name: "Runs" }).getByRole("listitem").filter({ hasText: name })).toBeVisible();
}

/** Opens the cube the fit was made on and pins pixel (x, y), counted from 0 with y up (the way the image is drawn). */
async function pin(page: Page, x: number, y: number) {
  await page.getByRole("button", { name: /^synth_cube\.fits/ }).last().click(); // the folders each registered their own copy; the newest import is last
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
  await page.mouse.click(box.x + ((x + 0.5) / NX) * box.width, box.y + (1 - (y + 0.5) / NY) * box.height);
}

const axe = async (page: Page) => {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
};

/** Screenshots (not assertions) at wide, compact and phone widths in both colour schemes, with the components table in view, and an axe scan of each. */
async function shoot(page: Page, name: string, sizes: readonly (readonly [number, number])[]) {
  const table = page.getByRole("region", { name: "Fitted components" });
  for (const [w, h] of sizes) {
    await page.setViewportSize({ width: w, height: h });
    for (const scheme of ["dark", "light"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      if (!(await inspector(page).isVisible())) await page.getByRole("button", { name: "Toggle inspector" }).click(); // a phone shows one panel at a time
      await table.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(out, `${name}-${w}-${scheme}.png`), fullPage: false });
      await axe(page);
    }
  }
}

test("a tool that writes its components as FITS maps is read from its profile: components, diagram, class maps", async ({ page }) => {
  await importRun(page, "", "synth_decomp_demo");
  await expect(inspector(page).getByText("3 (declared by the run record (config.n_components))")).toBeVisible();

  await pin(page, 3, 2);
  const toggle = page.getByRole("switch", { name: "Gaussian components" });
  await expect(toggle).toBeEnabled();
  await toggle.check();
  const table = page.getByRole("region", { name: "Fitted components" });
  await expect(table).toBeVisible();
  const rows = table.getByRole("row");
  await expect(rows).toHaveCount(4);
  const first = rows.nth(1).getByRole("cell");
  await expect(first.nth(0)).toHaveText("2.4");     // K, from the amplitude map's BUNIT
  await expect(first.nth(1)).toHaveText("-9.6");    // km/s: the centre map says m/s in BUNIT
  await expect(first.nth(2)).toHaveText("1.316");   // sigma = FWHM 3.1 / 2.3548
  await expect(first.nth(3)).toHaveText("3.1");     // the FWHM, as the file states it
  await expect(first.nth(5)).toHaveText("CNM");     // 1.316 < 2.03
  await expect(rows.nth(2).getByRole("cell").nth(5)).toHaveText("others");   // sigma 2.166
  // the record says how the numbers were read: a FWHM converted explicitly, a velocity centre in LSRK
  await expect(page.getByText(/Width: fwhm in km\/s \(sigma = FWHM \/ \(2 sqrt\(2 ln 2\)\) = FWHM \/ 2\.3548\)/)).toBeAttached();
  await expect(page.getByText(/frame LSRK/)).toBeAttached();

  // a slot the tool did not fit is not a component, and not a zero
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  const box = (await canvas.boundingBox())!;
  await page.mouse.click(box.x + (1.5 / NX) * box.width, box.y + (1 - 3.5 / NY) * box.height);
  await expect(table.getByText("not fitted at this pixel")).toBeVisible();

  const review = page.getByRole("region", { name: "Review of the decomposition" });
  await review.getByRole("button", { name: "Draw the diagram" }).click();
  await expect(review.getByRole("button", { name: "Draw the diagram again" })).toBeVisible({ timeout: 60_000 });
  // 3 x 7 x 5 = 105 slots, 9 of them absent ((x + y) % 4 == 0 over the 7 x 5 grid)
  await expect(review.getByText(/of 96 components are inside the diagram/)).toBeVisible();
  await expect(review.getByText(/9 slots the tool did not fit are not components/)).toBeVisible();
  await review.getByRole("button", { name: "Make the class maps" }).click();
  await expect(review.getByRole("button", { name: "Make the maps again" })).toBeVisible({ timeout: 60_000 });
  await axe(page);

  await shoot(page, "decomp-maps", [[1440, 900], [1280, 800], [390, 844]]);
});

test("a record that does not state the centre's frame lists the source's values and draws nothing", async ({ page }) => {
  await importRun(page, "unresolved", "unresolved");
  await pin(page, 3, 2);
  await page.getByRole("switch", { name: "Gaussian components" }).check();
  await expect(page.getByText(/Not drawn: the centre is a velocity but its spectral frame is not declared/)).toBeVisible();
  const table = page.getByRole("region", { name: "Fitted components" });
  await expect(table.getByRole("columnheader", { name: /Centre as stated \(m\/s\)/ })).toBeVisible();
  await expect(table.getByRole("columnheader", { name: /Width as stated \(fwhm, km\/s\)/ })).toBeVisible();
  await expect(table.getByRole("cell", { name: "-9600" })).toBeVisible();   // the centre as the map states it, in m/s, not converted
  await expect(table.getByText("CNM")).toHaveCount(0);
  await expect(page.locator('svg[aria-label^="Gaussian components at pixel"]')).toHaveCount(0);
  await shoot(page, "decomp-unresolved", [[1440, 900], [390, 844]]);
});
