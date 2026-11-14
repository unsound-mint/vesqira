import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Projects (ADR 0030): open a folder, see what was found, write the layout only on confirmation, answer what cannot be
// known, and open a file from the explorer. The folder is made by start-api.sh outside the readable roots, so opening it
// is what grants access.
//
// The file name sorts last on purpose: opening a project registers its FITS headers into the catalog the other specs share.
const here = path.dirname(fileURLToPath(import.meta.url));
const FOLDER = path.resolve(here, "../.run/project/hi-vgt-study");
const PROJECT_FILE = path.join(FOLDER, ".vesqira", "project.toml");

const toAnswer = async (page: Page) => {
  const pill = page.getByRole("button", { name: /to answer/ });
  return (await pill.count()) ? Number((await pill.textContent())?.match(/\d+/)?.[0]) : 0;
};

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("open a folder, confirm the layout, answer what is unknown, open a file", async ({ page }) => {
  await page.goto("/");

  // nothing open: the way in is opening a folder, and the viewer's own panels do not pretend there is a dataset
  await expect(page.getByRole("heading", { name: "Open a project" })).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Inspector" })).toBeHidden();
  await expectNoAxeViolations(page);

  const field = page.getByLabel("Folder on this computer");
  await field.fill(FOLDER);
  await field.press("Escape"); // the completion list would otherwise sit over the button
  await page.getByRole("button", { name: "Open folder" }).click();

  // first look: what was found, with where each number comes from, and a proposed project file that is not written yet
  await expect(page.getByRole("heading", { name: "hi-vgt-study" }).first()).toBeVisible();
  await expect(page.getByText("Proposed .vesqira/project.toml")).toBeVisible();
  await expect(page.getByText(/from: folders with a run record/)).toBeVisible();
  expect(fs.existsSync(PROJECT_FILE)).toBe(false);
  const gitBefore = fs.readdirSync(path.join(FOLDER, ".git")).sort();
  await expectNoAxeViolations(page);

  // a layout is written to the folder only when the person presses the button, and never committed
  await page.getByRole("button", { name: "Write it to the project" }).click();
  await expect.poll(() => fs.existsSync(PROJECT_FILE)).toBe(true);
  expect(fs.readFileSync(PROJECT_FILE, "utf8")).toContain('name = "hi-vgt-study"');
  expect(fs.readdirSync(path.join(FOLDER, ".git")).sort()).toEqual(gitBefore); // nothing touched version control

  // needs your input: the pill opens it with the keyboard; an answer is stored as declared, and "leave unknown" is an answer
  const pill = page.getByRole("button", { name: /to answer/ });
  await expect(pill).toBeVisible();
  const before = await toAnswer(page);
  expect(before).toBeGreaterThan(0);
  await pill.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Needs your input" })).toBeVisible();
  await expect(page.getByText(/Vesqira does not guess these/)).toBeVisible();
  for (const scheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectNoAxeViolations(page);
  }
  const first = page.getByRole("list", { name: "Questions" }).getByRole("listitem").first();
  const unitField = first.getByLabel("Declare a unit");
  if (await unitField.count()) {
    await unitField.fill("Jy/beam");
    await unitField.press("Enter");
  } else {
    await first.getByRole("button", { name: "Leave unknown" }).click();
  }
  await expect(page.getByText(/^Saved\. [A-Z]/)).toBeVisible(); // a sentence, not a fragment
  await expect.poll(() => toAnswer(page)).toBe(before - 1);

  // health
  await page.getByRole("button", { name: /^health/ }).click();
  await expectNoAxeViolations(page);

  // the explorer lists what the scan found; a file opens in the viewer with its own header facts
  const side = page.getByRole("complementary", { name: "Explorer" });
  const data = side.getByRole("button", { name: /^Data ·/ });
  if ((await data.getAttribute("aria-expanded")) !== "true") await data.click();
  await side.getByRole("list", { name: "Data" }).getByRole("button", { name: /^data/ }).click();
  await side.getByRole("button", { name: /^raw/ }).click();
  await side.getByRole("button", { name: /hi_cube\.fits/ }).click();
  await expect(page.getByRole("slider", { name: "Channel" })).toBeVisible();
  await expect(page.getByText("/ 64")).toBeVisible(); // the cube has 64 channels
  await expect(page.getByText(/Loading channel/)).toBeHidden(); // and the plane arrived
  // the unit declared on the Needs page reached the files it was about, as yours and never as the file's
  await side.getByRole("button", { name: /hi_cube_nounit\.fits/ }).click();
  const inspector = page.getByRole("complementary", { name: "Inspector" });
  await expect(inspector.getByText("Jy/beam (declared by you, not in the file)")).toBeVisible();
  const unitSection = inspector.locator("section").filter({ has: page.getByRole("heading", { name: "Unit of the values" }) });
  await expect(unitSection.getByText(/declared by you/)).toBeVisible();
  await expectNoAxeViolations(page);
  // withdrawing it returns to what the file said, and it can be declared again from the dataset itself
  await unitSection.getByRole("button", { name: "Withdraw the unit" }).click();
  await expect(inspector.getByText("— (unresolved)")).toBeVisible();
  await expect(page.getByText(/Brightness unit unresolved: the header has no BUNIT/)).toBeVisible(); // the viewer followed, no reload
  await unitSection.getByLabel("Declare the unit").fill("mJy/beam");
  await unitSection.getByRole("button", { name: "Declare" }).click();
  await expect(inspector.getByText("mJy/beam (declared by you, not in the file)")).toBeVisible();
  await expect(page.getByText(/Brightness unit \(mJy\/beam\) declared by you: the header has no BUNIT/)).toBeVisible();
  // a file that states its own unit offers no declaration at all
  await side.getByRole("button", { name: /hi_cube\.fits/ }).click();
  await expect(inspector.getByRole("heading", { name: "Unit of the values" })).toHaveCount(0);
  // the rows fit the panel: no horizontal scrollbar, the size column is not cut off
  const overflow = await side.locator(":scope > div").first().evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});

test("on a phone the explorer does not cover the page that was asked for", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "Open project…" }).first().click();
  await expect(page.getByRole("heading", { name: "Open a project" })).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Explorer" })).toBeHidden();
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
  expect(wide).toBeLessThanOrEqual(0);
  // the title bar's own items fit too: the pills keep their number, their accessible name stays whole
  const header = page.getByRole("banner");
  expect(await header.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0);
  const pill = page.getByRole("button", { name: /to answer/ });
  if (await pill.count()) await expect(pill).toHaveAccessibleName(/^\d+ to answer$/);
});
