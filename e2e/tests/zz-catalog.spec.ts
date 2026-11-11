import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Point catalogs (ADR 0043): register a table of sources, read its rows, declare which columns are the sky position and
// what the angle is, and read how the declaration treats every row. The table is a small starlight-polarization file in the
// shape of the real ones (tab separated, a commented header, h m s and d m s positions, trailing empty fields) with one
// position that cannot be read. Runs after the other specs (the name) because it adds a dataset to the shared instance.

const here = path.dirname(fileURLToPath(import.meta.url));
const data = path.resolve(here, "../.run/data");

const TABLE =
  "#Star\tRA(J2000)\tDEC(J2000)\tP(%)\tsP(%)\tPA(deg)\tsPA(deg)\n" +
  "67-2\t4 47 07.65\t-67 06 23.31\t1.17\t0.08\t25\t4\t\t\t\n" +
  "69-7\t4 49 15.83\t-69 27 27.92\t1.30\t0.09\t38\t7\n" +
  "67-5\t25:61:00\t-67 39 29.93\t0.79\t0.16\t61\t6\t\n" +
  "69-16\t4 52 03.57\t-69 26 33.76\t1.12\t0.08\t41\t\n";

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

test("register a catalog, declare its columns from a labelled proposal, and read what happened to each row", async ({ page }) => {
  writeFileSync(path.join(data, "mf_stars_LMC.dat"), TABLE);
  await page.goto("/");
  await page.getByLabel("Register a FITS file or a catalog").fill(path.join(data, "mf_stars_LMC.dat"));
  await page.getByRole("button", { name: "Register", exact: true }).click();
  await page.getByRole("button", { name: /^mf_stars_LMC\.dat/ }).click();

  // the rows, as the file says them: the line in the file, a dash for the empty cell, the 25:61:00 untouched
  const table = page.getByRole("table").first();
  await expect(table.getByText("25:61:00")).toBeVisible();
  await expect(table.getByRole("rowheader", { name: "4" })).toBeVisible(); // the third row is on line 4
  await expect(table.getByTitle("missing value")).toHaveCount(1);

  // nothing is declared, so nothing is placed on the sky; the proposal is labelled and is not applied
  const inspector = page.getByRole("complementary", { name: "Inspector" });
  await expect(inspector.getByText("Nothing is declared yet")).toBeVisible();
  await expect(inspector.getByText("proposed from the column names")).toBeVisible();
  await expect(inspector.getByLabel("Celestial frame")).toHaveValue("");
  await expectNoAxeViolations(page);

  // use the proposal, say what the angle is, declare
  await inspector.getByRole("button", { name: "Fill the form from this proposal" }).click();
  await expect(inspector.getByLabel("Celestial frame")).toHaveValue("fk5");
  await expect(inspector.getByLabel("Equinox")).toHaveValue("J2000");
  await inspector.getByLabel("What the angle is").selectOption("starlight_polarization");
  await inspector.getByRole("button", { name: "Declare", exact: true }).click();

  // the unreadable position is counted and found by its line; the empty angle is missing, not zero
  const report = inspector.getByRole("status", { name: "How the declaration reads the rows" });
  await expect(report.getByText("3 of 4 rows have a position.")).toBeVisible();
  await expect(report.getByText("1 row could not be read:")).toBeVisible();
  await expect(report.getByText(/line 4 RA\(J2000\) '25:61:00' is not a valid angle/)).toBeVisible();
  await expect(report.getByText(/Angle column PA\(deg\): 0 missing/).or(report.getByText(/not a check that the frame/))).toBeVisible();
  await expect(table.getByRole("columnheader", { name: /RA\(J2000\).*longitude/ })).toBeVisible();
  await expectNoAxeViolations(page);

  // withdrawing returns to "nothing declared"
  await inspector.getByRole("button", { name: "Withdraw" }).click();
  await expect(inspector.getByText("Nothing is declared yet")).toBeVisible();
});
