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
  await page.emulateMedia({ colorScheme: "light" });
  await result.screenshot({ path: path.resolve(here, "../test-results/relorient-result-1440-light.png") });
});
