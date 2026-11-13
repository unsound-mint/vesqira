import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

// The job form is drawn from the tool profile's template (ADR 0068, slice 5): the labels, ranges, defaults and descriptions come
// from GET /api/job-templates. This spec looks at the form for the two shipped tools at a wide and a phone width in both colour
// schemes, scans it with axe, and checks what the template puts on the screen. The screenshots are written to e2e/test-results.

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../test-results");
const remoteRoot = path.resolve(here, "../.run/remote_data");

async function chooseHostFolder(page: Page, target: string) {
  const home = path.resolve(here, "../.run/remote-home");
  await page.getByRole("button", { name: /Choose folder|Add another folder/ }).click();
  const list = page.getByRole("list", { name: /^Folders on / });
  await expect(list).toBeVisible();
  for (const step of path.relative(home, target).split(path.sep)) {
    await list.getByRole("button", { name: step === ".." ? ".." : new RegExp(`^${step}$`) }).click();
    await expect(page.getByRole("button", { name: "Use this folder" })).toBeEnabled();
  }
  await page.getByRole("button", { name: "Use this folder" }).click();
}

async function shoot(page: Page, name: string) {
  for (const [w, h] of [[1440, 900], [390, 844]] as const) {
    await page.setViewportSize({ width: w, height: h });
    for (const scheme of ["dark", "light"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.screenshot({ path: path.join(out, `jobform-${name}-${w}-${scheme}.png`), fullPage: true });
      const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
      expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
    }
  }
}

test("the job form shows each tool's declared keys, ranges, descriptions, pinned commit and preparation", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: /^Hosts/ }).click();
  await page.getByLabel("Host name").fill("Form host");
  await page.getByLabel("SSH destination").fill("login1");
  await chooseHostFolder(page, remoteRoot);
  await page.getByRole("button", { name: "Add host" }).click();
  await expect(page.getByRole("region", { name: "Host Form host" })).toBeVisible();

  await page.getByRole("button", { name: "Jobs", exact: true }).click();
  await expect(page.getByRole("heading", { name: "New VGT job" })).toBeVisible();
  await page.getByLabel("VGT folder on the host").fill("/shared/home/u/vgt");
  await page.getByLabel("Base config on the host").fill(path.join(remoteRoot, "configs", "vgt.toml"));
  await page.getByRole("button", { name: "Load settings" }).click();
  await expect(page.getByLabel("ker (smoothing, pixels)")).toHaveValue("4");
  // the description and the tool's documented default come from the profile, and are read with the field
  await expect(page.getByLabel("ker (smoothing, pixels)")).toHaveAccessibleDescription(/Gaussian smoothing sigma.*documented default is 4/);
  await expect(page.getByLabel("noise mode")).toHaveAccessibleDescription(/How the noise threshold is modelled/);
  await expect(page.getByLabel("Output folder")).toHaveCount(0); // Vesqira's own per job
  await expect(page.getByRole("checkbox", { name: /Stop the job unless the folder is at commit/ })).not.toBeChecked();
  await page.getByText("What the script does before and when it runs").click();
  await expect(page.getByText(/Stops with a plain message if Julia \(julia\) is not available/)).toBeVisible();
  await shoot(page, "vgt");

  await page.getByLabel("Kind").selectOption("rohsa");
  await expect(page.getByRole("heading", { name: "New ROHSA job" })).toBeVisible();
  await page.getByLabel("ROHSA-LMC folder on the host").fill("/shared/home/u/ROHSA-LMC");
  await expect(page.getByText(/Runs the ROHSA build-and-decompose script/)).toBeVisible();
  // the preparation list stays open from the VGT form: the same component, another template
  await expect(page.getByText(/Builds ROHSA in rohsa\/upstream\/ROHSA\/src/)).toBeVisible();
  await shoot(page, "rohsa");
});
