import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import path from "node:path";
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

test("a subset is defined, marked virtual, read through its window and written as an ordinary file", async ({ page }) => {
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

  // the subset is open: a window, not a file, with what it resolved to in the source's own pixels
  const stage = page.getByRole("heading", { name: "Core, every second pixel" });
  await expect(stage).toBeVisible();
  await expect(page.getByRole("main", { name: "Viewer" }).getByText("virtual subset", { exact: true })).toBeVisible();
  await expect(page.getByText(/It is not a file: nothing is copied until you write it as one/)).toBeVisible();
  const resolved = page.getByRole("region", { name: "What the window resolved to" });
  await expect(resolved).toContainText("9 by 7 pixels, 31 channels");
  await expect(resolved).toContainText("x 4 to 20, y 3 to 15, every 2 of 32 by 24");
  await expect(resolved).toContainText("11 to 41 of 64");

  // listed under its source, marked
  const sub = files.getByRole("button", { name: /Core, every second pixel/ });
  await expect(sub).toContainText("virtual subset");
  await expect(sub).toHaveAttribute("aria-current", "true");

  // a readout through the window: pixel 1 of the subset is source pixel 4 (the box starts there)
  await page.getByRole("button", { name: "Read coordinates" }).click();
  await expect(page.getByRole("status").filter({ hasText: "of the subset" })).toContainText("on the sky");
  await expectNoAxeViolations(page);

  // written as an ordinary file: a new dataset, not marked virtual, opened from the sentence that names it
  await page.getByRole("button", { name: "Write as a file" }).click();
  const made = page.getByRole("status").filter({ hasText: "Written as" });
  await expect(made).toContainText("It is an ordinary dataset; a script reads it unmodified.");
  const file = await made.getByRole("button").first().textContent();
  expect(file).toMatch(/^subset-\d+-v1-[0-9a-f]+\.fits$/);
  await expect(page.getByRole("button", { name: "Use the file already written" })).toBeVisible();
  await made.getByRole("button").first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const written = files.getByRole("button", { name: new RegExp(`^${file!.replace(/\./g, "\\.")}`) });
  await expect(written).toBeVisible();
  await expect(written).not.toContainText("virtual");
  await expectNoAxeViolations(page);
});
