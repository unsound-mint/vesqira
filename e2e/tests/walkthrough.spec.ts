import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// One complete walk-through, from registering a cube to a supervisor's comment on a frozen board, on generated
// fixtures. It exercises the real API, the real Python worker and the built web app. The expected numbers come from
// the fixtures' closed-form definitions (fixtures/generate.py), not from the code under test.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const REVIEW = "http://127.0.0.1:8788";

const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const bigChannel = (page: Page) => page.getByRole("region", { name: "Channels" }).locator("strong").first();

async function register(page: Page, file: string) {
  await page.getByLabel("Register a FITS file").fill(path.join(data, file));
  await page.getByRole("button", { name: "Register" }).click();
  await expect(page.getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) })).toBeVisible();
}

async function open(page: Page, file: string) {
  await page.getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
}

/** Centre of image pixel (x, y), 0-based, y counted from the bottom, in page coordinates of the first image. */
async function pixelAt(page: Page, w: number, h: number, x: number, y: number) {
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  let box = (await canvas.boundingBox())!;
  for (let i = 0; i < 40; i++) { // wait until the canvas has its final size
    await page.waitForTimeout(100);
    const next = (await canvas.boundingBox())!;
    const stable = Math.abs(next.width - box.width) < 0.5 && Math.abs(next.height - box.height) < 0.5 && next.width > 100;
    box = next;
    if (stable) break;
  }
  return { x: box.x + ((x + 0.5) / w) * box.width, y: box.y + (1 - (y + 0.5) / h) * box.height };
}

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("register, read, region, orientation, compare, figure, share, review", async ({ page, browser, request }) => {
  await page.goto("/");

  // ---- register a cube; the header facts are shown as declared ----
  await register(page, "hi_cube.fits");
  await open(page, "hi_cube.fits");
  await expect(inspector(page).getByText("VRAD")).toBeVisible();
  await expect(page.getByRole("button", { name: /^\+1|^\+10|^-1/ })).toHaveCount(0); // keyboard, not step buttons

  // ---- channel control from the keyboard: arrows 1, Page keys 10, Home and End ----
  const slider = page.getByRole("slider", { name: "Channel" });
  await slider.focus();
  await page.keyboard.press("ArrowRight");
  await expect(bigChannel(page)).toHaveText("2");
  await page.keyboard.press("PageUp");
  await expect(bigChannel(page)).toHaveText("12");
  await page.keyboard.press("End");
  await expect(bigChannel(page)).toHaveText("64");
  await page.keyboard.press("Home");
  await expect(bigChannel(page)).toHaveText("1");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight");
  await expect(bigChannel(page)).toHaveText("41");
  await expect(page.locator('canvas[aria-label^="Channel 41 of 64"]')).toBeVisible();

  // ---- readouts at the synthetic source: value, sky position, velocity (from the fixture's definitions) ----
  const peak = await pixelAt(page, 32, 24, 20, 9);
  await page.mouse.move(peak.x, peak.y);
  await expect(inspector(page).getByText("x 21, y 10")).toBeVisible(); // FITS pixel numbers start at 1
  await expect(inspector(page).getByText(/^12\.\d+ K$/)).toBeVisible();
  await expect(inspector(page).getByText("149.94997°")).toBeVisible();
  await expect(inspector(page).getByText("1.98000°")).toBeVisible();
  await expect(inspector(page).getByText("8 km/s")).toBeVisible();

  // ---- missing data is not zero ----
  const nan = await pixelAt(page, 32, 24, 3, 3);
  await page.mouse.move(nan.x, nan.y);
  await expect(inspector(page).getByText("no value", { exact: true })).toBeVisible();
  await expect(inspector(page).getByText(/No value \(NaN or BLANK\): 9 of 768 pixels/)).toBeVisible();

  // ---- flagged is not missing; absent flags are not "valid" ----
  await slider.focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 10; i++) await page.keyboard.press("ArrowRight"); // channel 11 (flagged by the fixture)
  await expect(page.getByRole("contentinfo").getByText("Channel flagged by provider")).toBeVisible();
  await expect(page.getByText(/valid/i)).toHaveCount(0);
  await page.keyboard.press("PageUp");
  await page.keyboard.press("PageUp");
  await page.keyboard.press("PageUp");
  await page.keyboard.press("PageUp"); // channel 51
  await expect(page.getByRole("contentinfo").getByText("Channel not flagged")).toBeVisible();

  // ---- a region and its spectrum ----
  await slider.focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight");
  await page.getByRole("button", { name: "Circle" }).click();
  const a = await pixelAt(page, 32, 24, 20, 9);
  const b = await pixelAt(page, 32, 24, 23, 9);
  await page.mouse.move(a.x, a.y);
  await page.mouse.down();
  await page.mouse.move(b.x, b.y, { steps: 4 });
  await page.mouse.up();
  const plot = page.getByRole("img", { name: /Mean spectrum of the region over 64 channels/ });
  await expect(plot).toBeVisible();
  await expect(plot).toHaveAccessibleName(/at channel 41\./); // the source peaks in 0-based channel 40
  await expect(inspector(page).getByText(/29 of 29 pixels have a value/)).toBeVisible();
  await expect(inspector(page).getByText(/Mean of the finite pixels inside the region/)).toBeVisible();
  await expectNoAxeViolations(page);

  // ---- an orientation map: the unit is declared by the user, statistics are axial ----
  await register(page, "vgt_psi_deg.fits");
  await register(page, "vgt_reliability.fits");
  await open(page, "vgt_psi_deg.fits");
  await inspector(page).getByLabel("Treat this map as").selectOption("axial");
  await expect(inspector(page).getByRole("button", { name: "Declare axial" })).toBeDisabled(); // no unit, no declaration
  await inspector(page).getByLabel("Angle unit (required)").selectOption("deg");
  await inspector(page).getByLabel("Reliability map (optional)").selectOption({ label: "vgt_reliability.fits" }); // chosen by the user; the server checks the grid
  await inspector(page).getByRole("button", { name: "Declare axial" }).click();
  await expect(inspector(page).getByText(/Declared by you as axial orientation in degrees/)).toBeVisible();
  const section = inspector(page).getByRole("heading", { name: "Orientation" }).locator("xpath=ancestor::section");
  await expect(section.getByText("Mean orientation")).toBeVisible();
  // the angle readout says 0 and 180 are the same orientation
  const px = await pixelAt(page, 4, 4, 1, 0);
  await page.mouse.move(px.x, px.y);
  await expect(inspector(page).getByText(/179\.00° \(axial: 0° = 180°\)/)).toBeVisible();
  // 1 and 179 degrees average to 0, not 90
  await page.getByRole("button", { name: "Rectangle" }).click();
  for (const [label, value] of [["x from", "1"], ["y from", "1"], ["x to", "2"], ["y to", "1"]] as const) await inspector(page).getByLabel(label, { exact: true }).fill(value);
  await inspector(page).getByRole("button", { name: "Set region" }).click();
  await expect(section.getByText("0.00°", { exact: true }).first()).toBeVisible();
  // 0 and 90 degrees cancel: no mean exists
  for (const [label, value] of [["y from", "4"], ["y to", "4"]] as const) await inspector(page).getByLabel(label, { exact: true }).fill(value);
  await inspector(page).getByRole("button", { name: "Set region" }).click();
  await expect(section.getByText("undefined").first()).toBeVisible();
  await expect(section.getByText(/orientations cancel/).first()).toBeVisible();
  await expect(inspector(page).getByText(/not defined for angles of an axial map/)).toBeVisible();
  // and the server refuses an arithmetic mean of angles outright
  const list = (await (await request.get("http://127.0.0.1:8787/api/datasets")).json()) as { datasets: { id: number; name: string }[] };
  const psi = list.datasets.find((d) => d.name === "vgt_psi_deg.fits")!;
  expect((await request.get(`http://127.0.0.1:8787/api/datasets/${psi.id}/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1`)).status()).toBe(409);

  // ---- comparing: linked only when the grids are confirmed the same ----
  await page.getByLabel("Compare with").selectOption({ label: "vgt_reliability.fits" });
  await expect(page.getByText("Same grid: cursor and channel are linked")).toBeVisible();
  await page.getByLabel("Compare with").selectOption("");
  await register(page, "hi_cube_coarser.fits");
  await open(page, "hi_cube.fits");
  await page.getByLabel("Compare with").selectOption({ label: "hi_cube_coarser.fits" });
  await expect(page.getByText(/not linked, nothing is resampled/)).toBeVisible();
  await page.getByText(/^Why \(/).click();
  await expect(page.getByText("axis 1 step differs")).toBeVisible();
  await page.getByLabel("Compare with").selectOption("");

  // ---- a figure that keeps its source, on a board ----
  await page.getByRole("slider", { name: "Channel" }).focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight");
  await page.getByRole("button", { name: "Figure", exact: true }).click();
  await inspector(page).getByLabel("Title").first().fill("H I peak at 8 km/s");
  await inspector(page).getByRole("button", { name: "Save as figure" }).click();
  await expect(inspector(page).getByText(/Saved: “H I peak at 8 km\/s”/)).toBeVisible();
  await inspector(page).getByLabel("New board name").fill("Demo board");
  await inspector(page).getByRole("button", { name: "Add to board" }).click();
  await expect(inspector(page).getByText("Added to the board.")).toBeVisible();

  // ---- share: the board is frozen and a link is created once ----
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await expect(page.getByRole("heading", { name: "H I peak at 8 km/s" })).toBeVisible();
  await page.getByText("Where this came from").click();
  await expect(page.getByText(/bytes were not hashed/)).toBeVisible();
  await page.getByLabel("Note for the reviewer").fill("Check the unit of the colour bar.");
  await page.getByLabel("Note for the reviewer").blur();
  await page.getByLabel("Label").fill("Group meeting draft");
  await page.getByRole("button", { name: "Freeze and create link" }).click();
  const link = await page.getByLabel("Review link").inputValue();
  expect(link.startsWith(`${REVIEW}/#/review/`)).toBe(true);

  // ---- the supervisor: previews and provenance only, never data or paths ----
  const supCtx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const sup = await supCtx.newPage();
  await sup.goto(link);
  await expect(sup.getByRole("heading", { name: "Demo board" })).toBeVisible();
  await expect(sup.getByText("Check the unit of the colour bar.")).toBeVisible();
  await expect(sup.getByText(/not the data, file paths or any credential/)).toBeVisible();
  const text = await sup.locator("body").innerText();
  expect(text).not.toContain(".run/data");
  expect(text).not.toMatch(/\/home\/|\/tmp\//);
  for (const p of ["/api/datasets", "/api/roots", "/api/figures", "/api/boards"]) {
    expect(await sup.evaluate(async (u) => (await fetch(u)).status, p)).toBe(404); // the owner API does not exist there
  }
  await expectNoAxeViolations(sup);
  await sup.getByLabel(/Your name/).fill("Prof. Reviewer");
  await sup.getByRole("textbox", { name: "Comment", exact: true }).fill("Why the 1st to 99th percentile limits?");
  await sup.getByRole("button", { name: "Send comment" }).click();
  await expect(sup.getByText("Why the 1st to 99th percentile limits?")).toBeVisible();

  // ---- back with the author: the comment is there; revoking ends access ----
  await page.getByRole("button", { name: "Comments" }).click();
  await expect(page.getByText(/Prof\. Reviewer.*Why the 1st to 99th percentile limits\?/)).toBeVisible();
  await page.getByRole("button", { name: "Revoke link" }).click();
  await sup.reload();
  await expect(sup.getByRole("alert")).toHaveText(/not valid, has expired, or was revoked/);
  await supCtx.close();
});

test("the workbench is usable in the light theme and at phone width", async ({ browser }) => {
  for (const [scheme, width, height] of [["light", 1440, 900], ["dark", 390, 844]] as const) {
    const ctx = await browser.newContext({ colorScheme: scheme, viewport: { width, height } });
    const page = await ctx.newPage();
    await page.goto("/");
    if (width <= 800) await page.getByRole("button", { name: "Toggle explorer" }).click();
    await page.getByRole("button", { name: /^hi_cube\.fits/ }).click();
    if (width <= 800) await page.getByRole("button", { name: "Toggle explorer" }).click();
    await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
    await expectNoAxeViolations(page);
    await ctx.close();
  }
});

test("a VGT run is imported with its parameters, linked to its outputs and carried to the reviewer", async ({ page, browser }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Runs", exact: false }).first().click().catch(() => undefined);
  await page.getByLabel("Import a VGT run").fill(path.join(data, "vgt_run_demo"));
  await page.getByRole("button", { name: "Import run" }).click();
  await expect(page.getByRole("listitem").filter({ hasText: "vgt_run_demo" })).toBeVisible();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();

  // the run, its code version, and the honest label for an uncommitted tree
  const insp = inspector(page);
  await expect(insp.getByText("Not reproducible from the commit: uncommitted changes")).toBeVisible();
  await expect(insp.getByText(/No registered file matches it by name and size/)).toBeVisible();
  await insp.getByText(/^Parameters \(/).click();
  await expect(insp.getByRole("rowheader", { name: "fitter_mode" })).toBeVisible();
  await expect(insp.getByText("input_file")).toHaveCount(0); // locations are not kept

  // the adapter proposed the orientation: shown as the run import's, not the file's and not the user's
  await expect(insp.getByText(/Proposed by the run import \(vgt-run-adapter\)/)).toBeVisible();
  await expect(insp.getByText("Mean orientation")).toBeVisible();

  // figure -> board -> reviewer sees the run's parameters and the dirty commit, never a path
  await page.getByRole("button", { name: "Figure", exact: true }).click();
  await insp.getByLabel("Title").first().fill("Demo run psi");
  await insp.getByRole("button", { name: "Save as figure" }).click();
  await expect(insp.getByText(/Saved: “Demo run psi”/)).toBeVisible();
  await insp.getByLabel("New board name").fill("Run board");
  await insp.getByRole("button", { name: "Add to board" }).click();
  await expect(insp.getByText("Added to the board.")).toBeVisible();
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await page.getByLabel("Board", { exact: true }).selectOption({ label: "Run board" });
  await page.getByRole("button", { name: "Freeze and create link" }).click();
  const link = await page.getByLabel("Review link").inputValue();
  const ctx = await browser.newContext();
  const sup = await ctx.newPage();
  await sup.goto(link);
  await sup.getByText("Where this came from and what it does not show").click();
  await expect(sup.getByText(/uncommitted changes, so not reproducible from the commit/)).toBeVisible();
  await sup.getByText(/^Run parameters \(/).click();
  await expect(sup.getByText("axial_vonmises").first()).toBeVisible();
  const text = await sup.locator("body").innerText();
  expect(text).not.toMatch(/\/tmp\/|\/home\/|vgt_inputs|validation_output|\.run\/data/);
  await ctx.close();
});

test("a cube on another machine is read in place and agrees with the same cube here", async ({ page }) => {
  const remoteRoot = path.resolve(here, "../.run/remote_data");
  await page.goto("/");
  await register(page, "hi_cube.fits");
  // add the host: your own ssh and agent do the connecting (a stand-in ssh here); nothing asks for keys
  await page.getByRole("button", { name: /^Hosts/ }).click();
  await expect(page.getByText(/Keys and passwords are never read by Vesqira/)).toBeVisible();
  await page.getByLabel("Host name").fill("Second laptop");
  await page.getByLabel("SSH destination").fill("laptop2");
  await page.getByLabel("Python on the host").fill(readFileSync(path.resolve(here, "../.run/python"), "utf8").trim());
  await page.getByLabel(/Source folders/).fill(remoteRoot);
  await page.getByRole("button", { name: "Add host" }).click();
  const host = page.getByRole("region", { name: "Host Second laptop" });
  await host.getByRole("button", { name: "Check connection" }).click();
  await expect(host.getByText(/The host enforces its source folders itself/)).toBeVisible();
  await host.getByRole("button", { name: "Browse" }).click();
  await host.getByRole("button", { name: /remote_cube\.fits/ }).click();
  await expect(page.getByRole("button", { name: /^remote_cube\.fits.*Second laptop/ })).toBeVisible();

  // the remote cube opens, and the same pixel reads the same value as the local copy
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /remote_cube\.fits/ }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const slider = page.getByRole("slider", { name: "Channel" });
  await slider.focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight");
  await expect(bigChannel(page)).toHaveText("41");
  const at = await pixelAt(page, 32, 24, 20, 9);
  await page.mouse.move(at.x, at.y);
  const remoteValue = await inspector(page).getByText(/^\d+\.\d+ K$/).first().innerText();
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  await page.getByRole("slider", { name: "Channel" }).focus();
  await page.keyboard.press("Home");
  for (let i = 0; i < 40; i++) await page.keyboard.press("ArrowRight");
  const at2 = await pixelAt(page, 32, 24, 20, 9);
  await page.mouse.move(at2.x, at2.y);
  await expect(inspector(page).getByText(remoteValue, { exact: true })).toBeVisible();

  // compare the cube on the other machine with the one here: same grid, so linked
  await page.getByLabel("Compare with").selectOption({ label: "remote_cube.fits" });
  await expect(page.getByText("Same grid: cursor and channel are linked")).toBeVisible();
});
