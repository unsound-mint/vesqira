import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// One complete walk-through, from registering a cube to a supervisor's comment on a frozen board, on generated
// fixtures. It exercises the real API, the real Python worker and the built web app. The expected numbers come from
// the fixtures' closed-form definitions (fixtures/generate.py), not from the code under test.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
import { API, REVIEW } from "../ports";

const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });
const bigChannel = (page: Page) => page.getByRole("region", { name: "Channels" }).locator("strong").first();

/** Picks a source folder on the host the way a person does: from the host's home, going up and into folders. */
async function chooseHostFolder(page: Page, target: string) {
  const home = path.resolve(here, "../.run/remote-home");
  await page.getByRole("button", { name: /Choose folder|Add another folder/ }).click();
  const list = page.getByRole("list", { name: /^Folders on / });
  await expect(list).toBeVisible();
  const up = path.relative(home, target).split(path.sep);
  for (const step of up) {
    await list.getByRole("button", { name: step === ".." ? ".." : new RegExp(`^${step}$`) }).click();
    await expect(page.getByRole("button", { name: "Use this folder" })).toBeEnabled();
  }
  await page.getByRole("button", { name: "Use this folder" }).click();
}

async function register(page: Page, file: string) {
  await page.getByLabel("Register a FITS file").fill(path.join(data, file));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await expect(page.getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) })).toBeVisible();
  await expect(page.getByLabel("Register a FITS file")).toHaveValue(""); // the form clears itself once the registration has finished
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

  // ---- the same pixel in other units and frames (ADR 0040): converted rows beside the native ones, with their basis ----
  await page.mouse.click(peak.x, peak.y); // pin the pixel so the pointer can leave the image for the controls
  await inspector(page).getByLabel("Spectral unit").selectOption("GHz");
  await expect(inspector(page).getByText(/^1\.420367\d+ GHz/)).toBeVisible(); // f0 (1 - v/c) for 8 km/s, radio, from the file's RESTFRQ
  await inspector(page).getByLabel("Sky frame").selectOption("galactic");
  await expect(inspector(page).getByText("l (Galactic)")).toBeVisible();
  await inspector(page).getByLabel("Value unit").selectOption("mK");
  await expect(inspector(page).getByText(/^12\d{3}(\.\d+)? mK$/)).toBeVisible();
  await expect(inspector(page).getByText(/doppler_radio/)).toBeVisible();
  await expect(inspector(page).getByText(/12\.\d+ K$/)).toBeVisible(); // the native value is still listed
  await expect(inspector(page).getByText(/a frame change such as TOPOCENT to LSRK is not offered/)).toBeVisible();
  await expectNoAxeViolations(page);
  await inspector(page).getByLabel("Spectral unit").selectOption("");
  await inspector(page).getByLabel("Sky frame").selectOption("");
  await inspector(page).getByLabel("Value unit").selectOption("");
  await page.keyboard.press("Escape"); // release the pinned pixel

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

  // ---- zoom, the cursor from the keyboard, and the colour limits on a histogram ----
  const plane = page.getByRole("group", { name: "Plane viewer" });
  await plane.focus();
  const pixelReadout = inspector(page).locator("dd").filter({ hasText: /^x \d+, y \d+$/ }).first();
  const beforePixel = await pixelReadout.innerText();
  await page.keyboard.press("Shift+ArrowRight");
  await expect(pixelReadout).not.toHaveText(beforePixel);
  await page.keyboard.press("+");
  await expect(page.getByRole("button", { name: "Zoom out" })).toBeEnabled();
  await page.keyboard.press("0");
  await expect(page.getByRole("button", { name: "Zoom out" })).toBeDisabled();
  await expect(page.getByRole("img", { name: /Histogram of \d+ finite pixels/ })).toBeVisible();
  await page.getByLabel("Lower limit").fill("0.5");
  await page.getByLabel("Lower limit").press("Enter");
  await expect(page.getByLabel("Range")).toHaveValue("custom");
  await expect(page.getByLabel("Lower limit")).toHaveValue("0.5");
  await page.getByLabel("Range").selectOption("percentile");
  await page.getByText("Spectrum values as a table").click();
  await expect(page.getByRole("region", { name: "Spectrum table" })).toBeVisible();

  // ---- saved regions: keep, rename, replace the shape, copy, delete, undo, export as DS9 ----
  await inspector(page).getByLabel("Save the current region as").fill("Peak core");
  await inspector(page).getByRole("button", { name: "Save", exact: true }).click();
  await expect(inspector(page).getByRole("button", { name: "Peak core" })).toBeVisible();
  await inspector(page).getByRole("button", { name: "Rename", exact: true }).click();
  await inspector(page).getByLabel("New name for Peak core").fill("Source A");
  await inspector(page).getByRole("button", { name: "Rename", exact: true }).click();
  await expect(inspector(page).getByRole("button", { name: "Source A" })).toBeVisible();
  await inspector(page).getByRole("button", { name: "Duplicate" }).click();
  await expect(inspector(page).getByRole("button", { name: "Source A copy" })).toBeVisible();
  const reg = await request.get(await inspector(page).getByRole("link", { name: "Export all as a DS9 region file (.reg)" }).getAttribute("href").then((h) => `${API}${h}`));
  const regText = await reg.text();
  expect(reg.status()).toBe(200);
  expect(regText).toContain("image\n");
  expect(regText).toContain("circle(21,10,3) # text={Source A}");
  await inspector(page).getByRole("button", { name: "Delete", exact: true }).last().click();
  await expect(inspector(page).getByRole("button", { name: "Source A copy" })).toHaveCount(0);
  await inspector(page).getByRole("button", { name: "Undo" }).click();
  await expect(inspector(page).getByRole("button", { name: "Source A copy" })).toBeVisible();
  // import a DS9 file: a preview first, then only what is confirmed is saved
  await inspector(page).getByLabel("Import a DS9 region file (image coordinates)").setInputFiles({ name: "from-ds9.reg", mimeType: "text/plain", buffer: Buffer.from("image\nbox(5,5,3,3,0) # text={Imported box}\ncircle(8.4,8,2.5)\nellipse(1,1,2,2,0)\n") });
  await expect(inspector(page).getByText("1 exact, 1 approximated, 1 skipped.")).toBeVisible();
  await expect(inspector(page).getByText(/not supported/)).toBeVisible();
  await expect(inspector(page).getByRole("button", { name: "Imported box" })).toHaveCount(0);   // nothing saved yet
  await inspector(page).getByRole("button", { name: "Add 1 region" }).click();
  await expect(inspector(page).getByRole("button", { name: "Imported box" })).toBeVisible();
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/regions.png` });
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
  const list = (await (await request.get(`${API}/api/datasets`)).json()) as { datasets: { id: number; name: string }[] };
  const psi = list.datasets.find((d) => d.name === "vgt_psi_deg.fits")!;
  expect((await request.get(`${API}/api/datasets/${psi.id}/spectrum?shape=rect&x0=0&y0=0&x1=1&y1=1`)).status()).toBe(409);

  // ---- comparing: linked only when the grids are confirmed the same ----
  await page.getByLabel("Compare with").selectOption({ label: "vgt_reliability.fits" });
  await expect(page.getByText("Same grid: cursor and channel are linked")).toBeVisible();
  await page.getByLabel("Compare with").selectOption("");
  await register(page, "hi_cube_coarser.fits");
  await open(page, "hi_cube.fits");
  await page.getByLabel("Compare with").selectOption({ label: "hi_cube_coarser.fits" });
  await expect(page.getByText(/not linked, nothing is resampled/)).toBeVisible();
  const sameScale = page.getByLabel("Same colour scale for both maps");
  await expect(sameScale).toBeEnabled();
  await sameScale.check();
  await expect(page.getByLabel("Range")).toHaveValue("custom");          // both maps now use one pair of limits
  if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/shared-scale.png` });
  await sameScale.uncheck();
  await expect(page.getByLabel("Range")).toHaveValue("percentile");
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
  await page.getByLabel("What should the reviewer look at?").fill("Is the colour stretch hiding the edge?");
  await page.getByRole("button", { name: "Check what will be shared" }).click();
  const region = page.getByRole("region", { name: "What the reviewer will see" });
  await expect(region).toContainText("exactly this and nothing else");
  await expect(region).toContainText("H I peak at 8 km/s");
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
  await expect(sup.getByText("Is the colour stretch hiding the edge?")).toBeVisible();
  await expectNoAxeViolations(sup);
  await sup.getByLabel(/Your name/).fill("Prof. Reviewer");
  await sup.getByRole("textbox", { name: "Comment", exact: true }).fill("Why the 1st to 99th percentile limits?");
  await sup.getByRole("button", { name: "Send comment" }).click();
  await expect(sup.getByText("Why the 1st to 99th percentile limits?")).toBeVisible();

  await sup.getByLabel("Changes requested").check();
  await sup.getByRole("button", { name: "Send outcome" }).click();
  await expect(sup.getByText(/Currently: Changes requested/)).toBeVisible();

  // ---- back with the author: the comment is there; revoking ends access ----
  await page.getByRole("button", { name: "Comments and outcome" }).click();
  await expect(page.getByText(/Prof\. Reviewer.*Why the 1st to 99th percentile limits\?/)).toBeVisible();
  await expect(page.getByText(/Reviewer: Changes requested \(Prof\. Reviewer\)/)).toBeVisible();
  await page.getByLabel("Reply to Prof. Reviewer").fill("Rescaled in the next version.");
  await page.getByRole("button", { name: "Reply", exact: true }).click();
  await expect(page.getByText("Rescaled in the next version.")).toBeVisible();
  await sup.reload();
  await expect(sup.getByText("Rescaled in the next version.")).toBeVisible();

  // ---- presenting, downloading and printing the board ----
  await page.getByRole("button", { name: "Present" }).click();
  const stage = page.getByRole("dialog", { name: /Presenting/ });
  await expect(stage).toBeVisible();
  await page.screenshot({ path: process.env.VESQIRA_SHOTS ? `${process.env.VESQIRA_SHOTS}/present.png` : undefined });
  await page.keyboard.press("Escape");
  await expect(stage).toBeHidden();
  const href = await page.getByRole("link", { name: "Download PNG" }).first().getAttribute("href");
  const png = await page.request.get(href!);
  expect(png.status()).toBe(200);
  expect(png.headers()["content-disposition"]).toContain("attachment");
  expect((await png.body()).includes(Buffer.from("iTXt"))).toBe(true);
  const printPage = await page.context().newPage();
  await printPage.goto(`${new URL(page.url()).origin}/#/print/1`);
  await expect(printPage.getByRole("button", { name: "Print or save as PDF" })).toBeVisible();
  await expect(printPage.getByText(/bytes were not hashed/)).toBeVisible();
  await printPage.close();
  await page.getByRole("button", { name: "Revoke link" }).click();
  await sup.reload();
  await expect(sup.getByRole("alert")).toHaveText(/not valid, has expired, or was revoked/);
  await supCtx.close();
});

test("the workbench is usable in the light theme and at phone width", async ({ browser }) => {
  for (const [scheme, width, height] of [["light", 1440, 900], ["dark", 1280, 800], ["dark", 390, 844]] as const) {
    const ctx = await browser.newContext({ colorScheme: scheme, viewport: { width, height } });
    const page = await ctx.newPage();
    await page.goto("/");
    if (width <= 800) await page.getByRole("button", { name: "Toggle explorer" }).click();
    await page.getByRole("button", { name: /^hi_cube\.fits/ }).click();
    if (width <= 800) await page.getByRole("button", { name: "Toggle explorer" }).click();
    await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
    await expectNoAxeViolations(page);

    // presenting the board from the first test: a modal that fits the screen, passes axe, and gives focus back
    await page.getByRole("button", { name: "Boards", exact: true }).click();
    await expect(page.getByRole("heading", { name: "H I peak at 8 km/s" })).toBeVisible();
    const opener = page.getByRole("button", { name: "Present" });
    await opener.click();
    const stage = page.getByRole("dialog", { name: /Presenting/ });
    await expect(stage).toBeVisible();
    await expectNoAxeViolations(page);
    const hint = (await page.getByText(/← → move/).boundingBox())!;
    expect(hint.y + hint.height, "the hint line is on screen, so the picture above it fits").toBeLessThanOrEqual(height);
    if (process.env.VESQIRA_SHOTS) await page.screenshot({ path: `${process.env.VESQIRA_SHOTS}/present-${scheme}-${width}.png` });
    for (let i = 0; i < 8; i++) {
      await page.keyboard.press("Tab");
      expect(await stage.evaluate((el) => el.contains(document.activeElement)), "Tab stays inside the dialog").toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(stage).toBeHidden();
    await expect(opener).toBeFocused();
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
  // the inspector keeps a summary; the run itself is a page of blocks
  await insp.getByRole("button", { name: "Open run page" }).click();
  const runPage = page.getByRole("article", { name: "Run vgt_run_demo" });
  await expect(runPage.getByText(/No registered file matches it by name and size/)).toBeVisible();
  await expect(runPage.getByRole("rowheader", { name: "fitter_mode" })).toBeVisible();
  await expect(runPage.getByText("input_file")).toHaveCount(0); // locations are not kept
  await runPage.getByRole("button", { name: "Back" }).click();

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
  await chooseHostFolder(page, remoteRoot);
  await page.getByRole("button", { name: "Add host" }).click();
  const host = page.getByRole("region", { name: "Host Second laptop" });
  await host.getByRole("button", { name: "Check connection" }).click();
  await expect(host.getByText(/The host enforces its source folders itself/)).toBeVisible();
  await host.getByRole("button", { name: "Browse", exact: true }).click();
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
  await expect(inspector(page).getByText(/preview only/)).toHaveCount(0); // a reduced preview gives no pixel value; wait for the full plane
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
  await page.getByLabel("Compare with").selectOption({ label: "remote_cube.fits (Second laptop)" });
  await expect(page.getByText("Same grid: cursor and channel are linked")).toBeVisible();
});

test("a Slurm job: render, read, submit exactly that text, follow it, import its results as a linked run", async ({ page }) => {
  const remoteRoot = path.resolve(here, "../.run/remote_data");
  const state = path.resolve(here, "../.run/slurm-state/4242");
  await page.goto("/");
  await page.getByRole("button", { name: /^Hosts/ }).click();
  await page.getByLabel("Host name").fill("Cluster");
  await page.getByLabel("SSH destination").fill("login1");
  await chooseHostFolder(page, remoteRoot);
  await page.getByRole("button", { name: "Add host" }).click();
  await expect(page.getByRole("region", { name: "Host Cluster" })).toBeVisible();

  await page.getByRole("button", { name: "Jobs", exact: true }).click();
  await page.getByLabel("Job name").fill("vgt-ker8");
  await page.getByLabel("VGT folder on the host").fill("/shared/home/u/vgt");
  await page.getByLabel("Base config on the host").fill(path.join(remoteRoot, "configs", "vgt.toml"));
  await page.getByRole("button", { name: "Load settings" }).click();
  await expect(page.getByLabel("ker (smoothing, pixels)")).toHaveValue("4"); // the form starts from the real configuration
  await page.getByLabel("ker (smoothing, pixels)").fill("8");
  await page.getByLabel("noise mode").selectOption("channel");
  await page.getByRole("button", { name: "Create draft and show the script" }).click();

  // the exact script is shown, and carries the changes and an output folder inside the host's roots
  const script = page.getByRole("textbox", { name: /Script/ });
  await expect(script).toHaveValue(/ker = 8/);
  await expect(script).toHaveValue(/noise_mode = "channel"/);
  const text = await script.inputValue();
  const outDir = /output_dir = "([^"]+)"/.exec(text)![1]!;
  expect(outDir.startsWith(remoteRoot)).toBe(true);
  await expect(page.getByText(/no extra approval step: the script above is what runs/)).toBeVisible();

  // editing makes you save before you can submit: what is sent is what you saw
  await script.fill(text.replace('echo "job_id', 'echo "reviewed; job_id'));
  await expect(page.getByRole("button", { name: "Submit this script" })).toBeDisabled();
  await page.getByRole("button", { name: "Save edits" }).click();
  await expect(page.getByRole("button", { name: "Submit this script" })).toBeEnabled();
  await page.getByRole("button", { name: "Submit this script" }).click();
  await expect(page.getByText(/Slurm job 4242/)).toBeVisible();
  await expect(page.getByRole("complementary").or(page.locator("body")).getByText("submitted").first()).toBeVisible();

  // the scheduler moves it on; the status follows
  writeFileSync(state, "RUNNING\n");
  await page.getByRole("button", { name: "Refresh status" }).click();
  await expect(page.locator("body")).toContainText("running");
  await page.getByRole("button", { name: "Show log" }).click();
  await expect(page.getByLabel("Job log")).toContainText("fake slurm log for job 4242");

  // it finishes and writes its run where the config said; the results come back as a run linked to the job
  execFileSync("python3", ["-c", "import sys, pathlib, generate; generate.write_vgt_run(pathlib.Path(sys.argv[1]), metrics=True)", outDir], { cwd: path.resolve(here, "../../fixtures") });
  writeFileSync(state, "COMPLETED\n");
  await page.getByRole("button", { name: "Refresh status" }).click();
  await expect(page.locator("body")).toContainText("exit 0:0");
  await page.getByRole("button", { name: "Import results as a run" }).click();
  await expect(page.getByText("Results imported.")).toBeVisible();
  await page.getByRole("button", { name: "Open the run" }).click();
  const runPage = page.getByRole("article", { name: /^Run / });
  await expect(runPage.getByText(/completed · job 4242/)).toBeVisible(); // the run knows its job
  await expect(runPage.getByText("0.344 ± 0.02 (standard error), n = 2695")).toBeVisible(); // its declared results, with their kind of uncertainty
  await expect(runPage.getByText(/As declared by the run; Vesqira did not compute or check them/)).toBeVisible();
  await runPage.getByRole("button", { name: "Open in the viewer" }).first().click();
  const insp = inspector(page);
  await expect(insp.getByText(/vgt-ker8 · 4242 · completed/)).toBeVisible();
  await expect(insp.getByText(/Proposed by the run import \(vgt-run-adapter\)/)).toBeVisible();
});

test("analysis makes new files with their provenance, and keeps missing data missing", async ({ page }) => {
  await page.goto("/");
  await register(page, "hi_cube.fits");
  await register(page, "hi_cube_coarser.fits");
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const insp = inspector(page);
  await page.getByRole("button", { name: "Analysis", exact: true }).click();

  // statistics: the 3 x 3 block of NaN pixels in every channel is counted as no value, not as zeros
  await insp.getByLabel("Analysis", { exact: true }).selectOption("stats");
  await insp.getByRole("button", { name: "Compute statistics" }).click();
  await expect(insp.getByText("576", { exact: true })).toBeVisible(); // 9 pixels x 64 channels
  await expect(insp.getByText(/x 21, y 10, channel 41/)).toBeVisible(); // the synthetic source

  // a moment map becomes a new dataset that says how it was made
  await insp.getByLabel("Analysis", { exact: true }).selectOption("moment");
  await insp.getByLabel("Only values at or above (optional)").fill("1");
  await insp.getByRole("button", { name: "Create the new file" }).click();
  await expect(page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^moment-/ })).toBeVisible();
  await page.getByRole("button", { name: "Made from", exact: true }).click();
  await expect(insp.getByText("hi_cube.fits").first()).toBeVisible();
  await expect(insp.getByText(/a pixel with no selected value is NaN, never 0/)).toBeVisible();
  await expect(insp.getByText(/threshold=1/)).toBeVisible();

  // aligning onto the coarser grid says plainly what it does and does not do
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ }).click();
  await insp.getByLabel("Analysis", { exact: true }).selectOption("align");
  await insp.getByLabel("Align onto the grid of").selectOption({ label: "hi_cube_coarser.fits" });
  await insp.getByRole("button", { name: "Create the new file" }).click();
  await expect(page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^align-/ })).toBeVisible();
  await expect(insp.getByText(/does not match angular resolution/).first()).toBeVisible();
});

test("the context-supplement gaps: preview, managed copy, nature, assessment, axial difference, beam", async ({ page }) => {
  await page.goto("/");
  // preview before registering: sky WCS, spectral axis and the cost of reading
  await page.getByLabel("Register a FITS file").fill(path.join(data, "hi_cube.fits"));
  await page.getByRole("button", { name: "Preview before registering" }).click();
  await expect(page.getByText(/64 planes of about 3\.00 KiB each/)).toBeVisible();
  await expect(page.getByText(/Sky coordinates: declared/)).toBeVisible();
  await page.getByLabel("Register a FITS file").fill("");
  await register(page, "hi_cube.fits");
  await open(page, "hi_cube.fits");
  const insp = inspector(page);
  // pixel scale and beam are different things
  await expect(insp.getByText("36.00 × 36.00 arcsec")).toBeVisible();
  await expect(insp.getByText("108.00 × 72.00 arcsec, position angle 45°")).toBeVisible();
  // what the data is, and a managed copy that says it is a byte-for-byte copy
  await insp.getByLabel("What this data is").selectOption("constructed_control");
  await expect(insp.getByText(/must not be read as an observation/)).toBeVisible();
  await page.getByRole("button", { name: "Storage", exact: true }).click();
  await insp.getByRole("button", { name: /Make a managed copy/ }).click();
  await expect(insp.getByText(/Copied\. SHA-256/)).toBeVisible();

  // two declared orientation maps: compare, see what differs, and make the axial difference
  await register(page, "vgt_psi_deg.fits");
  await register(page, "vgt_psi_rad.fits");
  for (const [file, unit] of [["vgt_psi_deg.fits", "deg"], ["vgt_psi_rad.fits", "rad"]] as const) {
    await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).click();
    await expect(insp.getByLabel("Treat this map as")).toBeVisible();
    await insp.getByLabel("Treat this map as").selectOption("axial");
    await insp.getByLabel("Angle unit (required)").selectOption(unit);
    await insp.getByRole("button", { name: "Declare axial" }).click();
    await expect(insp.getByText(/Declared by you as axial orientation/)).toBeVisible();
  }
  await page.getByLabel("Compare with").selectOption({ label: "vgt_psi_deg.fits" });
  await expect(page.getByText("Same grid: cursor and channel are linked")).toBeVisible();
  await expect(insp.getByText(/not better agreement/)).toBeVisible();
  await insp.getByRole("button", { name: /Axial difference map/ }).click();
  await expect(page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^axial_diff-/ })).toBeVisible();
});
