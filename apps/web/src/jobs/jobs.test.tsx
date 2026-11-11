import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readSetting } from "../api/jobs";
import type { Job } from "../api/jobs";
import type { Host } from "../api/hosts";
import { JobsView, jobFor } from "./JobsView";

const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
const draft: Job = { id: 7, hostId: 1, name: "vgt-ker8", template: "vgt", script: "#!/bin/bash -l\n#SBATCH --cpus-per-task=8\n", scriptHash: "a".repeat(64), state: "draft", detail: "", slurmId: "", workDir: "job-0123456789abcdef", outputDir: "/data/vesqira_runs/x", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: null, note: "", autoImport: true, usage: null };

function mockFetch(jobs: Job[]) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const body = url === "/api/jobs" && !init ? { jobs } : url.endsWith("/submit") ? { ...jobs[0], state: "submitted", slurmId: "4242" } : {};
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("jobs", () => {
  it("reads settings from a config's text, quoted or not, ignoring comments", () => {
    const t = 'ker = 4 # smoothing\nnoise_mode = "scalar"\n#dn = 99\nnoise = 0.17\n';
    expect(readSetting(t, "ker")).toBe("4");
    expect(readSetting(t, "noise_mode")).toBe("scalar");
    expect(readSetting(t, "noise")).toBe("0.17");
    expect(readSetting(t, "dn")).toBe("");
  });

  it("finds the job that produced a run", () => {
    expect(jobFor([{ ...draft, runId: 3 }], 3)?.id).toBe(7);
    expect(jobFor([draft], 3)).toBeNull();
  });

  it("asks for a host before anything else", async () => {
    mockFetch([]);
    render(<JobsView hosts={[]} onImported={() => {}} onOpenRun={() => {}} />);
    expect(await screen.findByText(/Add a host first/)).toBeInTheDocument();
  });

  it("shows the exact script, makes you save edits before submitting, and submits by the script's hash", async () => {
    mockFetch([draft]);
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: /vgt-ker8/ }));
    const script = screen.getByRole("textbox", { name: /Script/ });
    expect(script).toHaveValue(draft.script);
    expect(screen.getByText(/this exact text is what will be submitted/)).toBeInTheDocument();
    const submit = screen.getByRole("button", { name: "Submit this script" });
    expect(submit).toBeEnabled();
    await userEvent.type(script, "echo hi");
    expect(submit).toBeDisabled(); // unsaved edits: what is sent must be what is shown
    expect(screen.getByText(/Save your edits before submitting/)).toBeInTheDocument();
  });

  it("sends the reviewed script's hash when submitting", async () => {
    const calls = mockFetch([draft]);
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: /vgt-ker8/ }));
    await userEvent.click(screen.getByRole("button", { name: "Submit this script" }));
    const call = calls.find((c) => c.url.endsWith("/submit"))!;
    expect(JSON.parse(String(call.init!.body))).toEqual({ scriptHash: draft.scriptHash, autoImport: true });
  });

  it("offers automatic import for a VGT draft, on by default, and submits the choice", async () => {
    const calls = mockFetch([draft]);
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: /vgt-ker8/ }));
    const box = screen.getByRole("checkbox", { name: /import its results as a run/ });
    expect(box).toBeChecked();
    expect(screen.getByText(/checks nothing/)).toBeInTheDocument();
    await userEvent.click(box);
    await userEvent.click(screen.getByRole("button", { name: "Submit this script" }));
    const call = calls.find((c) => c.url.endsWith("/submit"))!;
    expect(JSON.parse(String(call.init!.body))).toEqual({ scriptHash: draft.scriptHash, autoImport: false });
  });

  it("offers no automatic import for a custom script, which names no output folder", async () => {
    mockFetch([{ ...draft, template: "custom", autoImport: false }]);
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: /vgt-ker8/ }));
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("offers automatic import of a ROHSA fit, on by default, says the .dat is not read, and submits the choice", async () => {
    const calls = mockFetch([{ ...draft, template: "rohsa", autoImport: true }]);
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: /vgt-ker8/ }));
    const box = screen.getByRole("checkbox", { name: /import its fit as a run.*the \.dat is not read/ });
    expect(box).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Submit this script" }));
    const call = calls.find((c) => c.url.endsWith("/submit"))!;
    expect(JSON.parse(String(call.init!.body))).toEqual({ scriptHash: draft.scriptHash, autoImport: true });
  });

  it("explains the notification toggle where jobs are listed", async () => {
    mockFetch([]);
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    expect(await screen.findByText(/cannot show desktop notifications/)).toBeInTheDocument();
  });
});
