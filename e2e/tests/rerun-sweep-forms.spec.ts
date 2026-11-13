import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// The "run again with..." form and the sweep form are drawn from the tool profile's template (ADR 0068): their fields, ranges and
// documented defaults (with the reason) come from GET /api/job-templates, a value outside a declared range is refused with the
// server's sentence, and both approval views say which code commit the script insists on and why. A VGT job is made, finished on the
// stand-in Slurm and imported as a run, as in the walk-through; then both forms are used on it. Screenshots go to e2e/test-results.

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

/** Screenshots at the two widths in both colour schemes, each scanned with axe. */
async function shoot(page: Page, name: string) {
  for (const [w, h] of [[1440, 900], [390, 844]] as const) {
    await page.setViewportSize({ width: w, height: h });
    for (const scheme of ["dark", "light"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.screenshot({ path: path.join(out, `forms-${name}-${w}-${scheme}.png`), fullPage: true });
      const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
      expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "dark" });
}

/** The labels of the controls reached by pressing Tab from `first`, in order. */
async function tabbedLabels(page: Page, first: Locator, count: number): Promise<string[]> {
  await first.focus();
  const seen: string[] = [];
  for (let i = 0; i < count; i++) {
    seen.push(await page.evaluate(() => {
      const e = document.activeElement as HTMLInputElement | null;
      return (e?.labels?.[0]?.textContent ?? e?.getAttribute("aria-label") ?? e?.textContent ?? "").trim();
    }));
    await page.keyboard.press("Tab");
  }
  return seen;
}

test("rerun and sweep forms come from the template, refuse a value outside its range, and say which commit the script insists on", async ({ page }) => {
  const tpl = (await (await page.request.get(`${API}/api/job-templates`)).json()).templates.find((t: { id: string }) => t.id === "vgt");
  const editable = tpl.keys.filter((k: { internal?: boolean }) => !k.internal) as { name: string; label: string; kind: string }[];
  const profileCommit = tpl.repo.commit as string;

  // a finished, imported VGT run to repeat
  await page.goto("/");
  await page.getByRole("button", { name: /^Hosts/ }).click();
  await page.getByLabel("Host name").fill("Forms host");
  await page.getByLabel("SSH destination").fill("login1");
  await chooseHostFolder(page, remoteRoot);
  await page.getByRole("button", { name: "Add host" }).click();
  await expect(page.getByRole("region", { name: "Host Forms host" })).toBeVisible();
  await page.getByRole("button", { name: "Jobs", exact: true }).click();
  await page.getByLabel("Job name").fill("forms-base");
  await page.getByLabel("VGT folder on the host").fill("/shared/home/u/vgt");
  await page.getByLabel("Base config on the host").fill(path.join(remoteRoot, "configs", "vgt.toml"));
  await page.getByRole("button", { name: "Load settings" }).click();
  await expect(page.getByLabel("ker (smoothing, pixels)")).toHaveValue("4");
  await page.getByRole("button", { name: "Create draft and show the script" }).click();
  const script = page.getByRole("textbox", { name: /Script/ });
  const text = await script.inputValue();
  const outDir = /output_dir = "([^"]+)"/.exec(text)![1]!;
  await page.getByRole("button", { name: "Submit this script" }).click();
  const slurmId = /Slurm job (\d+)/.exec(await page.locator("body").innerText())?.[1] ?? (await page.getByText(/Slurm job \d+/).first().innerText()).replace(/\D/g, "");
  execFileSync("python3", ["-c", "import sys, pathlib, generate; generate.write_vgt_run(pathlib.Path(sys.argv[1]), metrics=True)", outDir], { cwd: path.resolve(here, "../../fixtures") });
  writeFileSync(path.resolve(here, `../.run/slurm-state/${slurmId}`), "COMPLETED\n");
  await page.getByRole("button", { name: "Refresh status" }).click();
  await expect(page.locator("body")).toContainText("exit 0:0");
  await page.getByRole("button", { name: "Import results as a run" }).click();
  await expect(page.getByText("Results imported.")).toBeVisible();

  // the rerun form, from the imported run
  await page.getByRole("button", { name: "Open the run" }).click();
  await page.getByRole("button", { name: "Run again with…" }).first().click();
  const form = page.getByRole("form", { name: "New run draft" });
  await expect(form).toBeVisible();
  const dn = form.getByLabel("dn (window, pixels)");
  await expect(dn).toHaveValue("20"); // the parent's value, as the config had it
  await expect(dn).toHaveAccessibleDescription(/documented default is 20, the value in VGT\.jl's config\.example\.toml/);
  await expect(form.getByLabel("Output folder")).toHaveCount(0); // Vesqira's own per job
  // one field per editable key, in the template's order, reached in that order by the keyboard
  const labels = editable.map((k) => k.label);
  expect(await tabbedLabels(page, form.getByLabel(labels[0]!), labels.length)).toEqual(labels);
  await shoot(page, "rerun-form");
  await form.getByLabel("output binning (pixels, 1 or more)").fill("0");
  await form.getByRole("button", { name: "Prepare the job" }).click();
  await expect(page.getByRole("alert").filter({ hasText: /output_bin must be a whole number, 1 or more/ })).toBeVisible();
  console.log("rerun refusal:", await page.getByRole("alert").filter({ hasText: /output_bin must be a whole number, 1 or more/ }).innerText());
  await form.getByLabel("output binning (pixels, 1 or more)").fill("");
  await form.getByRole("checkbox", { name: /Stop the job unless the folder is at commit/ }).check();
  await form.getByRole("button", { name: "Prepare the job" }).click();

  // the draft is opened on the Jobs screen with its check
  const check = page.getByRole("region", { name: "Rerun check" });
  await expect(check).toBeVisible();
  await expect(check.getByText("Code commit.")).toBeVisible();
  await expect(check).toContainText(new RegExp(`insists on commit ${profileCommit}.*was written against`));
  await shoot(page, "rerun-check");

  // back to the base job for the sweep
  await page.getByRole("button", { name: /^forms-base\s+completed/ }).click();
  await page.getByRole("button", { name: "Sweep from this job…" }).click();
  const setting = page.getByLabel("Setting 1");
  await expect(setting).toHaveValue("dn"); // the template's sweepKey
  const options = await setting.locator("option").evaluateAll((o) => o.map((x) => (x as HTMLOptionElement).value));
  expect(options).toEqual(editable.filter((k) => k.kind !== "path").map((k) => k.name));
  await expect(setting).toHaveAccessibleDescription(/documented default is 20, the value in VGT\.jl's config\.example\.toml/);
  await expect(page.getByRole("combobox", { name: "Code commit" })).toHaveValue("base");
  // the keyboard reaches the form's controls in the order they are read
  expect((await tabbedLabels(page, page.getByLabel(/^Base job/), 6)).map((l) => l.split(" ")[0])).toEqual(["Base", "Code", "Sweep", "Setting", "Label", "Values"]);
  await shoot(page, "sweep-form");
  // a binning of 0 is outside "1 or more": the server's sentence, and nothing is counted
  await setting.selectOption("output_bin");
  await page.getByLabel("Values").fill("0, 2");
  await page.getByRole("button", { name: "Count the jobs" }).click();
  await expect(page.getByRole("alert")).toContainText(/output_bin must be a whole number, 1 or more/);
  await expect(page.getByRole("button", { name: /^Create (\d+ )?drafts/ })).toBeDisabled();
  console.log("sweep refusal:", await page.getByRole("alert").innerText());
  await setting.selectOption("dn");
  await page.getByLabel("Values").fill("8, 16");
  await page.getByRole("combobox", { name: "Code commit" }).selectOption("on");
  await page.getByRole("button", { name: "Count the jobs" }).click();
  await expect(page.getByText(/2 jobs/).first()).toBeVisible();
  await page.getByRole("button", { name: /Create 2 drafts/ }).click();
  await page.getByRole("button", { name: "Submit sweep…" }).click();
  const dialog = page.getByRole("dialog", { name: "Submit sweep" });
  await expect(dialog.getByText("Code commit.")).toBeVisible();
  await expect(dialog).toContainText(new RegExp(`insists on commit ${profileCommit}.*was written against`));
  await shoot(page, "sweep-review");
  await page.keyboard.press("Escape");

});
