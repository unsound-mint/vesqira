import { expect, test } from "@playwright/test";
import type { APIRequestContext, Locator, Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// A sky figure records the picture's own field (ADR 0072). With the survey in the picture the person can drag and zoom Aladin, so the
// panel's view is not the picture's; the saved centre is read back from Aladin. The survey host is stubbed here (a properties file,
// no tiles): this run has no external network, and what is checked is where the picture looks, not what the survey shows.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const HOST = "https://alasky.cds.unistra.fr";
const PROPERTIES = ["creator_did = ivo://CDS/P/DSS2/color", "obs_title = stub", "dataproduct_type = image", "hips_version = 1.4", "hips_order = 3", "hips_frame = equatorial", "hips_tile_format = jpeg", "hips_tile_width = 512", ""].join("\n");

type Fig = { id: number; kind: string; provenance: { sky?: { frame: string; centre: { lon: number; lat: number }; fovDeg: number; background: unknown; footprints: unknown[] } } };

async function ensure(request: APIRequestContext, file: string) {
  const find = async () => ((await (await request.get(`${API}/api/datasets`)).json()) as { datasets: { id: number; name: string }[] }).datasets.find((d) => d.name === file);
  if (!(await find())) expect([201, 409]).toContain((await request.post(`${API}/api/datasets`, { data: { path: path.join(data, file) } })).status());
  return (await find())!;
}

async function stubSurvey(page: Page) {
  await page.route("**/*", (route) => {
    const u = new URL(route.request().url());
    if (u.protocol === "data:" || u.protocol === "blob:" || u.hostname === "127.0.0.1" || u.hostname === "localhost") return route.continue();
    if (route.request().url().startsWith(`${HOST}/`)) {
      return route.request().url().endsWith("/properties") ? route.fulfill({ status: 200, contentType: "text/plain", headers: { "access-control-allow-origin": "*" }, body: PROPERTIES }) : route.fulfill({ status: 404, headers: { "access-control-allow-origin": "*" } });
    }
    return route.abort();
  });
}

const skyFigures = async (request: APIRequestContext) => ((await (await request.get(`${API}/api/figures`)).json()) as { figures: Fig[] }).figures.filter((f) => f.kind === "sky").sort((a, b) => a.id - b.id);

/** The sky figure saved after `after` (the saved message is still on screen from the one before, so the list is what is waited on). */
async function savedAfter(request: APIRequestContext, after: number) {
  await expect.poll(async () => (await skyFigures(request)).at(-1)?.id ?? 0).toBeGreaterThan(after);
  const f = (await skyFigures(request)).at(-1)!;
  return { id: f.id, sky: f.provenance.sky! };
}

const drag = async (page: Page, el: Locator, dx: number, dy: number) => {
  await el.scrollIntoViewIfNeeded(); // the inspector scrolls: a drag off the screen would miss the picture
  const b = (await el.boundingBox())!;
  const x = b.x + b.width / 2;
  const y = b.y + b.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + dx / 2, y + dy / 2, { steps: 6 });
  await page.mouse.move(x + dx, y + dy, { steps: 6 });
  await page.mouse.up();
  await page.waitForTimeout(600); // Aladin eases to rest
};

for (const frame of ["icrs", "galactic"] as const) {
  test(`${frame}: the saved field is the picture's, also after the person drags the survey view`, async ({ page, request }) => {
    const cube = await ensure(request, "hi_cube.fits");
    await stubSurvey(page);
    await page.goto("/");
    await page.evaluate(() => localStorage.removeItem("vesqira.sky.v1"));
    await page.reload();
    await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ }).first().click();
    await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible({ timeout: 30_000 });
    await page.getByRole("complementary", { name: "Inspector" }).getByRole("button", { name: "Sky", exact: true }).click();
    const panel = page.getByTestId("sky-panel");
    await expect(panel.locator(`[role="button"][data-dataset-id="${cube.id}"]`)).toBeVisible();
    await panel.getByLabel("Frame").selectOption(frame);
    await panel.getByRole("button", { name: "Fit the open dataset" }).click();

    // the panel's own view, saved without the survey: the reference for where the picture should look
    const lastId = async () => (await skyFigures(request)).at(-1)?.id ?? 0;
    let seen = await lastId();
    await panel.getByRole("button", { name: "Save as figure" }).click();
    await expect(panel.getByText(/It has no survey image/)).toBeVisible();
    const first = await savedAfter(request, seen);
    seen = first.id;
    const plain = first.sky;
    expect(plain.frame).toBe(frame);

    // the survey on (the stub answers its properties file), included in the picture
    await panel.getByRole("button", { name: "Choose a survey image…" }).click();
    await panel.getByRole("button", { name: /Contact alasky\.cds\.unistra\.fr and turn on/ }).click();
    await expect(panel.getByText(/Showing DSS2 colour/)).toBeVisible({ timeout: 30_000 });
    await panel.getByRole("checkbox", { name: "Include the survey image in the saved file" }).check();
    const bg = page.getByTestId("sky-background");
    await page.waitForTimeout(800);

    // not moved: Aladin looks where the panel's view does, in this frame (a frame mix-up would put it far away)
    await panel.getByRole("button", { name: "Save as figure" }).click();
    const one = await savedAfter(request, seen);
    seen = one.id;
    const still = one.sky;
    expect(still.background).not.toBeNull();
    const near = plain.fovDeg * 0.15;
    const dLon = Math.abs(((still.centre.lon - plain.centre.lon + 540) % 360) - 180);
    expect(Math.abs(still.centre.lat - plain.centre.lat)).toBeLessThan(near);
    expect(dLon * Math.cos((plain.centre.lat * Math.PI) / 180)).toBeLessThan(near);
    expect(still.footprints.length).toBeGreaterThan(0);

    // dragged by a quarter of the picture: the saved centre moved with it, by about that fraction of the field
    const w = (await bg.boundingBox())!.width;
    await drag(page, bg, w / 4, 0);
    await panel.getByRole("button", { name: "Save as figure" }).click();
    const two = await savedAfter(request, seen);
    seen = two.id;
    const moved = two.sky;
    const shift = Math.hypot(Math.abs(((moved.centre.lon - still.centre.lon + 540) % 360) - 180) * Math.cos((still.centre.lat * Math.PI) / 180), moved.centre.lat - still.centre.lat);
    expect(shift / still.fovDeg).toBeGreaterThan(0.15);
    expect(shift / still.fovDeg).toBeLessThan(0.4);
    expect(moved.fovDeg / still.fovDeg).toBeGreaterThan(0.9);
    expect(moved.fovDeg / still.fovDeg).toBeLessThan(1.1);

    // dragged right off the dataset: the picture shows no outline, so no figure is made, and the panel says why
    for (let i = 0; i < 8; i++) await drag(page, bg, w * 0.9, 0);
    await panel.getByRole("button", { name: "Save as figure" }).click();
    await expect(panel.getByRole("alert")).toContainText(/meets this field|no dataset/);
  });
}
