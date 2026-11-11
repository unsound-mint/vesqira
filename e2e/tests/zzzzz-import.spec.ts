import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Generic import (ADR 0058): scan a folder, read what the built-in rules recognise and why, write a rule with a live preview of
// what it matches, save it, register what it selects, and write it to project.toml only on a second confirmation.
//
// The folder is made here, outside the readable roots, so opening it is what grants access. The file name sorts last because
// opening a project registers its FITS headers into the catalog the other specs share.
const here = path.dirname(fileURLToPath(import.meta.url));
const FOLDER = path.resolve(here, "../.run/import-e2e");
const PROJECT_FILE = path.join(FOLDER, ".vesqira", "project.toml");

/** A valid 4x4 float32 FITS image; equal seeds are equal bytes. */
function tinyFits(seed: number): Buffer {
  const card = (s: string) => s.padEnd(80, " ");
  let h = [card("SIMPLE  =                    T"), card("BITPIX  =                  -32"), card("NAXIS   =                    2"), card("NAXIS1  =                    4"), card("NAXIS2  =                    4"), card("END")].join("");
  h = h.padEnd(2880, " ");
  const data = Buffer.alloc(2880);
  for (let i = 0; i < 16; i++) data.writeFloatBE(seed + i, i * 4);
  return Buffer.concat([Buffer.from(h, "latin1"), data]);
}

function put(rel: string, content: Buffer | string) {
  const p = path.join(FOLDER, rel);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, content);
}

/** A tall viewport shot of the page (the page scrolls inside the canvas), for reading the rendered screen; kept under .run. */
async function shot(page: Page, name: string, width: number, height: number) {
  const size = page.viewportSize() ?? { width: 1440, height: 900 };
  await page.setViewportSize({ width, height });
  fs.mkdirSync(path.resolve(here, "../.run/shots"), { recursive: true });
  await page.screenshot({ path: path.resolve(here, `../.run/shots/${name}.png`) });
  await page.setViewportSize(size);
}

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("scan a folder, write a rule, see what it matches, register, and write it to the project only when confirmed", async ({ page }) => {
  fs.rmSync(FOLDER, { recursive: true, force: true });
  put("vda/n3/vgt/psi_1.fits", tinyFits(1));
  put("vda/n3/vgt/psi_2.fits", tinyFits(100));
  put("maps/mu_kms.fits", tinyFits(200));
  put("README.md", "notes");
  put(".vesqira/project.toml", '# mine\n[project]\nname = "import-e2e"\n');
  const theirs = fs.readFileSync(PROJECT_FILE, "utf8");

  await page.goto("/");
  const field = page.getByLabel("Folder on this computer");
  await field.fill(FOLDER);
  await field.press("Escape");
  await page.getByRole("button", { name: "Open folder" }).click();
  await expect(page.getByRole("heading", { name: "import-e2e" }).first()).toBeVisible();

  // the scan preview: what the built-in rules recognise and what they do not, each with its reason
  await page.getByRole("button", { name: "Import rules…" }).click();
  await expect(page.getByRole("heading", { name: "Import rules" })).toBeVisible();
  const groups = page.getByRole("list", { name: "Files by role and extension" });
  await expect(groups.getByText("Dataset (FITS)")).toBeVisible();
  await expect(groups.getByText("Not recognised")).toBeVisible();
  await expect(groups.getByText("FITS file: a self-describing format, registered as a dataset")).toBeVisible();
  await expectNoAxeViolations(page);
  await shot(page, "import-scan-1440-dark", 1440, 1500);

  // a rule: nothing is preselected, and the preview follows the edit before anything is saved
  await page.getByRole("button", { name: "Add a rule" }).click();
  const card = page.getByRole("listitem", { name: /^Rule 1/ });
  await card.getByLabel("Name").fill("psi maps");
  await card.getByLabel("Pattern on the path from the project folder").fill("vda/*/vgt/psi_*.fits");
  await card.getByLabel("Role").selectOption("orientation");
  await expect(card.getByLabel("Angle unit")).toHaveValue("");
  await expect(card.getByText(/does not guess degrees or radians/)).toBeVisible();
  await card.getByLabel("Angle unit").selectOption("rad");
  await card.getByLabel("Angle frame").selectOption("pixel");
  await card.getByLabel("What the angle describes").selectOption("b_field");
  await expect(card.getByRole("status")).toContainText("2 files match");
  await expect(page.getByText(/matched against your unsaved edits/)).toBeVisible();
  expect(fs.readFileSync(PROJECT_FILE, "utf8")).toBe(theirs);
  await expect(page.getByRole("button", { name: /^Register/ })).toBeDisabled(); // unsaved: nothing is registered from a draft
  for (const scheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectNoAxeViolations(page);
    await shot(page, `import-rule-1440-${scheme}`, 1440, 2000);
  }
  await shot(page, "import-rule-390-light", 390, 2600);
  await page.emulateMedia({ colorScheme: "dark" });
  await shot(page, "import-rule-390-dark", 390, 2600);

  // the error state: a rule the server refuses is listed with every reason, and keeps the edit
  await card.getByLabel("Angle unit").selectOption("");
  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByRole("list", { name: "Problems" })).toContainText("angle unit is required");
  await expectNoAxeViolations(page);
  await card.getByLabel("Angle unit").selectOption("rad");

  await page.getByRole("button", { name: "Save rules" }).click();
  await expect(page.getByText(/Saved 1 rule in the catalog. project.toml is unchanged./)).toBeVisible();
  expect(fs.readFileSync(PROJECT_FILE, "utf8")).toBe(theirs);

  // register: declared by the rule, and the orientation stays unconfirmed until the researcher confirms it
  const register = page.getByRole("button", { name: /^Register 2 files/ });
  await expect(register).toBeEnabled();
  await register.click();
  const summary = page.getByRole("status", { name: "Import summary" });
  const counts = /Registered (\d+) · declared on (\d+)/.exec((await summary.textContent()) ?? "");
  expect(counts, "the summary says what was registered and declared").not.toBeNull();
  expect(Number(counts![1]) + Number(counts![2])).toBe(2); // the open-scan may or may not have registered them first
  await expect(summary).toContainText("0 failed");
  await shot(page, "import-applied-1440-dark", 1440, 2200);
  await page.getByRole("button", { name: /to answer/ }).click();
  await expect(page.getByText(/your import rule psi maps: declared by the rule, not by you/).first()).toBeVisible();

  // the rules reach project.toml only after a second click, in a marked section, and nothing else in the file moves
  await page.getByRole("button", { name: "Import rules…" }).click();
  await page.getByRole("button", { name: "Write the rules to project.toml…" }).click();
  expect(fs.readFileSync(PROJECT_FILE, "utf8")).toBe(theirs);
  await page.getByRole("button", { name: "Write the rules" }).click();
  await expect(page.getByText(/Nothing was committed./)).toBeVisible();
  const written = fs.readFileSync(PROJECT_FILE, "utf8");
  expect(written.startsWith(theirs)).toBe(true);
  expect(written).toContain("[[import.rules]]");
  expect(written).toContain('name = "psi maps"');
  expect(written).toContain('angle_unit = "rad"');
});
