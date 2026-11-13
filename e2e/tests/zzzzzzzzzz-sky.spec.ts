import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// The sky panel (ADR 0072) through the real API, worker and built app, with the survey background OFF, which is its default. The
// run has no external network by design: every request that leaves this machine is blocked and counted, and none may be attempted
// while the background is off. Runs late (the name): it adds datasets to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const shots = path.resolve(here, "../.run");

type Ds = { id: number; name: string };
const FILES = ["hi_cube.fits", "hi_cube_galactic.fits", "hi_cube_no_celestial_wcs.fits", "hi_cube_coarser.fits"];

/** Registers a file, or finds the dataset that already holds it (other specs register the same fixtures). */
async function ensure(request: import("@playwright/test").APIRequestContext, file: string): Promise<Ds> {
  const find = async () => ((await (await request.get(`${API}/api/datasets`)).json()) as { datasets: Ds[] }).datasets.find((d) => d.name === file);
  const known = await find();
  if (known) return known;
  const r = await request.post(`${API}/api/datasets`, { data: { path: path.join(data, file) } });
  expect([201, 409]).toContain(r.status());
  const d = await find();
  expect(d, `${file} is registered`).toBeTruthy();
  return d!;
}

/** A dataset's outline on the sky map, by id: other specs register files with the same name. */
const outline = (panel: import("@playwright/test").Locator, d: Ds) => panel.locator(`[role="button"][data-dataset-id="${d.id}"]`);

/** Everything that is not this machine is refused and written down. */
async function blockExternal(page: Page): Promise<string[]> {
  const attempts: string[] = [];
  const local = new Set(["127.0.0.1", "localhost"]);
  await page.route("**/*", (route) => {
    const u = new URL(route.request().url());
    if (u.protocol === "data:" || u.protocol === "blob:" || local.has(u.hostname)) return route.continue();
    attempts.push(route.request().url());
    return route.abort();
  });
  return attempts;
}

test("the sky panel works offline, draws footprints from the WCS, links the cursor by world coordinates, and contacts no server while the background is off", async ({ page, request }) => {
  const [cube, galactic, bare, coarser] = [await ensure(request, FILES[0]!), await ensure(request, FILES[1]!), await ensure(request, FILES[2]!), await ensure(request, FILES[3]!)];
  const attempts = await blockExternal(page);
  await page.goto("/");
  await page.evaluate(() => localStorage.removeItem("vesqira.sky.v1"));
  await page.reload();
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ }).first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible({ timeout: 30_000 });

  const insp = page.getByRole("complementary", { name: "Inspector" });
  await insp.getByRole("button", { name: "Sky", exact: true }).click();
  const panel = page.getByTestId("sky-panel");
  await expect(panel).toBeVisible();

  // the open cube and the overlapping one are drawn; the one without celestial WCS is listed with a reason and has no outline
  await expect(outline(panel, cube)).toBeVisible();
  await expect(outline(panel, coarser)).toBeVisible();
  const list = panel.getByRole("list", { name: "Datasets on the sky" });
  await expect(list.getByRole("listitem").filter({ hasText: bare.name }).first()).toContainText(/no celestial WCS|celestial/i);
  await expect(outline(panel, bare)).toHaveCount(0);

  // the background is off, says so, and names what turning it on would contact
  await expect(panel.getByText(/Off\. Nothing leaves this computer/)).toBeVisible();
  await expect(panel.getByText(/Plain coordinate grid\. No survey image: the background is off/)).toBeVisible();
  await panel.getByRole("button", { name: "Choose a survey image…" }).click();
  await expect(panel.getByRole("group", { name: "Contact a server" })).toContainText("alasky.cds.unistra.fr");
  await expect(panel.getByRole("group", { name: "Contact a server" })).toContainText("so those requests show which field you are working on");
  await panel.getByRole("button", { name: "Cancel" }).click();

  // the other frame is asked of the worker again, and the outlines stay
  await panel.getByLabel("Frame").selectOption("galactic");
  await expect(outline(panel, cube)).toBeVisible();
  await expect(panel.getByRole("group", { name: /Sky map in Galactic/ })).toBeVisible();
  await panel.getByLabel("Frame").selectOption("icrs");
  await expect(panel.getByRole("group", { name: /Sky map in ICRS/ })).toBeVisible();

  // hovering the viewer shows the position as a world coordinate in the panel
  await panel.getByRole("button", { name: "Fit the open dataset" }).click();
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  const box = (await canvas.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  const cursor = panel.locator("dl").getByText(/^RA \d/);
  await expect(cursor).toBeVisible();
  const where = (await cursor.innerText()).replace(/\s+/g, " ");
  expect(where).toMatch(/^RA \d+\.\d+°?, Dec -?\d+\.\d+/);

  // an outline is a button: from the keyboard it opens its dataset (a pointer click would hit whichever of the shared instance's overlapping outlines is on top)
  await outline(panel, coarser).focus();
  await page.keyboard.press("Enter");
  await expect(panel.locator("dl").getByText(coarser.name, { exact: true })).toBeVisible();

  // a cube whose WCS is Galactic is drawn too, from the worker's transform (it lies far from the others, so open it from the list)
  await list.getByRole("button", { name: galactic.name, exact: true }).first().click();
  await expect(outline(panel, galactic)).toBeVisible();
  await expect(list.getByRole("listitem").filter({ hasText: galactic.name }).first()).toContainText(/galactic/i);

  // nothing was asked of any server outside this machine
  expect(attempts).toEqual([]);

  // accessibility, and the picture in both themes at the two reference sizes
  const axe = await new AxeBuilder({ page: page as never }).include('[data-testid="sky-panel"]').withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(axe.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const [w, h] of [[1440, 900], [390, 844]] as const) {
      await page.setViewportSize({ width: w, height: h });
      if (w < 800 && !(await panel.isVisible())) await page.getByRole("button", { name: /inspector/i }).first().click();
      await panel.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `sky-${scheme}-${w}.png`) });
      await expect(panel).toBeVisible();
    }
  }
  expect(attempts).toEqual([]);
});
