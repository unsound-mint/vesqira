import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import type { Job } from "../api/jobs";
import { RunPanel, runFor } from "./RunPanel";
import { RunPage } from "../runs/RunPage";
import { Shell } from "../shell/Shell";

const run = (over: Partial<Run> = {}): Run => ({
  jobId: null, parentRunId: null, id: 1, name: "s1_ker8", kind: "vgt", importedAt: "", generatedAt: "2026-06-25T09:21:44Z", gitCommit: "bcbaed4ec9800ef95c561cfb4ad45c344743e469-dirty",
  dirty: true, reproducibleFromCommit: false, elapsedSeconds: 106.2, params: [{ key: "ker", value: "8", source: "config" }], diagnostics: {},
  inputName: "hnv1.fits", inputSizeBytes: 4007885760, inputHint: null, metrics: [],
  outputs: [{ role: "psi", datasetId: 5, name: "psi.fits" }, { role: "reliability", datasetId: 6, name: "rel.fits" }], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over,
});
const job: Job = { id: 7, hostId: 1, name: "vgt-ker8", template: "vgt", script: "", scriptHash: "", state: "running", detail: "", slurmId: "48235", workDir: "", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: 1, parentRunId: null };
const page = (r: Run, over: { runs?: Run[]; datasets?: Dataset[]; job?: Job | null } = {}) =>
  render(<RunPage run={r} runs={over.runs ?? [r]} datasets={over.datasets ?? []} job={over.job ?? null} onBack={() => {}} onOpenRun={() => {}} onOpenInViewer={() => {}} />);

afterEach(() => vi.unstubAllGlobals());

describe("run", () => {
  it("finds the run a dataset belongs to", () => {
    expect(runFor([run()], 6)?.role).toBe("reliability");
    expect(runFor([run()], 99)).toBeNull();
    expect(runFor([run()], null)).toBeNull();
  });

  it("the inspector keeps a compact summary with a way to the run page", async () => {
    const open = vi.fn();
    render(<RunPanel run={run()} role="psi" onOpenPage={open} />);
    expect(screen.getByText(/Not reproducible from the commit: uncommitted changes/)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Open run page" }));
    expect(open).toHaveBeenCalled();
  });
});

describe("run page", () => {
  it("says a dirty commit is not reproducible and that the input link is only a hint", () => {
    page(run());
    expect(screen.getByText(/Reproducible: No, the commit does not describe the code that ran/)).toBeInTheDocument();
    expect(screen.getByText(/No registered file matches it by name and size/)).toBeInTheDocument();
    expect(screen.getByText("bcbaed4ec980-dirty")).toBeInTheDocument();
    expect(screen.getByText("code has uncommitted changes")).toBeInTheDocument();
  });

  it("calls a clean commit recorded and offers the inferred input with its basis", () => {
    const d = { id: 9, name: "hnv1.fits" } as Dataset;
    page(run({ dirty: false, reproducibleFromCommit: true, gitCommit: "abc", inputHint: { datasetId: 9, basis: "inferred from file name and size; not proof of identity" } }), { datasets: [d] });
    expect(screen.getByText("Commit recorded")).toBeInTheDocument();
    expect(screen.getByText(/not proof of identity/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open it" })).toBeInTheDocument();
  });

  it("has a result block and the numbers as declared by the run, with an unrecorded one as unknown", () => {
    const m = { key: "am", value: null, unit: "", uncertainty: null, uncertaintyKind: "" as const, n: null, definition: "Axial agreement", status: "declared" as const };
    page(run({ metrics: [m] }));
    expect(screen.getByRole("button", { name: /Result · Orientation \(psi\)/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /More results \(1\)/ })).toBeInTheDocument();
    expect(screen.getByText(/As declared by the run; Vesqira did not compute or check them/)).toBeInTheDocument();
    expect(screen.getByText("not recorded")).toBeInTheDocument();
  });

  it("shows the parent value of a setting that differs and marks nothing else", () => {
    const parent = run({ id: 2, name: "s1_ker4", params: [{ key: "ker", value: "4", source: "config" }, { key: "dn", value: "9", source: "config" }] });
    const child = run({ id: 3, parentRunId: 2, params: [{ key: "ker", value: "8", source: "config" }, { key: "dn", value: "9", source: "config" }] });
    page(child, { runs: [parent, child] });
    expect(screen.getByText("differs from parent: ker")).toBeInTheDocument();
    expect(screen.getByText("parent 4")).toBeInTheDocument();
    expect(screen.queryByText("parent 9")).toBeNull();
    expect(screen.getByRole("button", { name: "s1_ker4" })).toBeInTheDocument();
  });

  it("says when the parent run is not in the list", () => {
    page(run({ parentRunId: 99 }));
    expect(screen.getByText("parent run not found")).toBeInTheDocument();
  });

  it("an imported run says so and has no log block", () => {
    page(run());
    expect(screen.getByText("imported")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Log/ })).toBeNull();
  });

  it("a run from a job shows its state and reads the log only when the block is opened", async () => {
    const fetchMock = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/history") ? { history: [] } : { log: "step 1 done" }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    page(run({ jobId: 7 }), { job });
    expect(screen.getByText("running · job 48235")).toBeInTheDocument();
    const calls = () => fetchMock.mock.calls.filter((c) => String((c as unknown[])[0]).endsWith("/log")).length;
    expect(calls()).toBe(0);
    await userEvent.setup().click(screen.getByRole("button", { name: /Log · job 48235/ }));
    expect(await screen.findByText("step 1 done")).toBeInTheDocument();
    expect(calls()).toBe(1);
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
  });

  it("opening a run from the runs list shows the run page", async () => {
    localStorage.clear();
    const m = { key: "am", value: 0.3, unit: "", uncertainty: null, uncertaintyKind: "" as const, n: null, definition: "", status: "declared" as const };
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const body = url === "/api/runs" ? { runs: [run({ metrics: [m] })] } : url === "/api/jobs" ? { jobs: [] } : url === "/api/datasets" ? { datasets: [] } : url === "/api/hosts" ? { hosts: [] } : url.endsWith("/history") ? { history: [] } : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    const user = userEvent.setup();
    render(<Shell />);
    await user.click(await screen.findByText("s1_ker8"));
    await waitFor(() => expect(screen.getByRole("article", { name: "Run s1_ker8" })).toBeInTheDocument());
    expect(screen.getByRole("button", { name: /Result · Orientation/ })).toBeInTheDocument();
    expect(screen.getByText(/As declared by the run/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.queryByRole("article", { name: "Run s1_ker8" })).toBeNull();
  });
});

describe("assessment", () => {
  it("starts as not assessed and says finishing is not a scientific verdict", () => {
    page(run());
    expect(screen.getByLabelText("Scientific QC")).toHaveValue("not_assessed");
    expect(screen.getByLabelText("Interpretation")).toHaveValue("not_interpreted");
    expect(screen.getByText(/whether the science holds is a separate question/)).toBeInTheDocument();
    expect(screen.queryByText(/\bvalid\b/i)).toBeNull();
  });
});

describe("reproducibility", () => {
  it("lists what is declared and what is unresolved, and says it is not about correctness", () => {
    const facts = [
      { key: "code", label: "Code version", status: "declared" as const, detail: "Commit bcbaed4ec980 with uncommitted changes." },
      { key: "seed", label: "Random seed", status: "unresolved" as const, detail: "No seed is recorded." },
    ];
    page(run({ reproducibility: facts }));
    expect(screen.getByRole("button", { name: /What is known about rerunning this\s*\(1 unresolved\)/ })).toBeInTheDocument();
    expect(screen.getByText(/No seed is recorded/)).toBeInTheDocument();
    expect(screen.getByText(/says nothing about whether the result is right/)).toBeInTheDocument();
  });
});
