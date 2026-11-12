import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// Spectral smoothing and the family view (ADR 0069) through the real API, worker and built app: smooth a cube with two boxcar widths,
// see the family recognised from the records, compare it on one scale with an agreement panel, and check the numbers are stated in words.
// Runs last (the name): it adds datasets to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const shots = path.resolve(here, "../.run");

type Ds = { id: number; name: string };

test("two smoothings of a cube are recognised as a family and compared on one scale", async ({ page, request }) => {
  await page.goto("/");
  await page.getByLabel("Register a FITS file").fill(path.join(data, "hi_cube.fits"));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  const row = page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ });
  const offer = page.getByRole("status", { name: "A dataset already holds this content" });
  await expect(row.first().or(offer)).toBeVisible();
  if (await offer.isVisible()) await offer.getByRole("button", { name: "Register as a separate dataset" }).click();
  await row.first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();

  // the smoothing form sits next to the other analyses and says what it does not do
  const insp = page.getByRole("complementary", { name: "Inspector" });
  await page.getByRole("button", { name: "Analysis", exact: true }).click();
  await insp.getByLabel("Analysis", { exact: true }).selectOption("smooth_spectral");
  await expect(insp.getByText(/A value with too little valid data around it becomes missing, never zero/)).toBeVisible();
  await expect(insp.getByRole("button", { name: "Create the new file" })).toBeDisabled();

  const list = (await (await request.get(`${API}/api/datasets`)).json()) as { datasets: Ds[] };
  const cube = list.datasets.find((d) => d.name === "hi_cube.fits")!;
  for (const width of ["3", "5"]) {
    const r = await request.post(`${API}/api/analysis`, { data: { op: "smooth_spectral", datasetId: cube.id, params: { kernel: "boxcar", width, minValidWeight: "0.5" } } });
    expect(r.status()).toBe(201);
  }
  await page.reload();

  await page.getByRole("button", { name: "Compare a family of maps", exact: true }).click();
  const found = page.getByRole("region", { name: "Families found in your results" });
  await expect(found).toContainText("differ in width");
  await found.getByRole("button", { name: "Use these" }).first().click();
  await expect(page.getByLabel("Channel (1-based, the same for every cube)")).toBeVisible();
  await page.getByLabel("Channel (1-based, the same for every cube)").fill("32");
  await page.getByRole("button", { name: "Compare", exact: true }).click();

  const maps = page.getByRole("region", { name: "The maps" });
  await expect(maps.locator("canvas")).toHaveCount(2, { timeout: 60_000 });
  const numbers = page.getByRole("region", { name: "Agreement numbers" });
  await expect(numbers).toContainText("(reference)");
  await expect(numbers).toContainText("the member minus the reference");

  // one pixel is read in every panel, in words and numbers, from the keyboard
  await maps.locator("canvas").first().focus();
  await page.keyboard.press("ArrowRight");
  const readout = page.getByRole("region", { name: "Values at the cursor" });
  await expect(readout.getByRole("table")).toBeVisible();
  await expect(readout).toContainText("Spread across the maps");

  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const [w, h] of [[1440, 900], [390, 844]] as const) {
      await page.setViewportSize({ width: w, height: h });
      // the page scrolls inside the window, so look at the top and at the numbers below
      await numbers.evaluate((el) => el.closest("div")!.parentElement!.scrollTo(0, 0));
      await page.screenshot({ path: path.join(shots, `family-${scheme}-${w}-top.png`) });
      await numbers.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `family-${scheme}-${w}-numbers.png`) });
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  const axe = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(axe.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
});
