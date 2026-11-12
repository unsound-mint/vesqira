import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Relayed } from "../api/runRequests";
import { AccountPage } from "../account/AccountPage";
import { RelayedView } from "./RelayedView";
import { RunRequestsPage } from "./RunRequestsPage";

const json = (status: number, b: unknown) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => {
  vi.unstubAllGlobals();
});

const HASH = "f".repeat(64);
const ask = { label: "lab cluster", template: "vgt", params: { sigma: "2" }, resources: {}, grid: [], note: "please run <b>this</b>" };
const relayed = (over: Partial<Relayed> = {}): Relayed => ({
  id: 5, serverId: 9, serverState: "waiting_review", localState: "draft", refusal: "", baseChosen: false, scriptHash: HASH, autoPush: true, runHash: "", seq: 0, reported: "",
  receivedAt: "2026-11-13T10:00:00Z", updatedAt: "2026-11-13T10:01:00Z", origin: "https://lab.example.org",
  draft: { params: { sigma: "2" }, resources: {}, label: "lab cluster" },
  request: { id: 9, change: 1, state: "waiting_review", requester: "acct-2", ask, createdAt: "2026-11-13T09:00:00Z", expiresAt: "2026-11-27T09:00:00Z", notes: [] },
  ...over,
} as Relayed);
const detail = { request: relayed(), job: { id: 3, name: "vgt-draft", state: "draft", script: "#!/bin/bash\nsbatch --cpus-per-task=4\n", scriptHash: HASH, editedLines: 0 } };

describe("the key holder's review of a relayed request", () => {
  it("approves with exactly the script hash it shows, and shows a refusal plainly", async () => {
    const bodies: unknown[] = [];
    let status = 502;
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/relayed/5") return json(200, detail);
      if (url === "/api/runs") return json(200, { runs: [] });
      if (url === "/api/relayed/5/approve") {
        bodies.push(JSON.parse(String(init?.body)));
        return status === 200 ? json(200, { request: relayed({ localState: "approved" }), submitted: true, message: "Approved and submitted." }) : json(status, { error: "group server unreachable" });
      }
      return json(404, { error: "not found" });
    }));
    render(<RelayedView summary={relayed()} onChanged={() => undefined} onClose={() => undefined} />);
    expect(await screen.findByLabelText("Script")).toHaveTextContent("sbatch --cpus-per-task=4");
    // the note is plain text, never markup
    expect(screen.getByText(/please run <b>this<\/b>/)).toBeInTheDocument();
    const button = screen.getByRole("button", { name: `Approve script ${HASH.slice(0, 8)}` });
    await userEvent.click(button);
    expect(await screen.findByRole("alert")).toHaveTextContent("nothing was claimed and nothing was submitted");
    expect(bodies[0]).toEqual({ scriptHash: HASH, autoPush: true });
    status = 409;
    await userEvent.click(button);
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("The request was not claimed"));
    status = 200;
    await userEvent.click(button);
    expect(await screen.findByRole("status")).toHaveTextContent("Approved and submitted.");
    expect(bodies).toHaveLength(3);
  });

  it("declines with the note typed and never before it is typed", async () => {
    const bodies: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/relayed/5") return json(200, detail);
      if (url === "/api/runs") return json(200, { runs: [] });
      if (url === "/api/relayed/5/decline") {
        bodies.push(JSON.parse(String(init?.body)));
        return json(200, relayed({ localState: "declined" }));
      }
      return json(404, { error: "not found" });
    }));
    render(<RelayedView summary={relayed()} onChanged={() => undefined} onClose={() => undefined} />);
    const decline = await screen.findByRole("button", { name: "Decline with this note" });
    expect(decline).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Note to the requester/), "cluster is down this week");
    await userEvent.click(decline);
    expect(await screen.findByRole("status")).toHaveTextContent("Declined.");
    expect(bodies).toEqual([{ note: "cluster is down this week" }]);
  });

  it("sends the sweep hash and the dropped drafts for a sweep", async () => {
    const bodies: unknown[] = [];
    const sweepDetail = {
      request: relayed(),
      sweep: {
        id: 2, sweepHash: "a".repeat(64), view: {}, maxConcurrency: 16,
        drafts: [
          { jobId: 11, name: "s-1", values: { sigma: "1" }, scriptHash: "1".repeat(64), script: "one" },
          { jobId: 12, name: "s-2", values: { sigma: "2" }, scriptHash: "2".repeat(64), script: "two" },
        ],
      },
    };
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/relayed/5") return json(200, sweepDetail);
      if (url === "/api/runs") return json(200, { runs: [] });
      if (url === "/api/relayed/5/approve") {
        bodies.push(JSON.parse(String(init?.body)));
        return json(200, { request: relayed(), submitted: true });
      }
      return json(404, { error: "not found" });
    }));
    render(<RelayedView summary={relayed()} onChanged={() => undefined} onClose={() => undefined} />);
    await userEvent.click(await screen.findByRole("checkbox", { name: /s-2/ }));
    await userEvent.click(screen.getByRole("button", { name: "Approve 1 of 2" }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({ sweepHash: "a".repeat(64), drop: [12], concurrency: 1, stopOnFailure: false });
  });
});

describe("filing a run request", () => {
  it("offers only the keys the inbox allows and sends nothing else", async () => {
    const bodies: unknown[] = [];
    const inbox = {
      account: "acct-2", name: "Grace", updatedAt: "2026-11-13T08:00:00Z",
      inbox: { paused: false, labels: [{ label: "lab cluster", templates: [{ template: "vgt", keys: [{ key: "sigma", min: 1, max: 5 }, { key: "mode", choices: ["a", "b"] }], limits: { maxCpus: 16, maxMemGiB: 64, maxTimeMinutes: 600 } }] }] },
    };
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/run-inboxes") return json(200, { inboxes: [inbox] });
      if (url === "/api/run-requests" && init?.method === "POST") {
        bodies.push(JSON.parse(String(init.body)));
        return json(201, {});
      }
      if (url === "/api/run-requests") return json(200, { requests: [] });
      if (url === "/api/auth/state") return json(200, { stage: "full", account: { id: "acct-1", name: "Ada" } });
      if (url === "/api/pushed") return json(403, { error: "no" });
      return json(404, { error: "not found" });
    }));
    render(<RunRequestsPage />);
    await userEvent.selectOptions(await screen.findByLabelText("Ask"), "acct-2");
    await userEvent.selectOptions(screen.getByLabelText("Cluster label"), "lab cluster");
    await userEvent.selectOptions(screen.getByLabelText("Template"), "vgt");
    expect(screen.getByLabelText("sigma (min 1, max 5)")).toBeInTheDocument();
    const mode = screen.getByLabelText("mode (one of a, b)");
    expect(within(mode).getAllByRole("option").map((o) => o.textContent)).toEqual(["Not set", "a", "b"]);
    expect(screen.queryByLabelText(/output_dir|input_file/)).toBeNull();
    await userEvent.type(screen.getByLabelText("sigma (min 1, max 5)"), "3");
    await userEvent.type(screen.getByLabelText("CPUs"), "8");
    await userEvent.click(screen.getByRole("button", { name: "File request" }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({ addressee: "acct-2", ask: { label: "lab cluster", template: "vgt", params: { sigma: "3" }, resources: { cpus: 8 }, grid: [], note: "" }, expiresInDays: 14 });
  });
});

describe("Link a computer", () => {
  const mint = async (tick: boolean) => {
    const bodies: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/auth/state") return json(200, { stage: "full", recent: true, recentMinutes: 15, recoveryCodesLeft: 0, setupNeeded: false, account: { id: "acct-1", name: "Ada", username: "ada", admin: false, createdAt: "", disabled: false, secondFactor: true } });
      if (url === "/api/account/tokens" && init?.method === "POST") {
        bodies.push(JSON.parse(String(init.body)));
        return json(200, { token: "vsq_x", info: {} });
      }
      if (url === "/api/account/tokens") return json(200, { tokens: [] });
      return json(404, { error: "not found" });
    }));
    render(<AccountPage />);
    if (tick) await userEvent.click(await screen.findByRole("checkbox", { name: "Also take run requests" }));
    await userEvent.click(await screen.findByRole("button", { name: "Link a computer" }));
    await screen.findByLabelText("Link token");
    return bodies[0];
  };

  it("mints push alone by default", async () => {
    expect(await mint(false)).toEqual({ name: "Linked computer", scopes: ["push"], days: 30 });
  });

  it("mints push and relay when asked to take run requests", async () => {
    expect(await mint(true)).toEqual({ name: "Linked computer", scopes: ["push", "relay"], days: 30 });
  });
});
