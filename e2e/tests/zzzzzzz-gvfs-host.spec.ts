import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

// ADR 0031: a folder chosen in the file manager inside a mount of a host that is not registered offers "Add this host…",
// which opens the Hosts section and fills the add-host form from the mount's name. The desktop chooser cannot run
// headless, so the chooser's answer is stubbed; the page, the form and the real API for hosts are the real ones.
test("a mount of an unregistered host fills the add-host form and adds nothing until confirmed", async ({ page }) => {
  await page.route("**/api/fs/choose", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        path: "/run/user/1000/gvfs/sftp:host=login.hpc.example,user=me,port=2222/data",
        hostId: null,
        remotePath: null,
        gvfs: { host: "login.hpc.example", user: "me", port: "2222" },
      }),
    }),
  );
  const created: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && new URL(r.url()).pathname === "/api/hosts") created.push(r.url());
  });
  await page.goto("/");
  await page.getByRole("button", { name: /Browse…/ }).first().click();
  await expect(page.getByRole("status").filter({ hasText: "me@login.hpc.example" })).toBeVisible();
  await page.getByRole("button", { name: "Add this host…" }).click();

  const form = page.getByRole("form", { name: "Add a host" });
  await expect(form).toBeVisible(); // the Hosts section opened
  await expect(form.getByLabel("SSH destination")).toHaveValue("me@login.hpc.example");
  await expect(form.getByLabel("Host name")).toHaveValue("login.hpc.example");
  await expect(form.getByRole("status")).toContainText("port 2222");
  await expect(form.getByRole("button", { name: "Add host" })).toBeDisabled(); // a source folder is still needed
  expect(created).toEqual([]);
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
});
