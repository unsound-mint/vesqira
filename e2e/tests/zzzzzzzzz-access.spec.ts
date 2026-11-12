import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Access labels (ADR 0069) in the real app: label a dataset, make a figure and see it carry the label on the board, publishing asks
// for a confirmation that names the dataset, the reviewer's page states the label and holds no dataset name, and the label is set
// back at the end. Runs last (the name): it labels a dataset of the shared instance and restores it.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("a labelled dataset marks its figure, publishing asks for confirmation, and the reviewer is told the label", async ({ page, browser }) => {
  await page.goto("/");
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
  await inspector(page).getByLabel(/Why/).fill("not yet validated");
  await inspector(page).getByRole("button", { name: "Set label" }).click();
  await expect(inspector(page).getByText(/History \(1\)/).or(inspector(page).getByText(/History \(\d+\)/))).toBeVisible();
  await expect(page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: new RegExp(`^${file.replace(".", "\\.")}.*unreleased`) })).toBeVisible();
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
  await expect(page.getByText(/Unreleased, from hi_cube\.fits/).first()).toBeVisible();

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
  await expect(sup.getByRole("note")).toContainText("marked the data behind this board as unreleased");
  expect(await sup.locator("body").innerText()).not.toContain("hi_cube.fits.");
  await expectNoAxeViolations(sup);
  await ctx.close();

  // put the label back so later runs of the suite publish without a question
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  await row.click();
  const again = inspector(page).getByRole("button", { name: /^Access/ });
  if ((await again.getAttribute("aria-expanded")) !== "true") await again.click();
  await inspector(page).getByLabel("Access label").selectOption("none");
  await inspector(page).getByRole("button", { name: "Set label" }).click();
  await expect(inspector(page).getByText("Released: no label.")).toBeVisible();
});
