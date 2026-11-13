import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Access labels (ADR 0069) in the real app: label a dataset, make a figure and see it carry the label on the board, publishing asks
// for a confirmation that names the dataset, the reviewer's page states the label and is given no list of labelled sources, and the
// label is set back at the end. The file name stays in a figure's provenance, as it always did; the label adds no name. Runs last (the name): it labels a dataset of the shared instance and restores it.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });

// Clears every access label on the shared instance through the API: before the test, so a failed earlier attempt cannot leave a
// second labelled dataset behind, and after it, so later specs publish without a question. Housekeeping, not the flow under test.
async function clearLabels(page: Page) {
  const r = await page.request.get("/api/access");
  expect(r.ok()).toBeTruthy();
  const { datasets } = (await r.json()) as { datasets: Record<string, { own: string }> };
  for (const [id, row] of Object.entries(datasets)) {
    if (row.own === "none") continue;
    const set = await page.request.post(`/api/datasets/${id}/access`, { data: { label: "none", reason: "end-to-end test cleanup" } });
    expect(set.ok()).toBeTruthy();
  }
}

async function expectNoAxeViolations(page: Page, skip: string[] = []) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).disableRules(skip).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("a labelled dataset marks its figure, publishing asks for confirmation, and the reviewer is told the label", async ({ page, browser }) => {
  await page.goto("/");
  await clearLabels(page);
  const file = "hi_cube.fits";
  const row = page.getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}`) }).first();
  if (!(await row.count())) {
    await page.getByLabel("Register a FITS file").fill(path.join(data, file));
    await page.getByRole("button", { name: "Register", exact: true }).click();
    const offer = page.getByRole("status", { name: "A dataset already holds this content" });
    await expect(row.or(offer)).toBeVisible();
    if (await offer.isVisible()) await offer.getByRole("button", { name: "Register as a separate dataset" }).click();
  }
  await row.click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();

  // set the label from the dataset's Access section
  const access = inspector(page).getByRole("button", { name: /^Access/ });
  if ((await access.getAttribute("aria-expanded")) !== "true") await access.click();
  await inspector(page).getByLabel("Access label").selectOption("unreleased");
  await inspector(page).getByLabel(/^Reason/).fill("not yet validated");
  await inspector(page).getByRole("button", { name: "Set label" }).click();
  await expect(inspector(page).getByText(/History \(1\)/).or(inspector(page).getByText(/History \(\d+\)/))).toBeVisible();
  await expect(page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}.*unreleased`) }).first()).toBeVisible();
  await expectNoAxeViolations(page);

  // a figure from it goes on a board and carries the label
  const fig = page.getByRole("button", { name: "Figure", exact: true });
  if ((await fig.getAttribute("aria-expanded")) !== "true") await fig.click();
  await inspector(page).getByLabel("Title").first().fill("Labelled peak");
  await inspector(page).getByRole("button", { name: "Save as figure" }).click();
  await expect(inspector(page).getByText("Saved: “Labelled peak”.")).toBeVisible();
  await inspector(page).getByLabel("New board name").fill("Access board");
  await inspector(page).getByRole("button", { name: "Add to board" }).click();
  await expect(inspector(page).getByText("Added to the board.")).toBeVisible();
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await expect(page.getByText(/Made from hi_cube\.fits/).first()).toBeVisible();

  // publishing is refused until the author confirms, and the sentence names the dataset
  await page.getByRole("button", { name: "Freeze and create link" }).click();
  const ask = page.getByRole("alert", { name: "Labelled data" });
  await expect(ask).toContainText("hi_cube.fits");
  await expect(ask).toContainText("Unreleased");
  await expectNoAxeViolations(page);
  await ask.getByRole("button", { name: /I confirm: publish/ }).click();
  const link = await page.getByLabel("Review link").inputValue();

  // the reviewer is told the label and gets no dataset name
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const sup = await ctx.newPage();
  await sup.goto(link);
  await expect(sup.getByRole("heading", { name: "Access board" })).toBeVisible();
  await expect(sup.getByRole("note")).toContainText("marked the data shown here as unreleased");
  expect(await sup.locator("body").innerText()).not.toMatch(/Made from/); // the owner's "Made from <dataset>" line is not the reviewer's
  await expectNoAxeViolations(sup);
  for (const scheme of ["dark", "light"] as const) {
    await sup.emulateMedia({ colorScheme: scheme });
    await expectNoAxeViolations(sup);
  }
  await sup.emulateMedia({ colorScheme: "dark" });
  await sup.screenshot({ path: test.info().outputPath("review-dark-390.png"), fullPage: true });
  await ctx.close();

  // the owner's board and inspector in the dark scheme and at 390 px
  await page.emulateMedia({ colorScheme: "dark" });
  await expectNoAxeViolations(page);
  await page.screenshot({ path: test.info().outputPath("board-dark.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  await expectNoAxeViolations(page);
  await page.screenshot({ path: test.info().outputPath("board-390.png") });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "light" });

  // put the label back so later runs of the suite publish without a question
  await clearLabels(page);
});
