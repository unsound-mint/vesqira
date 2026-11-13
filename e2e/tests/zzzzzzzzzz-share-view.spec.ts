import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// "Share this view" (ADR 0071): from the viewer, one click opens the dialog and one makes the link, which is copied. A fresh browser
// context (a supervisor with no session) opens it and sees the frozen picture and its provenance, can reach no data endpoint, and
// the link is dead once the author revokes it from the list of shared figures. Runs after the access spec (the name) so no label is left on the dataset.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");
const inspector = (page: Page) => page.getByRole("complementary", { name: "Inspector" });

async function expectNoAxeViolations(page: Page, skip: string[] = []) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).disableRules(skip).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("share the view from the viewer, open it as a reviewer, then revoke it", async ({ page, browser }) => {
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
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

  // one click opens the dialog; the figure is not saved until it is confirmed
  const fig = page.getByRole("button", { name: "Figure", exact: true });
  if ((await fig.getAttribute("aria-expanded")) !== "true") await fig.click();
  await inspector(page).getByLabel("Title").first().fill("Shared peak");
  await inspector(page).getByRole("button", { name: "Share this view", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Share this view", exact: true });
  await expect(dialog).toBeVisible();
  await expectNoAxeViolations(page);
  for (const scheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectNoAxeViolations(page);
  }
  await page.emulateMedia({ colorScheme: "dark" });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(dialog.getByRole("button", { name: "Create link", exact: true })).toBeInViewport();
  await page.screenshot({ path: test.info().outputPath("share-dialog-390.png") });
  await page.setViewportSize({ width: 1440, height: 900 });

  // the second step: Enter in the label field is the whole confirmation, so the keyboard path is one key
  await dialog.getByLabel("Label (optional)").fill("For the call");
  await dialog.getByLabel("Label (optional)").press("Enter");
  const box = dialog.getByLabel("Review link");
  await expect(box).toBeVisible();
  const link = await box.inputValue();
  expect(link).toContain("/#/review/");
  await expect(dialog).toContainText("copied to the clipboard");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(link);
  await expectNoAxeViolations(page);
  await page.screenshot({ path: test.info().outputPath("share-link-dark.png") });
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await expect(dialog).toBeHidden();

  // a reviewer with no session: the frozen picture and its provenance, and nothing of the data
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const sup = await ctx.newPage();
  await sup.goto(link);
  await expect(sup.getByRole("heading", { name: "Shared peak", level: 1 })).toBeVisible();
  await expect(sup.getByText("For the call", { exact: false }).first()).toBeVisible();
  await expect(sup.getByRole("img", { name: /^Shared peak\./ })).toBeVisible();
  await sup.getByText("Where this came from and what it does not show").click();
  await expect(sup.getByRole("listitem").filter({ hasText: file }).first()).toBeVisible();
  const text = await sup.locator("body").innerText();
  expect(text).not.toContain(data);
  const origin = new URL(link).origin;
  for (const p of ["/api/datasets", "/api/datasets/1/plane?channel=0", "/api/figures", "/api/figure-shares", "/api/boards", "/api/roots"]) {
    expect((await sup.request.get(origin + p)).status(), p).toBe(404);
  }
  await expectNoAxeViolations(sup);
  await sup.emulateMedia({ colorScheme: "light" });
  await expectNoAxeViolations(sup);
  await sup.emulateMedia({ colorScheme: "dark" });
  await sup.screenshot({ path: test.info().outputPath("review-share-dark-390.png"), fullPage: true });

  // the author finds the link among the shared figures and revokes it
  await page.getByRole("button", { name: "Boards", exact: true }).click();
  const shared = page.getByRole("region", { name: "Shared figures" });
  await expect(shared).toContainText("Shared peak");
  await expect(shared).toContainText("Active");
  await expectNoAxeViolations(page, ["heading-order"]);
  await shared.getByRole("button", { name: /^Revoke the link to Shared peak, For the call/ }).first().click();
  await expect(shared.getByText("Revoked").first()).toBeVisible();
  await page.screenshot({ path: test.info().outputPath("shared-figures-dark.png") });

  await sup.reload();
  await expect(sup.getByRole("alert")).toBeVisible();
  await expect(sup.getByRole("heading", { name: "Shared peak" })).toHaveCount(0);
  const token = link.split("/review/")[1]!;
  expect((await sup.request.get(`${origin}/review-api/${token}`)).status()).toBe(404);
  await ctx.close();
});
