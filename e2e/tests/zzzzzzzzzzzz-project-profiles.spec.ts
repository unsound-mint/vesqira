import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// A project's own tool profile (ADR 0068, slice 6): the folder has a run of a synthetic HOG-style tool that Vesqira does not ship
// and .vesqira/profiles/hog.json that declares how to read it (made by start-api.sh). The scan lists the run and says whose profile
// reads it; only the person's Import registers anything, and the run page says the profile came from the project folder.
//
// The file name sorts last on purpose: opening a project registers its FITS headers into the catalog the other specs share.
const here = path.dirname(fileURLToPath(import.meta.url));
const FOLDER = path.resolve(here, "../.run/project/own-tool-study");
const out = path.resolve(here, "../test-results");

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")} ${v.nodes[0]?.any[0]?.message}`)).toEqual([]);
}


// shots takes the page in both widths and both colour schemes with an axe scan of each; `prepare` puts the view back after a resize
// (the explorer is closed on a phone and its groups fold again when it is remounted).
async function shots(page: Page, name: string, prepare: () => Promise<void> = async () => {}) {
  for (const [w, h] of [[1440, 900], [390, 844]] as const) {
    await page.setViewportSize({ width: w, height: h });
    await prepare();
    for (const scheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.screenshot({ path: path.join(out, `project-profiles-${name}-${w}-${scheme}.png`) });
      await expectNoAxeViolations(page);
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "light" });
  await prepare();
}

test("a run of a tool only the project profiles is listed, imported by the person, and labelled as the project's declaration", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  const field = page.getByLabel("Folder on this computer");
  await field.fill(FOLDER);
  await field.press("Escape");
  await page.getByRole("button", { name: "Open folder" }).click();
  await expect(page.getByRole("heading", { name: "own-tool-study" }).first()).toBeVisible();

  const side = page.getByRole("complementary", { name: "Explorer" });
  const importButton = side.getByRole("button", { name: /^Import hog_run_demo with the project's profile/ });
  const showExplorer = async () => {
    if (!(await side.isVisible())) await page.getByRole("button", { name: "Toggle explorer" }).click();
    await expect(side).toBeVisible();
    // the profile file is listed as the project's and as not verified
    if (!(await side.getByText(/declared by this file and not verified/).isVisible())) await side.getByText(/1 tool profile from this folder/).click();
    await expect(side.getByText(/HOG-synthetic@1, declared by this file and not verified/)).toBeVisible();
    // the scan found the run and registered nothing from it: the row offers Import, it does not open a run yet
    const runs = side.getByRole("button", { name: /^Runs ·/ });
    if ((await runs.getAttribute("aria-expanded")) !== "true") await runs.click();
    await expect(importButton).toBeVisible();
  };
  await showExplorer();
  await shots(page, "explorer", showExplorer);

  await importButton.click();
  await expect(page.getByRole("heading", { name: /hog_run/ }).first()).toBeVisible();
  const origin = page.getByText(/read with the HOG-synthetic@1 profile from the project folder \(\.vesqira\/profiles\/hog\.json\), declared by that file/);
  await expect(origin).toBeVisible();
  // the tool's own products stay visible under generic roles, and nothing about orientation is claimed
  await expect(page.getByText(/orientation_map/).first()).toBeVisible();
  await shots(page, "run");
});
