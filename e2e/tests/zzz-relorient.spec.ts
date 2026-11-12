import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// Relative orientation (ADR 0047): a declared orientation map against a catalog with a declared angle, run from the map's
// inspector, gives an agreement statistic with its chance level, a histogram and segment figure, and a table to download.
// Four stars sit around the centre of the tangential fixture field, each with the field's direction at its place, so the
// alignment measure is near 1. Runs last (the name) because it adds datasets to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const dra = (0.08 / Math.cos((2 * Math.PI) / 180)).toFixed(8);
const STARS = `ra,dec,pa\n${(150 - Number(dra)).toFixed(8)},2.0,0\n${(150 + Number(dra)).toFixed(8)},2.0,0\n150.0,2.08,90\n150.0,1.92,90\n,,\n`;

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("compare a declared orientation map with a starlight catalog and read the agreement statistic", async ({ page, request }) => {
  writeFileSync(path.join(data, "stars_tangential.csv"), STARS);
  const map = await (await request.post(`${API}/api/datasets`, { data: { path: path.join(data, "vgt_tangential_deg.fits") } })).json();
  const decl = await request.post(`${API}/api/datasets/${map.id}/interpretation`, { data: { kind: "axial_orientation", unit: "deg", frame: "pixel", quantity: "b_field" } });
  expect(decl.ok()).toBe(true);
  const stars = await (await request.post(`${API}/api/catalogs`, { data: { path: path.join(data, "stars_tangential.csv") } })).json();
  const cd = await request.put(`${API}/api/datasets/${stars.id}/catalog/declaration`, {
    data: { lon: "ra", lat: "dec", format: "degrees", frame: "icrs", equinox: null, values: [], angle: { column: "pa", unit: "deg", frame: "sky", quantity: "starlight_polarization", sigmaColumn: null } },
  });
  expect(cd.ok()).toBe(true);

  await page.goto("/");
  await page.getByRole("button", { name: /^vgt_tangential_deg\.fits/ }).click();
  const inspector = page.getByRole("complementary", { name: "Inspector" });
  await inspector.getByRole("button", { name: "Compare orientation" }).click();
  await inspector.getByLabel("Compare orientation with").selectOption({ label: "stars_tangential.csv (catalog)" });
  await inspector.getByLabel("Disk radius (arcmin)").fill("1.5");
  await inspector.getByText("Advanced settings").click(); // the shortest shift and the other settings that rarely change sit behind it
  await inspector.getByLabel("Shortest shift (deg)").fill("0.05");
  await inspector.getByLabel("Shifts in the null").fill("200");
  await expectNoAxeViolations(page);
  await inspector.getByRole("button", { name: "Compute the alignment measure" }).click();

  // the result is a new dataset (the per-source table), selected, with its numbers and figures
  const result = inspector.getByRole("region", { name: "Relative orientation result" });
  await expect(result.getByText(/Agreement statistic · vgt_tangential_deg\.fits against stars_tangential\.csv/)).toBeVisible({ timeout: 60_000 });
  await expect(result.getByText("4 of 5 (80.0%)")).toBeVisible();
  await expect(result.getByLabel("Excluded samples")).toContainText("1 position missing or unreadable in the catalog");
  await expect(result.getByRole("img", { name: /Histogram of \|Δψ\|/ })).toBeVisible();
  await expect(result.getByRole("img", { name: /Orientations at 4 sources/ })).toBeVisible();
  const am = Number(await result.locator("dd").first().textContent());
  expect(am).toBeGreaterThan(0.99);
  const csv = await request.get(`${API}${await result.getByRole("link", { name: /per-source table/ }).getAttribute("href")}`);
  expect(csv.ok()).toBe(true);
  expect((await csv.text()).split("\n")[0]).toMatch(/^row,lon_deg,lat_deg,frame/);
  await expect(inspector.getByText(/validated|success/i)).toHaveCount(0);
  await expectNoAxeViolations(page);
  // pictures for the visual review (not assertions): the result at desktop width in dark, and in light
  await result.screenshot({ path: path.resolve(here, "../test-results/relorient-result-1440-dark.png") });
  await result.getByRole("img", { name: /Orientations at 4 sources/ }).screenshot({ path: path.resolve(here, "../test-results/relorient-segments-dark.png") });
  await page.emulateMedia({ colorScheme: "light" });
  await result.screenshot({ path: path.resolve(here, "../test-results/relorient-result-1440-light.png") });
});

// Bins of a third map (ADR 0047 amendment): the same field in degrees against radians, binned by the reliability map on their
// grid. The two maps agree everywhere, so every bin has AM 1; the test is about the binning, the unit and the outputs.
test("bin a map-against-map comparison by a third map and read the per-bin table", async ({ page, request }) => {
  const reg = async (name: string) => (await (await request.post(`${API}/api/datasets`, { data: { path: path.join(data, name) } })).json()) as { id: number };
  const a = await reg("vgt_psi_deg.fits");
  const b = await reg("vgt_psi_rad.fits");
  const rel = await reg("vgt_reliability.fits");
  for (const [d, unit] of [[a, "deg"], [b, "rad"]] as const) {
    const r = await request.post(`${API}/api/datasets/${d.id}/interpretation`, { data: { kind: "axial_orientation", unit, frame: "pixel", quantity: "b_field" } });
    expect(r.ok()).toBe(true);
  }
  // the reliability map has no BUNIT: bins are stated in a unit someone declared, never an assumed one
  const u = await request.post(`${API}/api/datasets/${rel.id}/unit`, { data: { unit: "1" } });
  expect(u.ok(), await u.text()).toBe(true);

  await page.goto("/");
  await page.getByRole("button", { name: /^vgt_psi_deg\.fits/ }).click();
  const inspector = page.getByRole("complementary", { name: "Inspector" });
  await inspector.getByRole("button", { name: "Compare orientation" }).click();
  await inspector.getByLabel("Compare orientation with").selectOption({ label: "vgt_psi_rad.fits" });
  await inspector.getByLabel("Bin by a third map (optional)").selectOption({ label: "vgt_reliability.fits" });
  await inspector.getByLabel("Number of bins").fill("4");
  await inspector.getByText("Advanced settings").click();
  await inspector.getByLabel("Shortest shift (deg)").fill("0.01"); // the fixture map is small
  await inspector.getByLabel("Shifts in the null").fill("50");
  await expectNoAxeViolations(page);
  await inspector.getByRole("button", { name: "Compute the alignment measure" }).click();

  const result = inspector.getByRole("region", { name: "Relative orientation result" });
  await expect(result.getByText(/vgt_psi_deg\.fits against vgt_psi_rad\.fits/).first()).toBeVisible({ timeout: 60_000 });
  const table = result.getByRole("table", { name: "Alignment by bin" });
  await expect(table).toBeVisible();
  await expect(table.locator("tbody tr")).toHaveCount(4);
  await expect(inspector.getByText(/validated|success/i)).toHaveCount(0);
  await expectNoAxeViolations(page);
  const strata = result.getByRole("region", { name: "Alignment by bins of a third map" });
  await strata.scrollIntoViewIfNeeded();
  await strata.screenshot({ path: path.resolve(here, "../test-results/relorient-strata-1440-dark.png") });
  await page.emulateMedia({ colorScheme: "light" });
  await strata.screenshot({ path: path.resolve(here, "../test-results/relorient-strata-1440-light.png") });
});
