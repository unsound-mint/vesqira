import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

// The dataset and run lists with 5000 rows each (Stage P). The API answers the two list routes with synthetic rows (the
// catalog of a person who registered years of cubes); everything else is the real app. The lists draw only the rows near the
// viewport, say the full size to assistive technology, and stay usable from the keyboard.

const N = 5000;
const datasets = Array.from({ length: N }, (_, i) => ({
  id: i + 1, path: `/data/synthetic/cube_${String(i).padStart(5, "0")}.fits`, name: `cube_${String(i).padStart(5, "0")}.fits`, sizeBytes: 1_000_000 + i, modifiedAt: "2026-01-01T00:00:00Z", addedAt: "2026-01-01T00:00:00Z",
  hostId: 0, nature: "", unitDeclaration: null, interpretation: null,
  header: { naxis: 2, bitpix: -32, bunit: null, bunit_status: "unresolved", specsys: null, radesys: null, axes: [] },
}));
const runs = Array.from({ length: N }, (_, i) => ({
  id: i + 1, name: `lmc_sweep_${String(i).padStart(5, "0")}`, kind: "vgt", importedAt: "2026-01-01T00:00:00Z", generatedAt: "2026-01-01T00:00:00Z", gitCommit: "abc", dirty: false, reproducibleFromCommit: i % 7 !== 0,
  elapsedSeconds: null, params: [], diagnostics: {}, inputName: "hnv1.fits", inputSizeBytes: 0, outputs: [], metrics: [], inputHint: null, jobId: null, parentRunId: null,
  review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" },
}));

async function synthetic(page: Page) {
  await page.route(/\/api\/datasets(\?.*)?$/, (route) => route.fulfill({ json: { datasets } }));
  await page.route(/\/api\/runs(\?.*)?$/, (route) => route.fulfill({ json: { runs } }));
}

const list = (page: Page, name: string) => page.getByRole("list", { name, exact: true });

async function open(page: Page, narrow = false) {
  await page.goto("/");
  await page.evaluate(() => localStorage.setItem("vesqira.layout.v1", JSON.stringify({ side: 320, insp: 360, timeline: 196, sideOpen: true, inspOpen: false, timelineOpen: false })));
  const t0 = Date.now();
  await page.reload();
  // narrow screens show the explorer only when asked
  if (narrow) await page.getByRole("button", { name: "Toggle explorer" }).click();
  // other specs may have left a virtual subset behind, which is one more row of the real list
  await expect.poll(async () => Number(await list(page, "Datasets").getAttribute("data-rows"))).toBeGreaterThanOrEqual(N);
  return Date.now() - t0;
}

test("5000 datasets and 5000 runs: few rows in the page, the full size announced, keyboard and scrolling work", async ({ page }) => {
  await synthetic(page);
  const ms = await open(page);
  const datasetList = list(page, "Datasets");
  const runList = list(page, "Runs");
  // the section opened before the runs arrived stays closed
  await page.getByRole("button", { name: /^Runs/ }).click();
  await expect(runList).toHaveAttribute("data-rows", String(N));

  // render time: from reload to the list holding 5000 rows (network is local, so this is mostly parsing and drawing)
  const nodes = await page.evaluate(() => document.querySelectorAll("*").length);
  console.info(`list-scale: 2 x ${N} rows ready ${ms} ms after reload, ${nodes} DOM nodes`);
  expect(Number(await datasetList.getAttribute("data-drawn"))).toBeLessThan(80);
  expect(Number(await runList.getAttribute("data-drawn"))).toBeLessThan(80);
  expect(nodes).toBeLessThan(4000);
  expect(ms).toBeLessThan(10_000);

  // the screen reader hears a place in 5000 although few rows exist
  const first = datasetList.getByRole("listitem").first();
  await expect(first).toHaveAttribute("aria-posinset", "1");
  expect(Number(await first.getAttribute("aria-setsize"))).toBeGreaterThanOrEqual(N);

  // keyboard: one tab stop, End goes to the last row (it does not exist yet), Home back
  await datasetList.getByRole("button").first().focus();
  await page.keyboard.press("End");
  const last = datasetList.getByRole("button", { name: /^cube_04999\.fits/ });
  await expect(last).toBeFocused({ timeout: 5_000 });
  await expect(last).toBeInViewport();
  expect(Number(await datasetList.getAttribute("data-drawn"))).toBeLessThan(80);
  await page.keyboard.press("ArrowUp");
  await expect(datasetList.getByRole("button", { name: /^cube_04998\.fits/ })).toBeFocused();
  await page.keyboard.press("Home");
  await expect(datasetList.getByRole("button", { name: /^cube_00000\.fits/ })).toBeFocused();

  // the mouse wheel on the sidebar: rows from the middle of the list appear, the first ones go
  await datasetList.getByRole("button").first().hover();
  await page.mouse.wheel(0, 28 * 1000);
  await expect.poll(async () => (await datasetList.getByRole("listitem").evaluateAll((els) => els.map((e) => Number(e.getAttribute("aria-posinset"))))).filter((n) => n > 1).length).toBeGreaterThan(5);
  expect(Number(await datasetList.getAttribute("data-drawn"))).toBeLessThan(80);

  // the filter narrows 5000 rows to the matching few
  await page.getByLabel("Filter datasets").fill("cube_0123");
  await expect(datasetList.getByRole("listitem")).toHaveCount(10);
});

// Not windowed: the runs table is a real table with a cell per column and shows 300 runs at a time ("Show 300 more").
test("the runs table with 5000 runs draws 300 rows and offers the rest", async ({ page }) => {
  await synthetic(page);
  await open(page);
  await page.getByRole("button", { name: /^Runs/ }).click();
  const t0 = Date.now();
  await page.getByRole("button", { name: "Open as a table with plot…" }).click();
  await expect.poll(() => page.locator("tbody tr").count(), { timeout: 60_000 }).toBe(300);
  await expect(page.getByRole("button", { name: /^Show 300 more of \d+ runs$/ })).toBeVisible();
  const ms = Date.now() - t0;
  const nodes = await page.evaluate(() => document.querySelectorAll("*").length);
  console.info(`list-scale: runs table, 300 of ${N} rows ready ${ms} ms after the click, ${nodes} DOM nodes`);
});

for (const scheme of ["dark", "light"] as const) {
  for (const [width, height] of [[1440, 900], [390, 844]] as const) {
    test(`the long lists have no axe violations and look right at ${width}x${height} (${scheme})`, async ({ page }) => {
      await page.setViewportSize({ width, height });
      await page.emulateMedia({ colorScheme: scheme });
      await synthetic(page);
      await open(page, width < 800);
      await expect(list(page, "Datasets").getByRole("listitem").first()).toBeVisible();
      await list(page, "Datasets").getByRole("button").first().hover(); // the wheel scrolls what the pointer is over
      await page.mouse.wheel(0, 28 * 400);
      await expect.poll(async () => Number((await list(page, "Datasets").getByRole("listitem").nth(2).getAttribute("aria-posinset")) ?? 0)).toBeGreaterThan(100);
      const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
      expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
      await page.screenshot({ path: test.info().outputPath(`list-scale-${width}-${scheme}.png`) });
    });
  }
}
