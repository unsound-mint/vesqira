import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Typed masks (ADR 0055) in the real app: make the no-value mask and a saturation mask of a cube, see them hatched on the plane with
// a legend in words, and apply one of them to statistics with an explicit policy that the result records. Runs last (the name): it
// adds masks and a dataset to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("masks are typed, hatched with a legend, and applied per mask with a policy the result records", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Register a FITS file").fill(path.join(data, "hi_cube.fits"));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  const row = page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube\.fits/ });
  const offer = page.getByRole("status", { name: "A dataset already holds this content" });
  await expect(row.or(offer)).toBeVisible();
  if (await offer.isVisible()) await offer.getByRole("button", { name: "Register as a separate dataset" }).click();
  await row.first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const insp = page.getByRole("complementary", { name: "Inspector" });

  // the pixels with no value: computed from the data, verified, never declared
  await page.getByRole("button", { name: "Masks", exact: true }).click();
  await insp.getByLabel("Make a mask").selectOption("missing");
  await insp.getByRole("button", { name: "Make the mask" }).click();
  const masks = insp.getByRole("list", { name: "Masks", exact: true });
  await expect(masks.getByTestId("mask-row")).toHaveCount(1);
  await expect(masks).toContainText("No value");
  await expect(masks).toContainText("verified");
  await expect(masks).toContainText("576 of 49,152 pixels"); // the 3 x 3 block of NaN in each of 64 channels

  // it is shown at once: a hatch over the plane, and a legend that names it
  const legend = page.getByTestId("mask-legend");
  await expect(legend).toContainText("No value: No value (NaN, BLANK)");
  await expect(page.getByTestId("mask-overlay").locator("path[data-mask-type='missing']")).toHaveCount(1);

  // a saturation mask needs its threshold and where it comes from
  await insp.getByLabel("Make a mask").selectOption("saturated");
  await insp.getByLabel(/Threshold \(flag where value/).fill("0.5");
  await insp.getByRole("button", { name: "Make the mask" }).click();
  await expect(insp.getByText("Say where the threshold comes from.")).toBeVisible();
  await insp.getByLabel("Where the threshold comes from").fill("a test value");
  await insp.getByRole("button", { name: "Make the mask" }).click();
  await expect(masks.getByTestId("mask-row")).toHaveCount(2);
  await expect(legend).toContainText("Saturated: Saturated ≥ 0.5");
  await expectNoAxeViolations(page);

  // statistics: nothing is applied until a policy is chosen per mask; the result records it
  await page.getByRole("button", { name: "Analysis", exact: true }).click();
  await insp.getByLabel("Analysis", { exact: true }).selectOption("stats");
  await insp.getByRole("button", { name: "Compute statistics" }).click();
  await expect(insp.getByText("576", { exact: true })).toBeVisible();
  await expect(insp.getByTestId("stats-masks")).toHaveCount(0);
  await insp.getByLabel("Saturated: Saturated ≥ 0.5").selectOption("exclude");
  await insp.getByRole("button", { name: "Compute statistics" }).click();
  const applied = insp.getByTestId("stats-masks");
  await expect(applied).toContainText("Saturated mask “Saturated ≥ 0.5”: policy exclude");
  await expect(applied).toContainText("declared");
  await expect(insp.getByText(/Pixels used/)).toBeVisible();
  await expect(insp.getByText(/never treated as masked/)).toBeVisible();
  await expectNoAxeViolations(page);
});
