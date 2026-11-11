import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// The transfer queue (ADR 0051) through the real API and the real rsync, with the stand-in ssh that runs the "host" on this
// machine: download a file, see it verified by hash, meet a conflict, keep both, and check nothing was overwritten.

const here = path.dirname(fileURLToPath(import.meta.url));
const remoteRoot = path.resolve(here, "../.run/remote_data");
const incoming = path.resolve(here, "../.run/data/incoming-transfers");

const sha = (p: string) => createHash("sha256").update(fs.readFileSync(p)).digest("hex");

function haveRsync(): boolean {
  try {
    execFileSync("rsync", ["--version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function newTransfer(page: Page, source: string, policy?: string) {
  const panel = page.getByRole("region", { name: "Transfers" });
  await panel.getByRole("button", { name: "New transfer…" }).click();
  await panel.getByRole("combobox", { name: "File or folder on the host" }).fill(source);
  await panel.getByRole("combobox", { name: "Into this folder on this computer" }).fill(incoming);
  if (policy) await panel.getByLabel("If the name is already there").selectOption(policy);
  await panel.getByRole("button", { name: "Queue transfer" }).click();
}

test("a file is downloaded, verified by hash on both ends, and never overwritten", async ({ page }) => {
  test.skip(!haveRsync(), "rsync is not installed");
  fs.mkdirSync(incoming, { recursive: true });
  const data = randomBytes(3 << 20);
  const remoteFile = path.join(remoteRoot, "transfer_cube.fits");
  fs.writeFileSync(remoteFile, data);
  const created = await page.request.post(`${API}/api/hosts`, { data: { name: "Transfer host", dest: "fake", roots: [remoteRoot] } });
  expect(created.ok()).toBeTruthy();

  await page.goto("/");
  await page.getByRole("button", { name: /^Hosts/ }).click();
  const panel = page.getByRole("region", { name: "Transfers" });
  await expect(panel).toBeVisible();

  // 1. an ordinary download: verified by hash, and the same bytes on disk
  await newTransfer(page, remoteFile);
  const first = panel.getByRole("listitem", { name: "Download transfer_cube.fits" });
  await expect(first.getByText("Done, verified")).toBeVisible({ timeout: 60_000 });
  const copy = path.join(incoming, "transfer_cube.fits");
  expect(sha(copy)).toBe(sha(remoteFile));
  await first.getByText("Paths and SHA-256").click();
  await expect(first.getByText(sha(copy))).toBeVisible();
  expect(fs.readdirSync(incoming)).toEqual(["transfer_cube.fits"]); // no partial folder left behind

  // 2. the same file again: it is already there, so nothing is copied and nothing is duplicated
  await newTransfer(page, remoteFile);
  await expect(panel.getByText(/already on this computer; nothing was transferred/)).toBeVisible({ timeout: 60_000 });
  expect(fs.readdirSync(incoming)).toEqual(["transfer_cube.fits"]);

  // 3. a different file with the same name waits for a decision and is left untouched until then
  fs.writeFileSync(remoteFile, randomBytes(2 << 20));
  await newTransfer(page, remoteFile);
  await expect(panel.getByText("Needs your decision")).toBeVisible({ timeout: 60_000 });
  expect(sha(copy)).toBe(createHash("sha256").update(data).digest("hex"));
  await panel.getByRole("button", { name: "Keep both (new name)" }).click();
  await expect(panel.getByRole("listitem", { name: "Download transfer_cube (2).fits" }).getByText("Done, verified")).toBeVisible({ timeout: 60_000 });
  expect(sha(path.join(incoming, "transfer_cube (2).fits"))).toBe(sha(remoteFile));
  expect(sha(copy)).toBe(createHash("sha256").update(data).digest("hex")); // the first copy is still the first copy

  const results = await new AxeBuilder({ page }).include('section[aria-label="Transfers"]').analyze();
  expect(results.violations).toEqual([]);
  // the panel as it looks (kept in .run for a person to look at; not an assertion)
  await panel.screenshot({ path: path.resolve(here, "../.run/transfers-panel.png") });

  // leave the shared run as it was found: the other specs know nothing of this host or folder
  const hosts = (await (await page.request.get(`${API}/api/hosts`)).json()) as { hosts: { id: number; name: string }[] };
  const mine = hosts.hosts.find((h) => h.name === "Transfer host");
  if (mine) await page.request.post(`${API}/api/hosts/${mine.id}/delete`, { data: {} });
  fs.rmSync(incoming, { recursive: true, force: true });
  fs.rmSync(remoteFile, { force: true });
});
