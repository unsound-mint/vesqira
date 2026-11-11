import { expect, test, type Page } from "@playwright/test";
import os from "node:os";
import path from "node:path";

// Narrow screens (<= 800 px): the explorer is an overlay on the canvas. Whatever page a person goes to, its content has to be
// what they see; the explorer covers it only while it is asked for.
const SHOTS = path.join(os.homedir(), ".cache/agent-tmp-mobile/shots");
const DESTINATIONS = ["Open project…", "Boards", "Jobs", "Reports", "Terminal"] as const;

async function mainIsUncovered(page: Page) {
  const main = page.getByRole("main");
  await expect(main).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Explorer" })).toBeHidden();
  const covered = await main.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(r.height / 2, 120));
    return !hit || !el.contains(hit);
  });
  expect(covered).toBe(false);
  expect(((await main.innerText()) || "").trim().length).toBeGreaterThan(0);
}

for (const width of [390, 600, 800]) {
  for (const scheme of ["dark", "light"] as const) {
    test(`at ${width} px (${scheme}) every rail destination shows its page, not the explorer`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 });
      await page.emulateMedia({ colorScheme: scheme });
      for (const name of DESTINATIONS) {
        await page.goto("/");
        await page.evaluate(() => localStorage.setItem("vesqira.layout.v1", JSON.stringify({ side: 280, insp: 360, timeline: 196, sideOpen: true, inspOpen: true, timelineOpen: true })));
        // a layout saved on a wide screen says the explorer is open
        await page.reload();
        const rail = page.getByRole("button", { name, exact: true });
        if (name === "Terminal" && !(await rail.count())) continue;
        await rail.click();
        if (name === "Terminal") {
          await expect(page.getByRole("dialog", { name: "Terminal" })).toBeVisible();
          if (width === 390 && scheme === "dark") await page.screenshot({ path: path.join(SHOTS, "narrow-Terminal.png") });
          continue;
        }
        await mainIsUncovered(page);
        if (name !== "Reports") { // reports have their own page tree instead of the explorer
          // the explorer can still be asked for, and takes the page's place while open
          await page.getByRole("button", { name: "Toggle explorer" }).click();
          await expect(page.getByRole("complementary", { name: "Explorer" })).toBeVisible();
          await page.getByRole("button", { name: "Toggle explorer" }).click();
          await mainIsUncovered(page);
        }
        if (width === 390 && scheme === "dark") await page.screenshot({ path: path.join(SHOTS, `narrow-${name.replace(/\W+/g, "")}.png`) });
      }
      // the viewer itself, first paint
      await page.goto("/");
      await mainIsUncovered(page);
    });
  }
}
