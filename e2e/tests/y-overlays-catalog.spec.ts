import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// A point catalog drawn over the open map (ADR 0042 catalog overlay, ADR 0044), through the real API, worker and built app:
// a small starlight-polarization table in decimal ICRS degrees laid on the generated H I cube (TAN at 150, +2, 0.01 degree
// pixels): three rows on the map, two outside it, one whose position cannot be read. Markers, vectors, a row filter with its
// counts, a click on a marker for its row, the keyboard list, and a figure that stores the overlay.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const datasets = (page: Page) => page.getByRole("list", { name: "Datasets" });

const TABLE = [
  "RA,DEC,P,sP,PA",
  "150.0,2.0,2.0,0.1,0",
  "150.05,2.03,1.0,0.5,45",
  "149.95,1.95,3.0,0.1,90",
  "150.2,2.0,1.0,0.1,10",
  "150.0,40.0,1.0,0.1,10",
  "abc,2.0,1.0,0.1,10",
].join("\n") + "\n";

async function register(page: Page, file: string) {
  await page.getByLabel("Register a FITS file").fill(path.join(data, file));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await expect(datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first()).toBeVisible();
  await expect(page.getByLabel("Register a FITS file")).toHaveValue("");
}

const open = (page: Page, file: string) => datasets(page).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first().click();

test("a catalog is drawn over the map from its declaration, counted, clickable and listed for the keyboard, and saved in a figure", async ({ page, request }) => {
  writeFileSync(path.join(data, "y_stars.csv"), TABLE);
  await page.goto("/");
  await register(page, "hi_cube.fits");
  await register(page, "y_stars.csv");
  const insp = inspector(page);

  // the columns are declared by the person: position columns, the frame, and what the angle is
  await open(page, "y_stars.csv");
  await insp.getByRole("button", { name: "Fill the form from this proposal" }).click();
  await insp.getByLabel("Celestial frame").selectOption("icrs");
  await insp.getByLabel("How positions are written").selectOption("degrees");
  await insp.getByLabel("Angle unit").selectOption("deg");
  await insp.getByLabel("What the angle is").selectOption("starlight_polarization");
  await insp.getByRole("button", { name: "Declare", exact: true }).click();
  await expect(insp.getByRole("status", { name: "How the declaration reads the rows" })).toBeVisible();

  await open(page, "hi_cube.fits");
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const rows = insp.getByRole("list", { name: /^Overlays/ }).getByRole("listitem");

  // markers only first: three of six rows, two outside the map, one that could not be placed
  await insp.getByRole("button", { name: "Add overlay" }).click();
  await insp.getByLabel("Kind").selectOption("catalog");
  await insp.getByLabel("Colour markers by").selectOption("P");
  await insp.getByRole("button", { name: "Add", exact: true }).click();
  const row = rows.filter({ hasText: "Catalog of y_stars.csv" });
  await expect(row).toContainText("3 of 6 rows drawn as markers");
  await expect(row).toContainText("2 rows outside the base map, counted and not drawn");
  await expect(row).toContainText("1 row not drawn: see the catalog's parse report");
  await expect(row).toContainText("catalog frame ICRS (declared by you in the catalog's column declaration)");
  await expect(row.getByRole("group", { name: "Colour legend: P" })).toContainText("1");
  await expect(page.locator('[data-role="catalog"] g[data-row]')).toHaveCount(3);

  // a click on a marker shows its row, with the line in the file; Escape closes it
  const dot = page.locator('[data-role="catalog"] g[data-row="3"] circle').last();
  const box = (await dot.boundingBox())!;
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  const pop = page.getByRole("dialog", { name: "Catalog source, row 3" });
  await expect(pop.getByText("149.95")).toBeVisible();
  await expect(pop.getByText(/Row 3, line 4 of the file/)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(pop).toHaveCount(0);

  // vectors from the declared angle, long by P, only rows with P / sP at least 3
  await insp.getByRole("button", { name: "Edit Catalog of y_stars.csv" }).click();
  await insp.getByLabel(/Orientation vectors from the declared angle/).check();
  await insp.getByLabel("Vector length").selectOption("P");
  await insp.getByLabel("Keep rows where").selectOption("P");
  await insp.getByLabel("divided by").selectOption("sP");
  await insp.getByRole("button", { name: "Apply" }).click();
  await expect(row).toContainText("2 of 6 rows drawn as markers");
  await expect(row).toContainText("2 orientation vectors (headless)");
  await expect(row).toContainText("starlight polarization, parallel to B");
  await expect(row).toContainText("row filter P / sP >= 3: 2 kept, 1 below, 0 unknown");
  await expect(page.locator('[data-role="catalog"] g[data-row]')).toHaveCount(2);

  // the keyboard route to the same rows: the list, a button per source, the row's values beside it
  await row.getByText(/2 sources drawn: list/).click();
  const first = row.getByRole("list", { name: "Sources drawn by this overlay" }).getByRole("button").first();
  await first.focus();
  await page.keyboard.press("Enter");
  await expect(row.getByRole("region", { name: "Row 1" })).toContainText("150");
  await expect(first).toHaveAttribute("aria-pressed", "true");
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/catalog-overlay.png` });
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).include('[aria-label="Inspector"]').analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);

  // a figure stores the overlay as drawn and names the catalog file, with what the author declared, in its provenance
  await page.getByRole("button", { name: "Figure", exact: true }).click();
  await insp.getByLabel("Title").first().fill("H I with starlight polarization");
  await insp.getByRole("button", { name: "Save as figure" }).click();
  await expect(insp.getByText(/Saved: “H I with starlight polarization”/)).toBeVisible();
  const list = (await (await request.get(`${API}/api/figures`)).json()) as { figures: { title: string; scene?: { overlays: Record<string, unknown>[] }; provenance: { statements: string[] } }[] };
  const fig = list.figures.find((f) => f.title === "H I with starlight polarization")!;
  const cat = fig.scene!.overlays.find((o) => o.kind === "catalog") as { vectors: boolean; lengthColumn: string; filter: { column: string; sigmaColumn: string; min: number } };
  expect(cat).toMatchObject({ vectors: true, lengthColumn: "P", filter: { column: "P", sigmaColumn: "sP", min: 3 } });
  expect(fig.provenance.statements.join(" ")).toContain("Point catalog drawn on this map, y_stars.csv");
  expect(JSON.stringify(fig.provenance)).not.toContain('"datasetId"');
});
