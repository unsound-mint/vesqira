import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
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
const workerHome = path.join(run, "remote-home", ".cache", "vesqira");

function hostDown(isDown: boolean) {
  if (!isDown) {
    fs.rmSync(down, { force: true });
    return;
  }
  fs.writeFileSync(down, "");
  // the session dies with the VPN: the host's worker runs from a folder under the stand-in home, which is how it is told apart
  for (const pid of fs.readdirSync("/proc").filter((n) => /^\d+$/.test(n))) {
    try {
      if (fs.readlinkSync(`/proc/${pid}/cwd`).startsWith(workerHome)) process.kill(Number(pid), "SIGKILL");
    } catch {
      /* gone, or not ours */
    }
  }
}

async function openDataset(page: Page, file: string) {
  await page.goto("/");
  await page.getByRole("list", { name: "Datasets" }).getByRole("button", { name: new RegExp(file.replace(".", "\\.")) }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible({ timeout: 30_000 });
}

test.afterAll(() => hostDown(false));

test("a prepared cube keeps working, marked as a copy, when the host goes away", async ({ page }) => {
  fs.copyFileSync(path.join(remoteRoot, "remote_cube.fits"), path.join(remoteRoot, "offline_cube.fits"));
  const host = await page.request.post(`${API}/api/hosts`, { data: { name: "Cluster login", dest: "fake", roots: [remoteRoot] } });
  expect(host.ok()).toBeTruthy();
  const { id } = (await host.json()) as { id: number };
  const reg = await page.request.post(`${API}/api/hosts/${id}/register`, { data: { path: path.join(remoteRoot, "offline_cube.fits") } });
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
  await page.screenshot({ path: path.join(run, "offline-sheet.png") });
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
  await expect(page.getByRole("alert").filter({ hasText: /is not in the offline copy of offline_cube\.fits/ })).toBeVisible({ timeout: 30_000 });
  await expect(banner).toBeVisible(); // still said to be a copy beside the refusal
  for (const [w, h] of [[1440, 900], [1280, 800], [390, 844]]) {
    await page.setViewportSize({ width: w, height: h });
    await page.screenshot({ path: path.join(run, `offline-banner-${w}.png`) });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => { localStorage.clear(); sessionStorage.clear(); }); // the narrow layout remembers a closed explorer

  // the host is back: answers are live again within a moment (the host is asked again in the background), and the banner goes
  hostDown(false);
  await expect(async () => {
    try {
      await openDataset(page, "offline_cube.fits");
      await expect(page.getByTestId("offline-banner")).toHaveCount(0, { timeout: 1500 });
    } catch (e) {
      await page.screenshot({ path: path.join(run, "offline-fail.png") });
      throw e;
    }
  }).toPass({ timeout: 60_000, intervals: [1000, 2000, 3000] });
});
