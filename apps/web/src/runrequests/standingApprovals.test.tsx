import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Grant, Relayed, RelayLocal } from "../api/runRequests";
import { RelayedView } from "./RelayedView";
import { RunRequestsPage } from "./RunRequestsPage";
import { StandingApprovals } from "./StandingApprovals";

// ADR 0074: the key holder's standing approvals, the collaborator's choice of one with the preview, and the note on a request
// submitted under one.

const json = (status: number, b: unknown) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
afterEach(() => vi.unstubAllGlobals());

const GID = "c".repeat(32);
const COMMIT = "a".repeat(40);
const grant = (over: Partial<Grant> = {}): Grant => ({
  id: GID, requesterId: "acct-2", requesterName: "Bea", label: "lab cluster", hostId: 1, template: "vgt", profile: "VGT (2025)", commit: COMMIT, commitBasis: "profile",
  keys: [{ key: "ker", min: 4, max: 8 }], roots: ["/scratch/lmc/cubes"], files: [], maxJobs: 10, maxCoreHours: 200, perJob: { maxCpus: 8, maxMemGiB: 16, maxTimeMinutes: 120 },
  maxAtOnce: 2, note: "", createdAt: "2026-11-01T10:00:00Z", expiresAt: "2026-12-01T10:00:00Z", jobsUsed: 3, coreMinutesUsed: 1440, atOnce: 1, live: true,
  hostName: "Cluster", openJobs: [{ id: 41, name: "request-7", state: "running" }], ...over,
});
const local: RelayLocal = {
  linked: true, workspace: "Lab", hosts: [{ id: 1, name: "Cluster" }], requests: [],
  state: {
    origin: "https://lab.example.org", configured: true, published: true, cursor: 0, failures: 0, message: "", hosts: { "lab cluster": 1 },
    inbox: { paused: false, labels: [{ label: "lab cluster", templates: [{ template: "vgt", keys: [{ key: "ker", min: 2, max: 16 }, { key: "dn", choices: ["8", "16", "24"] }], limits: { maxCpus: 16, maxMemGiB: 32, maxTimeMinutes: 240 } }] }] },
  },
} as RelayLocal;

describe("standing approvals on the key holder's computer", () => {
  it("lists usage against the budget and revokes only after a confirmation, listing the jobs it leaves running", async () => {
    const posts: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/relay/local/grants" && !init?.method) return json(200, { linked: true, grants: [grant()], templates: { vgt: { profile: "VGT (2025)", commit: COMMIT } } });
      if (url === "/api/relay/local/members") return json(200, { members: [{ id: "acct-2", name: "Bea" }] });
      if (url === `/api/relay/local/grants/${GID}/revoke`) {
        posts.push(url);
        return json(200, grant({ live: false, revokedAt: "2026-11-13T12:00:00Z" }));
      }
      return json(404, { error: "not found" });
    }));
    render(<StandingApprovals local={local} onChanged={() => undefined} />);
    const list = await screen.findByRole("list", { name: "Live standing approvals" });
    expect(list).toHaveTextContent("Bea");
    expect(list).toHaveTextContent("ker 4 to 8");
    expect(list).toHaveTextContent("Jobs: 3 of 10");
    expect(list).toHaveTextContent("Core-hours: 24 of 200");
    expect(list).toHaveTextContent("request-7 (running)");
    await userEvent.click(within(list).getByRole("button", { name: "Revoke" }));
    expect(posts).toHaveLength(0); // nothing until confirmed
    await userEvent.click(screen.getByRole("button", { name: "Revoke now" }));
    expect(await screen.findByRole("status")).toHaveTextContent("1 job(s) started under it keep running and were not cancelled: request-7 (running)");
    expect(posts).toHaveLength(1);
  });

  it("makes a grant with the settings narrowed and the inbox's kind of bound kept", async () => {
    const bodies: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/relay/local/grants" && init?.method === "POST") {
        bodies.push(JSON.parse(String(init.body)));
        return json(201, grant());
      }
      if (url === "/api/relay/local/grants") return json(200, { linked: true, grants: [], templates: { vgt: { profile: "VGT (2025)", commit: COMMIT } } });
      if (url === "/api/relay/local/members") return json(200, { members: [{ id: "acct-2", name: "Bea" }] });
      return json(404, { error: "not found" });
    }));
    render(<StandingApprovals local={local} onChanged={() => undefined} />);
    await userEvent.click(await screen.findByRole("button", { name: "New standing approval" }));
    await userEvent.selectOptions(screen.getByLabelText("Collaborator"), "acct-2");
    await userEvent.click(screen.getByRole("checkbox", { name: /^ker/ }));
    await userEvent.clear(screen.getByLabelText("ker: from"));
    await userEvent.type(screen.getByLabelText("ker: from"), "4");
    await userEvent.clear(screen.getByLabelText("ker: to"));
    await userEvent.type(screen.getByLabelText("ker: to"), "8");
    await userEvent.click(screen.getByRole("checkbox", { name: /^dn/ }));
    await userEvent.clear(screen.getByLabelText("dn: allowed values"));
    await userEvent.type(screen.getByLabelText("dn: allowed values"), "8, 16");
    await userEvent.type(screen.getByLabelText(/Input folders/), "/scratch/lmc/cubes");
    await userEvent.click(screen.getByRole("button", { name: "Make standing approval" }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      requesterId: "acct-2", requesterName: "Bea", label: "lab cluster", template: "vgt", commitBasis: "profile",
      keys: [{ key: "ker", min: 4, max: 8 }, { key: "dn", choices: ["8", "16"] }], roots: ["/scratch/lmc/cubes"], files: [],
      perJob: { maxCpus: 16, maxMemGiB: 32, maxTimeMinutes: 240 }, expiresInDays: 30,
    });
    expect(await screen.findByRole("status")).toHaveTextContent("Made a standing approval for Bea");
  });
});

describe("filing under a standing approval", () => {
  it("shows the offer's terms and whether the request goes through it before sending, and sends the grant id", async () => {
    const filed: unknown[] = [];
    const previews: unknown[] = [];
    const offer = { id: GID, requester: "acct-1", label: "lab cluster", template: "vgt", keys: [{ key: "ker", min: 4, max: 8 }], perJob: { maxCpus: 8, maxMemGiB: 16, maxTimeMinutes: 120 },
      maxJobs: 10, jobsUsed: 3, maxCoreHours: 200, coreHoursUsed: 24, maxAtOnce: 2, expiresAt: "2026-12-01T10:00:00Z" };
    const inbox = { account: "acct-2", name: "Ada", updatedAt: "2026-11-13T08:00:00Z",
      inbox: { paused: false, labels: [{ label: "lab cluster", templates: [{ template: "vgt", keys: [{ key: "ker", min: 2, max: 16 }], limits: { maxCpus: 16, maxMemGiB: 32, maxTimeMinutes: 240 } }] }], grants: [offer] } };
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/run-inboxes") return json(200, { inboxes: [inbox] });
      if (url === "/api/run-requests/preview") {
        const b = JSON.parse(String(init?.body));
        previews.push(b);
        return Number(b.ask.params.ker) > 8
          ? json(200, { route: "review", reason: "It would wait for review: outside the standing approval: ker is at most 8 here." })
          : json(200, { route: "grant", grant: offer, reason: "It fits the standing approval as last published." });
      }
      if (url === "/api/run-requests" && init?.method === "POST") {
        filed.push(JSON.parse(String(init.body)));
        return json(201, {});
      }
      if (url === "/api/run-requests") return json(200, { requests: [] });
      if (url === "/api/auth/state") return json(200, { stage: "full", account: { id: "acct-1", name: "Bea" } });
      if (url === "/api/pushed") return json(200, { items: [{ id: 7, kind: "run", title: "vgt-base", archived: false }] });
      return json(404, { error: "not found" });
    }));
    render(<RunRequestsPage />);
    await userEvent.selectOptions(await screen.findByLabelText("Ask"), "acct-2");
    await userEvent.selectOptions(screen.getByLabelText("Cluster label"), "lab cluster");
    await userEvent.selectOptions(screen.getByLabelText("Template"), "vgt");
    await userEvent.selectOptions(screen.getByLabelText("Standing approval"), GID);
    expect(screen.getByText(/Under this standing approval: ker 4 to 8/)).toHaveTextContent("3 of 10 jobs and 24 of 200 core-hours used");
    await userEvent.selectOptions(screen.getByLabelText(/Based on a pushed run/), "7");
    await userEvent.type(screen.getByLabelText("ker (min 2, max 16)"), "12");
    expect(await screen.findByText("Waits for review.")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("ker is at most 8");
    await userEvent.clear(screen.getByLabelText("ker (min 2, max 16)"));
    await userEvent.type(screen.getByLabelText("ker (min 2, max 16)"), "6");
    expect(await screen.findByText("Goes through the standing approval.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "File under the standing approval" }));
    await waitFor(() => expect(filed).toHaveLength(1));
    expect(filed[0]).toEqual({ addressee: "acct-2", ask: { label: "lab cluster", template: "vgt", params: { ker: "6" }, resources: {}, grid: [], note: "", grant: GID }, baseItemId: 7, expiresInDays: 14 });
    expect(previews.length).toBeGreaterThan(0);
  });
});

describe("a request submitted under a standing approval", () => {
  it("says so and shows the exact script that was submitted", async () => {
    const r = {
      id: 5, serverId: 9, serverState: "submitted", localState: "approved", refusal: "", baseChosen: false, scriptHash: "f".repeat(64), autoPush: true, runHash: "", seq: 1, reported: "",
      receivedAt: "2026-11-13T10:00:00Z", updatedAt: "2026-11-13T10:01:00Z", origin: "https://lab.example.org", grantId: GID, grantState: "used", grantNote: "",
      draft: { params: { ker: "6" }, resources: {}, label: "lab cluster" },
      request: { id: 9, change: 1, state: "requested", requester: "Bea", ask: { label: "lab cluster", template: "vgt", params: { ker: "6" }, resources: {}, grid: [], note: "", grant: GID }, createdAt: "2026-11-13T09:00:00Z", expiresAt: "2026-11-27T09:00:00Z", notes: [] },
    } as Relayed;
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      if (url === "/api/relayed/5") return json(200, { request: r, outsideBounds: "", job: { id: 3, name: "request-9", state: "queued", script: "#!/bin/bash\n#SBATCH --cpus-per-task=4\n", scriptHash: "f".repeat(64), editedLines: 0 } });
      if (url === "/api/runs") return json(200, { runs: [] });
      return json(404, { error: "not found" });
    }));
    render(<RelayedView summary={r} onChanged={() => undefined} onClose={() => undefined} />);
    expect(await screen.findByLabelText("Submitted script")).toHaveTextContent("--cpus-per-task=4");
    expect(screen.getByRole("note")).toHaveTextContent(`Submitted without a click under standing approval ${GID.slice(0, 8)}`);
    expect(screen.queryByRole("button", { name: /Approve/ })).toBeNull();
  });
});
