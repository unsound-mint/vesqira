import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { API } from "../ports";

// Prepare for offline (ADR 0059) through the real API, worker and built app, with the stand-in ssh that runs the "host" on this
// machine: prepare a few planes of a cube on the host from the preparation sheet, then take the host away (the stand-in ssh refuses,
// and the open session dies as a dropped VPN would kill it), and see the viewer go on from the copy, say so, and name what is missing.

const here = path.dirname(fileURLToPath(import.meta.url));
const run = path.resolve(here, "../.run");
const remoteRoot = path.join(run, "remote_data");
const down = path.join(run, "remote-home", "DOWN");

function hostDown(isDown: boolean) {
  if (isDown) {
    fs.writeFileSync(down, "");
    try {
      execFileSync("pkill", ["-f", path.join(run, "remote-home", ".cache", "vesqira")]); // the session dies with the VPN
    } catch {
      /* nothing was running */
    }
  } else fs.rmSync(down, { force: true });
}

async function openDataset(page: Page, file: string) {
  await page.goto("/");
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: new RegExp(file.replace(".", "\\.")) }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible({ timeout: 30_000 });
}

test.afterAll(() => hostDown(false));

test("a prepared cube keeps working, marked as a copy, when the host goes away", async ({ page }) => {
  fs.copyFileSync(path.join(remoteRoot, "remote_cube.fits"), path.join(remoteRoot, "offline_cube.fits"));
  const host = await page.request.post(`${API}/api/hosts`, { data: { name: "Offline host", dest: "fake", roots: [remoteRoot] } });
  expect(host.ok()).toBeTruthy();
  const { id } = (await host.json()) as { id: number };
  const reg = await page.request.post(`${API}/api/hosts/${id}/register`, { data: { path: path.join(remoteRoot, "offline_cube.fits"), asNew: true } });
  expect(reg.ok()).toBeTruthy();

  await openDataset(page, "offline_cube.fits");
  // the Offline section, then the sheet
  await page.getByRole("button", { name: /^Offline/ }).click();
  await page.getByRole("button", { name: "Prepare for offline…" }).click();
  const sheet = page.getByRole("dialog", { name: /Prepare offline_cube\.fits for offline/ });
  await expect(sheet).toContainText("never copies the cube");
  await sheet.getByLabel("First channel").fill("0");
  await sheet.getByLabel("Last channel").fill("3");
  await sheet.getByLabel("Plane detail").selectOption("full");
  await expect(sheet).toContainText("Works partially", { timeout: 30_000 }); // a status in words
  await expect(sheet).toContainText("planes 0–3 (full resolution)");
  await expect(sheet).toContainText(/About .* of your 256\.0 MiB budget/);
  await sheet.getByRole("button", { name: "Prepare", exact: true }).click();
  await expect(sheet).toContainText("Done.", { timeout: 60_000 });
  await sheet.getByRole("button", { name: "Close" }).click();
  await expect(page.getByText(/Kept: .*planes 0–3/)).toBeVisible();

  // the host goes away: the same view, from the copy, with the banner
  hostDown(true);
  await openDataset(page, "offline_cube.fits");
  const banner = page.getByTestId("offline-banner");
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("Offline copy");
  await expect(banner).toContainText("It cannot show anything newer");
  // a channel that was not kept fails with a sentence that names it, and the banner repeats it
  await page.getByRole("group", { name: "Plane viewer" }).press("End");
  await expect(page.getByText(/is not in the offline copy of offline_cube\.fits/).first()).toBeVisible({ timeout: 30_000 });

  // the host is back: live again, no banner
  hostDown(false);
  await openDataset(page, "offline_cube.fits");
  await expect(page.getByTestId("offline-banner")).toHaveCount(0);
});
