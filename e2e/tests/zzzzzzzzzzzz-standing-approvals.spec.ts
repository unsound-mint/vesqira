import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

// Standing approvals (ADR 0074), the screens only. The relay needs a group server on TLS with a second account, so these answer
// the relay routes with fixed data (page.route) and drive the real app: the key holder's inbox on their computer and the
// collaborator's request form on the group server. The grant flow itself (offer, filing, the key holder's computer checking every
// term, the claim, sbatch, revocation, the budget race) is tested end to end in services/api/internal/server/relay_grant_test.go.

async function expectNoAxeViolations(page: Page) {
  const r = await new AxeBuilder({ page: page as never }).withTags(["wcag2a", "wcag2aa", "best-practice"]).analyze();
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.target.join(" ")}`)).toEqual([]);
}

async function shots(page: Page, name: string) {
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await expectNoAxeViolations(page);
    for (const [w, h] of [[1440, 900], [390, 844]]) {
      await page.setViewportSize({ width: w, height: h });
      await page.screenshot({ path: test.info().outputPath(`${name}-${w}-${scheme}.png`), fullPage: true });
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "light" });
}

const GID = "3f9a1c0be2d84e57a6c1d0f2b9e4a713";
const COMMIT = "9c41e0d7a2b35f8861c0e4d2b7a9f31c5d6e8b02";
const SCRIPT = "#!/bin/bash -l\n#SBATCH --job-name=request-12\n#SBATCH --cpus-per-task=8\n#SBATCH --mem=16G\n#SBATCH --time=02:00:00\nset -euo pipefail\n# the job stops unless the code folder is at commit " + COMMIT + "\ncd /scratch/u/vgt\njulia --project run_vgt.jl config.toml\n";

const inbox = {
  paused: false,
  labels: [{ label: "lab cluster", templates: [{ template: "vgt", keys: [{ key: "ker", min: 2, max: 16 }, { key: "dn", choices: ["8", "16", "24"] }], limits: { maxCpus: 16, maxMemGiB: 32, maxTimeMinutes: 240 } }] }],
};
const grantRow = (over: Record<string, unknown> = {}) => ({
  id: GID, requesterId: "acct-5e1b", requesterName: "Bea Lindqvist", label: "lab cluster", hostId: 1, template: "vgt", profile: "VGT (ISM-NU, 2025)", commit: COMMIT, commitBasis: "profile",
  keys: [{ key: "ker", min: 4, max: 8 }, { key: "dn", choices: ["8", "16"] }], roots: ["/scratch/lmc/hi_cubes"], files: [], maxJobs: 12, maxCoreHours: 300,
  perJob: { maxCpus: 8, maxMemGiB: 16, maxTimeMinutes: 120 }, maxAtOnce: 2, note: "LMC kernel sweep for the March report", createdAt: "2026-11-02T09:00:00Z", expiresAt: "2026-12-02T09:00:00Z",
  jobsUsed: 4, coreMinutesUsed: 3840, atOnce: 1, live: true, hostName: "Cluster (login1)", openJobs: [{ id: 81, name: "request-12", state: "running" }], ...over,
});
const relayed = (id: number, serverId: number, over: Record<string, unknown>, ask: Record<string, unknown>) => ({
  id, serverId, serverState: "waiting_review", localState: "draft", refusal: "", baseChosen: false, scriptHash: "e".repeat(64), autoPush: true, runHash: "", seq: 0, reported: "",
  receivedAt: "2026-11-13T09:30:00Z", updatedAt: "2026-11-13T09:31:00Z", origin: "https://group.lab.example", jobId: 80 + id, baseRunId: 3, grantId: "", grantState: "", grantNote: "",
  draft: { params: ask.params, resources: {}, label: "lab cluster" },
  request: { id: serverId, change: serverId, state: "waiting_review", requester: "Bea Lindqvist", requesterId: "acct-5e1b", createdAt: "2026-11-13T09:00:00Z", expiresAt: "2026-11-27T09:00:00Z", notes: [],
    base: { origin: "o", hash: "h", title: "vgt LMC block 3", instance: "i" }, ask: { label: "lab cluster", template: "vgt", resources: {}, grid: [], note: "", ...ask } },
  ...over,
});
const used = relayed(1, 12, { localState: "approved", serverState: "running", grantId: GID, grantState: "used" }, { params: { ker: "6", dn: "16" }, grant: GID });
const fell = relayed(2, 13, { grantState: "outside", grantNote: "Waiting for you: outside the standing approval. Outside the standing approval: ker is at most 8 here." }, { params: { ker: "12" }, grant: GID });

async function keyHolder(page: Page) {
  let grants = [grantRow(), grantRow({ id: "a".repeat(32), requesterName: "Chen Wu", live: false, revokedAt: "2026-11-10T16:00:00Z", jobsUsed: 2, coreMinutesUsed: 960, atOnce: 0, openJobs: [] })];
  await page.route("**/api/relay/local", (r) => r.fulfill({ json: {
    linked: true, workspace: "ISM group", hosts: [{ id: 1, name: "Cluster (login1)" }], requests: [used, fell],
    state: { origin: "https://group.lab.example", configured: true, inbox, hosts: { "lab cluster": 1 }, published: true, cursor: 13, polledAt: "2026-11-13T09:40:00Z", answeredAt: "2026-11-13T09:40:00Z", failures: 0, message: "" },
  } }));
  await page.route("**/api/relay/local/grants", (r) => {
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON();
      const made = grantRow({ id: "b".repeat(32), requesterName: b.requesterName, keys: b.keys, roots: b.roots, jobsUsed: 0, coreMinutesUsed: 0, atOnce: 0, openJobs: [], note: b.note });
      grants = [made, ...grants];
      return r.fulfill({ status: 201, json: made });
    }
    return r.fulfill({ json: { linked: true, grants, templates: { vgt: { profile: "VGT (ISM-NU, 2025)", commit: COMMIT }, rohsa: { profile: "ROHSA", commit: "1".repeat(40) } } } });
  });
  await page.route(`**/api/relay/local/grants/${GID}/revoke`, (r) => {
    const g = grantRow({ live: false, revokedAt: "2026-11-13T10:00:00Z" });
    grants = grants.map((x) => (x.id === GID ? g : x));
    return r.fulfill({ json: g });
  });
  await page.route("**/api/relay/local/members", (r) => r.fulfill({ json: { members: [{ id: "acct-5e1b", name: "Bea Lindqvist" }, { id: "acct-77c2", name: "Chen Wu" }] } }));
  await page.route("**/api/relayed/1", (r) => r.fulfill({ json: { request: used, outsideBounds: "", code: `The script insists on commit ${COMMIT}, the one the standing approval allows.`,
    job: { id: 81, name: "request-12", state: "running", script: SCRIPT, scriptHash: "e".repeat(64), editedLines: 0 } } }));
}

test("the key holder makes, reads and revokes standing approvals, and sees what ran under one", async ({ page }) => {
  await keyHolder(page);
  await page.goto("/#/inbox");
  const section = page.getByRole("region", { name: "Standing approvals" });
  await expect(section).toBeVisible();
  const live = section.getByRole("list", { name: "Live standing approvals" });
  await expect(live).toContainText("Bea Lindqvist");
  await expect(live).toContainText("ker 4 to 8; dn one of 8, 16");
  await expect(live).toContainText("Jobs: 4 of 12");
  await expect(live).toContainText("Core-hours: 64 of 300");
  await expect(live).toContainText("request-12 (running)");
  // the requests list says which went through and which waits, and why
  const requests = page.getByRole("table");
  await expect(requests).toContainText("submitted without a click under the standing approval");
  await expect(requests).toContainText("waiting for you: it asks for settings or resources outside the standing approval");
  await shots(page, "standing-approvals-inbox");

  // keyboard: Revoke asks first, focus lands on the safe "Keep it", and Enter there backs out
  await live.getByRole("button", { name: "Revoke" }).focus();
  await page.keyboard.press("Enter");
  const confirm = page.getByRole("group", { name: "Revoke the standing approval for Bea Lindqvist" });
  await expect(confirm).toContainText("Jobs already queued or running keep running.");
  await expect(confirm.getByRole("button", { name: "Keep it" })).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(confirm.getByRole("button", { name: "Revoke now" })).toBeFocused();
  await page.keyboard.press("Tab");
  await page.keyboard.press("Enter");
  await expect(confirm).toHaveCount(0);

  // a new one: the form offers the inbox's settings to narrow
  await section.getByRole("button", { name: "New standing approval" }).click();
  await section.getByLabel("Collaborator").selectOption("acct-77c2");
  await section.getByRole("checkbox", { name: /^ker/ }).check();
  await section.getByLabel("ker: from").fill("4");
  await section.getByLabel("ker: to").fill("6");
  await section.getByLabel(/Input folders/).fill("/scratch/lmc/hi_cubes");
  await shots(page, "standing-approvals-form");
  await section.getByRole("button", { name: "Make standing approval" }).click();
  await expect(section.getByRole("status")).toContainText("Made a standing approval for Chen Wu");

  // revoke for real: immediate, and the running job is listed, not cancelled
  await live.getByRole("listitem").filter({ hasText: "Bea Lindqvist" }).getByRole("button", { name: "Revoke" }).click();
  await page.getByRole("button", { name: "Revoke now" }).click();
  await expect(section.getByRole("status")).toContainText("1 job(s) started under it keep running and were not cancelled: request-12 (running)");

  // the request that went through: the note and the exact script
  await requests.getByRole("row").filter({ hasText: "submitted without a click" }).getByRole("button", { name: "vgt on lab cluster" }).click();
  const view = page.getByRole("region", { name: "Request 12" });
  await expect(view.getByRole("note")).toContainText("Submitted without a click under standing approval 3f9a1c0b");
  await expect(view.getByLabel("Submitted script")).toContainText("--cpus-per-task=8");
  await expect(view.getByRole("button", { name: /Approve/ })).toHaveCount(0);
  await view.scrollIntoViewIfNeeded();
  await shots(page, "standing-approvals-submitted");
});

test("the collaborator sees, before sending, whether a request goes through the standing approval", async ({ page }) => {
  const offer = { id: GID, requester: "acct-5e1b", label: "lab cluster", template: "vgt", keys: [{ key: "ker", min: 4, max: 8 }, { key: "dn", choices: ["8", "16"] }],
    perJob: { maxCpus: 8, maxMemGiB: 16, maxTimeMinutes: 120 }, maxJobs: 12, jobsUsed: 4, maxCoreHours: 300, coreHoursUsed: 64, maxAtOnce: 2, expiresAt: "2026-12-02T09:00:00Z" };
  await page.route("**/api/capabilities", async (r) => {
    const real = await r.fetch();
    const c = await real.json();
    c.features = { ...c.features, accounts: true };
    c.workspace = { id: 1, name: "ISM group", role: "researcher", permissions: ["run_request.read", "run_request.file", "pushed.read"], workspaces: [{ id: 1, name: "ISM group", role: "researcher" }] };
    return r.fulfill({ json: c });
  });
  await page.route("**/api/auth/state", (r) => r.fulfill({ json: { stage: "full", recent: true, recentMinutes: 15, recoveryCodesLeft: 8, setupNeeded: false,
    account: { id: "acct-5e1b", name: "Bea Lindqvist", username: "bea", admin: false, createdAt: "2026-10-01T00:00:00Z", disabled: false, secondFactor: true } } }));
  await page.route("**/api/run-inboxes", (r) => r.fulfill({ json: { inboxes: [{ account: "acct-0a11", name: "Ada Okafor", updatedAt: "2026-11-13T09:40:00Z", polledAt: "2026-11-13T09:40:00Z", inbox: { ...inbox, grants: [offer] } }] } }));
  await page.route("**/api/run-requests", (r) => r.fulfill(r.request().method() === "POST" ? { status: 201, json: {} } : { json: { requests: [] } }));
  await page.route("**/api/pushed", (r) => r.fulfill({ json: { items: [{ id: 7, kind: "run", title: "vgt LMC block 3", archived: false }] } }));
  await page.route("**/api/run-requests/preview", (r) => {
    const b = r.request().postDataJSON();
    return r.fulfill({ json: Number(b.ask.params.ker) > 8
      ? { route: "review", reason: "It would wait for review: outside the standing approval: ker is at most 8 here." }
      : { route: "grant", grant: offer, reason: "It fits the standing approval as last published, so the key holder's computer should submit it without a click. That computer still checks the base run's input, the code commit and the budget itself, and anything outside the approval waits for review." } });
  });
  await page.goto("/#/run-requests");
  await page.getByLabel("Ask").selectOption("acct-0a11");
  await page.getByLabel("Cluster label").selectOption("lab cluster");
  await page.getByLabel("Template").selectOption("vgt");
  await page.getByLabel("Standing approval").selectOption(GID);
  await page.getByLabel(/Based on a pushed run/).selectOption("7");
  await page.getByLabel("ker (min 2, max 16)").fill("12");
  await expect(page.getByText("Waits for review.")).toBeVisible();
  await page.getByLabel("ker (min 2, max 16)").fill("6");
  await expect(page.getByText("Goes through the standing approval.")).toBeVisible();
  await expect(page.getByRole("button", { name: "File under the standing approval" })).toBeEnabled();
  await shots(page, "standing-approvals-filing");
});
