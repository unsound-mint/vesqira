import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import path from "node:path";
import { API } from "../ports";
import { fileURLToPath } from "node:url";

// Virtual subsets (ADR 0063) in the real app: define a window onto a cube (a box, every second pixel, a channel range) without
// writing anything, see it listed under its source marked as virtual, read what it resolved to and a coordinate through it, then write
// it as an ordinary file and find that dataset. Runs last (the name): it adds datasets to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("a subset is defined, marked virtual, browsed in the viewer through its window and written as an ordinary file", async ({ page, request }) => {
  await page.goto("/");
  await page.getByLabel("Register a FITS file").fill(path.join(data, "hi_cube_rotated.fits"));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  const files = page.getByRole("list", { name: "Datasets" });
  const row = files.getByRole("button", { name: /^hi_cube_rotated\.fits/ });
  const offer = page.getByRole("status", { name: "A dataset already holds this content" });
  await expect(row.first().or(offer)).toBeVisible();
  if (await offer.isVisible()) await offer.getByRole("button", { name: "Register as a separate dataset" }).click();
  await row.first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const insp = page.getByRole("complementary", { name: "Inspector" });

  await insp.getByRole("button", { name: /Virtual subsets/ }).click();
  await insp.getByRole("button", { name: "New virtual subset" }).click();
  const form = insp.getByRole("form", { name: "Virtual subset" });
  await form.getByLabel("Name").fill("Core, every second pixel");
  await form.getByLabel("Reason").fill("the source and its surroundings for the comparison");
  await form.getByLabel("Area").selectOption("box");
  await form.getByLabel("x from").fill("4");
  await form.getByLabel("x to").fill("21");
  await form.getByLabel("y from").fill("3");
  await form.getByLabel("y to").fill("16");
  await form.getByLabel("Keep every N-th pixel").fill("2");
  await form.getByLabel("Spectral range").selectOption("channels");
  await form.getByLabel("From channel").fill("11");
  await form.getByLabel("To channel").fill("41");
  await form.getByRole("button", { name: "Save the subset" }).click();

  // the subset opens in the viewer, read through its window: nothing was written, and the inspector says what it is
  const subsetLine = insp.getByTestId("subset-line");
  await expect(subsetLine).toHaveText("virtual subset of hi_cube_rotated.fits · version 1 · box x 4–21, y 3–16 every 2nd pixel · channels 11–41");
  const canvas = page.locator('canvas[aria-label*="pixels"]').first();
  await expect(canvas).toBeVisible();
  await expect(canvas).toHaveAttribute("aria-label", /9 by 7 pixels/);
  await expect(page.getByRole("slider", { name: "Channel" })).toBeVisible();
  const resolved = insp.getByRole("region", { name: "What the window resolved to" });
  await expect(resolved).toContainText("9 by 7 pixels, 31 channels");
  await expect(resolved).toContainText("x 4 to 20, y 3 to 15, every 2 of 32 by 24");
  await expect(resolved).toContainText("11 to 41 of 64");
  // what does not apply to a window is not offered, with the reason
  await expect(insp.getByText(/Masks, saved regions, overlays, channel maps and analyses are the file's/)).toBeVisible();
  await expect(insp.getByRole("button", { name: /^Overlays|^Masks|^Analysis|^Figure/ })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "Channel maps" })).toHaveCount(0);

  // listed under its source, marked
  const sub = files.getByRole("button", { name: /Core, every second pixel/ });
  await expect(sub).toContainText("virtual subset");
  await expect(sub).toHaveAttribute("aria-current", "true");

  // a readout through the window: pixel (1, 1) of channel 1 of the subset is source pixel (4, 3) of channel 11, by the source's own WCS
  const box = (await canvas.boundingBox())!;
  await page.mouse.move(box.x + 2, box.y + box.height - 2);
  const readout = insp.locator("section", { has: page.getByRole("heading", { name: "Coordinates" }) });
  await expect(readout).toContainText("x 1, y 1");
  const sources = (await (await request.get(`${API}/api/datasets`)).json()).datasets as { id: number; name: string }[];
  const srcId = sources.find((d) => d.name === "hi_cube_rotated.fits")!.id;
  const want = await (await request.get(`${API}/api/datasets/${srcId}/coord?x=3&y=2&channel=10`)).json();
  const num = async (label: string) => parseFloat((await insp.locator("dt").filter({ hasText: new RegExp(`^${label}$`) }).locator("xpath=following-sibling::dd[1]").textContent())!);
  await expect.poll(async () => Math.abs((await num("RA")) - want.sky.lon)).toBeLessThan(1e-4);
  expect(Math.abs((await num("Dec")) - want.sky.lat)).toBeLessThan(1e-4);
  await expectNoAxeViolations(page);

  // written as an ordinary file: a new dataset, not marked virtual, opened from the sentence that names it
  await insp.getByRole("button", { name: "Write as a file" }).click();
  const made = insp.getByRole("status").filter({ hasText: "Written as" });
  await expect(made).toContainText("It is an ordinary dataset; a script reads it unmodified.");
  const file = await made.getByRole("button").first().textContent();
  expect(file).toMatch(/^subset-\d+-v1-[0-9a-f]+\.fits$/);
  await expect(insp.getByRole("button", { name: "Use the file already written" })).toBeVisible();
  await made.getByRole("button").first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const written = files.getByRole("button", { name: new RegExp(`^${file!.replace(/\./g, "\\.")}`) });
  await expect(written).toBeVisible();
  await expect(written).not.toContainText("virtual");
  await expectNoAxeViolations(page);
});
