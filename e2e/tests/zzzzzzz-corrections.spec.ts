import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// WCS and unit corrections (ADR 0061) in the real app: correct a WCS keyword and the unit the file states, see the effect before
// saving (coordinates before and after, what depends on the dataset), save with a reason, see the readout say it is a correction,
// and revert by a new record. Runs last (the name): it adds corrections to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("a correction is previewed, saved with a reason, said in the readout, and reverted by a new record", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Register a FITS file").fill(path.join(data, "hi_cube_freq.fits"));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  const row = page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: /^hi_cube_freq\.fits/ });
  const offer = page.getByRole("status", { name: "A dataset already holds this content" });
  await expect(row.first().or(offer)).toBeVisible();
  if (await offer.isVisible()) await offer.getByRole("button", { name: "Register as a separate dataset" }).click();
  await row.first().click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  const insp = page.getByRole("complementary", { name: "Inspector" });

  const form = insp.getByRole("form", { name: "Correct a value" });
  await form.scrollIntoViewIfNeeded();
  await expect(insp.getByText(/Nothing is corrected/)).toBeVisible();
  await form.getByLabel("Keyword", { exact: true }).selectOption("CRVAL2");
  await form.getByLabel("New CRVAL2").fill("2.05");
  await form.getByRole("button", { name: "Add a keyword" }).click();
  await form.getByLabel("Keyword 2").selectOption("BUNIT");
  await form.getByLabel("New BUNIT").fill("K");
  await form.getByRole("button", { name: "Show the effect" }).click();

  const before = insp.getByRole("region", { name: "Before saving" });
  await expect(before).toContainText("BUNIT Jy/beam → K (the file says Jy/beam)");
  await expect(before).toContainText("CRVAL2 2 → 2.05 (the file says 2)");
  const sky = before.getByRole("table", { name: /Sky position/ });
  await expect(sky.getByRole("row")).toHaveCount(6);
  await expect(sky).toContainText("180.00″");
  await expect(before).toContainText("Read again with the new values");
  await expect(before).toContainText("Keep the values they were made with");
  await expectNoAxeViolations(page);
  await expect(before.getByRole("button", { name: "Save as a new record" })).toBeDisabled();
  await before.getByLabel("Reason (required)").fill("pointing and unit from the observing log");
  await before.getByLabel(/Source \(optional\)/).fill("observatory log 2019-03-02");
  await before.getByRole("button", { name: "Save as a new record" }).click();

  const inForce = insp.getByRole("region", { name: "Corrections in force" });
  await expect(inForce).toContainText("CRVAL2");
  await expect(inForce).toContainText("pointing and unit from the observing log");
  await expect(insp.getByRole("region", { name: "History of corrections" })).toContainText("Corrected");
  // the readout says the unit and the coordinates are the researcher's correction, beside what the file says
  await expect(insp.getByText(/Brightness unit \(K\) is your correction: the file says Jy\/beam/)).toBeVisible();
  await expect(insp.getByText(/Coordinates use your correction of CRVAL2/)).toBeVisible();

  // revert: a new record; the file's own values come back
  const hist = insp.getByRole("region", { name: "History of corrections" });
  await hist.getByRole("button", { name: "Revert this…" }).first().click();
  await expect(before).toContainText("correction withdrawn: back to the file's 2");
  await before.getByLabel("Reason (required)").fill("checked again: the file was right");
  await before.getByRole("button", { name: "Save as a new record" }).click();
  await expect(insp.getByText(/Nothing is corrected/)).toBeVisible();
  await expect(hist).toContainText("Reverted");
  await expect(hist).toContainText("Corrected");
  await expectNoAxeViolations(page);
});
