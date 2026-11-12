import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// The runs table and the cross-run plot (ADR 0062) through the real API and the built app: six sweep runs written here (block
// size and gradient method varied; one records no result, one records it in another unit), then filter, columns, sort, the
// plot, selection both ways, a saved view that survives a reload, and the CSV, SVG and PNG exports with their provenance.
// The file name sorts last: it imports runs into the catalog the other specs share.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const FOLDER = path.join(data, "sweep_e2e");

const RUNS = [
  { name: "sweep_r1", bs: 8, grad: "central", am: "value = 0.21\nunit = \"\"\nuncertainty = 0.02\nuncertainty_kind = \"standard_error\"" },
  { name: "sweep_r2", bs: 16, grad: "sobel", am: "value = 0.35\nunit = \"\"\nuncertainty = 0.02\nuncertainty_kind = \"standard_error\"" },
  { name: "sweep_r3", bs: 32, grad: "central", am: "value = 0.52\nunit = \"\"\nuncertainty = 0.03\nuncertainty_kind = \"standard_error\"" },
  { name: "sweep_r4", bs: 64, grad: "sobel", am: null }, // records no result at all
  { name: "sweep_r5", bs: 128, grad: "central", am: "value = 0.48\nunit = \"\"" },
  { name: "sweep_r6", bs: 256, grad: "sobel", am: "value = 27\nunit = \"deg\"" }, // the same name in another unit
];

function seed() {
  fs.rmSync(FOLDER, { recursive: true, force: true });
  const base = path.join(FOLDER, "_base");
  execFileSync("python3", ["-c", "import sys, pathlib, generate; generate.write_vgt_run(pathlib.Path(sys.argv[1]))", base], { cwd: path.resolve(here, "../../fixtures") });
  RUNS.forEach((r, i) => {
    const d = path.join(FOLDER, r.name);
    fs.mkdirSync(d, { recursive: true });
    fs.copyFileSync(path.join(base, "psi_demo.fits"), path.join(d, "psi_demo.fits"));
    fs.writeFileSync(
      path.join(d, "run_metadata.toml"),
      `generated_at_utc = "2026-08-0${i + 1}T10:00:00Z"\ngitcommit = "abc1234"\n[config]\nblock_size = ${r.bs}\ngradient_method = "${r.grad}"\n[input]\nsize_bytes = 100\n[outputs]\npsi_path = "x/psi_demo.fits"\n` +
        (r.am ? `[metrics.am]\n${r.am}\ndefinition = "Axial agreement with Planck 353 GHz"\n` : ""),
    );
  });
}

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

async function shot(page: Page, name: string, width: number, height: number) {
  const size = page.viewportSize() ?? { width: 1440, height: 900 };
  await page.setViewportSize({ width, height });
  fs.mkdirSync(path.resolve(here, "../.run/shots"), { recursive: true });
  await page.screenshot({ path: path.resolve(here, `../.run/shots/${name}.png`) });
  await page.setViewportSize(size);
}

/** Replaces the query in the page's own filter box (the explorer has one too, with the same label). */
async function query(page: Page, q: string) {
  const main = page.getByRole("main");
  const clear = main.getByRole("button", { name: "Clear" });
  if (await clear.isVisible()) await clear.click();
  if (!q) return;
  const box = main.getByRole("combobox", { name: "Filter runs" });
  await box.fill(`${q} `);
  await box.press("Escape"); // the suggestions would sit over what follows
}

const names = (page: Page) => page.getByRole("table", { name: /one row per run/i }).locator("tbody tr th button");

test("filter, expose columns, sort, plot, select both ways, save a view, and export with provenance", async ({ page, request }) => {
  seed();
  await page.goto("/");
  for (const r of RUNS) {
    const res = await request.post("/api/runs/import", { data: { path: path.join(FOLDER, r.name) }, headers: { Origin: new URL(page.url()).origin } });
    expect(res.ok(), await res.text()).toBe(true);
  }
  await page.reload();

  // the table opens from the rail; newest import first, and a run with no job records neither status nor host
  await page.getByRole("button", { name: "Results table" }).click();
  await expect(page.getByRole("heading", { name: "Runs", exact: true })).toBeVisible();
  const sweep = names(page).filter({ hasText: /^sweep_r\d$/ });
  await expect(sweep).toHaveText(["sweep_r6", "sweep_r5", "sweep_r4", "sweep_r3", "sweep_r2", "sweep_r1"]);
  await expect(page.getByRole("button", { name: "sweep_r1" }).locator("xpath=ancestor::tr").getByText("not recorded").first()).toBeVisible();
  await expectNoAxeViolations(page);

  // the shared query: a range on a parameter, from the keyboard
  await query(page, "block_size:16..64");
  await expect(sweep).toHaveText(["sweep_r4", "sweep_r3", "sweep_r2"]);
  await expect(page.getByRole("columnheader", { name: /^block_size/ })).toBeVisible(); // asked about, so shown

  // a result: the run that records none is left out and said so, then listed last on request
  await query(page, "am>0.3");
  await expect(sweep).toHaveText(["sweep_r6", "sweep_r5", "sweep_r3", "sweep_r2"]); // 27 deg has no unit in the word: the number as recorded
  await expect(page.getByRole("status").filter({ hasText: /\d+ runs? left out: (it did|they did) not record am>0\.3/ })).toBeVisible();
  await page.getByRole("button", { name: "Show them, last" }).click();
  await expect(sweep).toHaveText(["sweep_r6", "sweep_r5", "sweep_r3", "sweep_r2", "sweep_r4"]);
  await page.getByRole("button", { name: "Hide them" }).click();
  await query(page, "am>0.3deg");
  await expect(sweep).toHaveText(["sweep_r6"]); // the others recorded no unit: not compared as degrees, nothing converted
  await expect(page.getByRole("status").filter({ hasText: /4 runs record am>0\.3deg in another unit/ })).toBeVisible();
  await query(page, "");

  // columns and sorting, by keyboard: missing last in both directions
  await page.getByRole("button", { name: "Columns" }).click();
  await page.getByRole("checkbox", { name: /^block_size/ }).check();
  await page.getByRole("checkbox", { name: /^gradient_method/ }).check();
  await page.getByRole("checkbox", { name: /^am/ }).check();
  await page.getByRole("button", { name: "Columns" }).click();
  const amHead = page.getByRole("columnheader", { name: /^am/ });
  await amHead.getByRole("button").focus();
  await page.keyboard.press("Enter");
  await expect(amHead).toHaveAttribute("aria-sort", "ascending");
  await expect(sweep.last()).toHaveText("sweep_r4");
  await page.keyboard.press("Enter");
  await expect(amHead).toHaveAttribute("aria-sort", "descending");
  await expect(sweep.last()).toHaveText("sweep_r4");
  await expect(page.getByRole("button", { name: "sweep_r4" }).locator("xpath=ancestor::tr").getByText("not recorded").first()).toBeVisible();

  // the plot: choose a result, a parameter, a categorical colour; the unit that differs gets its own labelled panel
  await page.getByLabel("Result (y)").selectOption("am");
  await page.getByLabel("Against (x)").selectOption("param:block_size");
  await page.getByLabel("Colour and shape by").selectOption("gradient_method");
  const plot = page.getByTestId("runs-plot");
  await expect(plot.locator('[data-testid="plot-point"]')).toHaveCount(5);
  await expect(plot.getByText(/not directly comparable: unit differs: deg here/)).toBeVisible();
  await expect(plot.getByText("± standard error", { exact: true })).toBeVisible();
  await expect(plot.getByText("central", { exact: true })).toBeVisible();
  await expect(page.getByText(/\d+ of \d+ runs in the table are not drawn/)).toBeVisible(); // sweep_r4 and runs of other specs record no am
  await expectNoAxeViolations(page);

  // selection: keyboard on the plot selects the row, a row selects the point
  await plot.focus();
  await page.keyboard.press("ArrowRight");
  await expect(page.getByRole("row", { selected: true })).toContainText("sweep_r1");
  await page.getByRole("button", { name: "Select sweep_r3" }).click();
  await expect(plot.locator('[data-run][data-selected="true"]')).toHaveCount(1);
  await expect(page.getByText("Selected: sweep_r3")).toBeVisible();
  await shot(page, "runs-table-1440-dark", 1440, 1800);
  await shot(page, "runs-table-1280", 1280, 1700);
  await shot(page, "runs-table-390", 390, 2400);

  // one click opens a run; Back returns to the table as it was, selection included
  await page.getByRole("button", { name: "Open run" }).click();
  const article = page.getByRole("article", { name: /^Run / });
  await expect(article).toContainText("sweep_r3");
  await article.getByRole("button", { name: "Back" }).click();
  await expect(page.getByText("Selected: sweep_r3")).toBeVisible();
  await expect(page.getByLabel("Result (y)")).toHaveValue("am");

  // a saved view lives on the server: it survives a reload
  await query(page, "block_size:16..64");
  await page.getByRole("button", { name: "Save view…" }).click();
  await page.getByLabel("View name").fill("mid blocks");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByLabel("Saved view")).toHaveValue(/\d+/);
  await page.reload();
  await page.getByRole("button", { name: "Results table" }).click();
  await page.getByLabel("Saved view").selectOption({ label: "mid blocks" });
  await expect(sweep).toHaveText(["sweep_r3", "sweep_r2", "sweep_r4"]); // the view's sort too: am, largest first, the run without it last
  await expect(page.getByLabel("Result (y)")).toHaveValue("am");

  // exports: the query and the run ids travel with the files
  let download = page.waitForEvent("download");
  await page.getByRole("button", { name: /Export table/ }).click();
  const csv = fs.readFileSync(await (await download).path(), "utf8");
  expect(csv).toContain("# query: block_size:16..64");
  expect(csv).toMatch(/# runs: 3 of \d+/);
  expect(csv).toMatch(/\n\d+,sweep_r3,.*\n/);
  expect(csv).not.toContain("sweep_r1");

  download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export plot (SVG)" }).click();
  const svg = fs.readFileSync(await (await download).path(), "utf8");
  // the file is XML (a font stack with quotes once made it invalid), so an image decoder opens it
  expect(await page.evaluate((t) => new DOMParser().parseFromString(t, "image/svg+xml").querySelector("parsererror") === null, svg)).toBe(true);
  expect(svg).not.toContain("var(--");
  const meta = /<metadata id="vesqira-provenance">([^<]+)<\/metadata>/.exec(svg)![1]!.replace(/&quot;/g, '"').replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">");
  expect(JSON.parse(meta)).toMatchObject({ query: "block_size:16..64", y: "am", x: "param:block_size", by: "gradient_method" });
  expect(JSON.parse(meta).runIds).toHaveLength(2); // sweep_r4 records no result: not drawn, not listed as drawn

  download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export plot (PNG)" }).click();
  const png = fs.readFileSync(await (await download).path());
  expect(png.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  // a picture, not an empty canvas: the size the figure declares, and enough drawn pixels to need more than a few hundred bytes
  expect(png.readUInt32BE(16)).toBeGreaterThanOrEqual(1000);
  expect(png.readUInt32BE(20)).toBeGreaterThanOrEqual(400);
  expect(png.length).toBeGreaterThan(15_000);
  expect(png.toString("latin1")).toContain("vesqira-provenance");
  expect(png.toString("utf8")).toContain('"query":"block_size:16..64"');
});
