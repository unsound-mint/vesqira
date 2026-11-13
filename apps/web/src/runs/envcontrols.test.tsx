import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import type { Job } from "../api/jobs";
import type { Controls, ParentComparison as Cmp, Rerun, RerunCheck as Check, Run } from "../api/runs";
import { RunLine } from "../boards/RunLine";
import { RunPage } from "./RunPage";
import { RerunCheck } from "../jobs/RerunCheck";
import { controlLines, statedText } from "./controlsText";
import { ControlsEditor, declarationOf } from "./ControlsEditor";
import { ParentComparison } from "./ParentComparison";
import { RerunDraft, strictRefusal } from "./RerunDraft";
import { rohsaTemplate, vgtTemplate } from "../jobs/templateFixtures";

const REV = "0123456789abcdef0123456789abcdef01234567";
const stated = { basis: "declared" as const, by: "researcher" as const, at: "2026-11-11T10:00:00Z" };
const controls: Controls = {
  smoothing: { kernel: "gaussian_fwhm", width: 3.5, unit: "arcmin", note: "", ...stated },
  noise: { kind: "scalar_sigma", sigma: 0.17, sigmaUnit: "K", estimated: "robust MAD of line-free channels", note: "", ...stated },
  construction: { kind: "shifted_map_null", params: [{ key: "n_shifts", value: "1000" }, { key: "seed", value: "5" }], note: "", basis: "verified", by: "vesqira", at: "2026-11-11T10:00:00Z" },
};
const run = (over: Partial<Run> = {}): Run => ({
  jobId: 7, parentRunId: null, id: 1, name: "vgt-ker8", kind: "vgt", importedAt: "", generatedAt: "", gitCommit: REV, dirty: false,
  reproducibleFromCommit: true, elapsedSeconds: 60, params: [], diagnostics: {}, inputName: "cube.fits", inputSizeBytes: 100, inputHint: null,
  metrics: [], outputs: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over,
});
afterEach(() => vi.unstubAllGlobals());

describe("controls", () => {
  it("lists only what was stated, each block with who said so; verified is Vesqira's own run", () => {
    const lines = controlLines(controls);
    expect(lines.map((l) => l.key)).toEqual(["smoothing", "noise", "construction"]);
    expect(lines[0]!.text).toBe("Gaussian, width is the FWHM, 3.5 arcmin");
    expect(lines[1]!.text).toContain("σ = 0.17 K");
    expect(lines[1]!.text).toContain("Estimated: robust MAD of line-free channels");
    expect(lines[2]!.text).toBe("Shifted-map null (n_shifts = 1000, seed = 5)");
    expect(lines[0]!.stated).toBe("declared by the researcher on 2026-11-11, not checked");
    expect(lines[2]!.stated).toBe("verified: run by Vesqira on 2026-11-11, recorded as it ran");
    expect(statedText({ basis: "declared", by: "researcher", at: "" })).toBe("declared by the researcher, not checked");
    // nothing stated is nothing shown, and an unstated width is not a number
    expect(controlLines(null)).toEqual([]);
    expect(controlLines({ smoothing: { kernel: "boxcar", width: null, unit: "", note: "", ...stated }, noise: null, construction: null })[0]!.text).toBe("Boxcar, width not stated");
  });

  it("refuses a width without its unit and a σ without its unit before asking the server", () => {
    const base = { smooth: true, kernel: "boxcar", width: "5", unit: "", smoothNote: "", noise: false, noiseKind: "scalar_sigma", sigma: "", sigmaUnit: "", estimated: "", noiseNote: "", construct: false, constrKind: "permutation", params: "", constrNote: "" };
    expect(declarationOf(base)).toEqual({ error: "Choose the unit of the smoothing width." });
    expect(declarationOf({ ...base, width: "", unit: "pixel" })).toMatchObject({ error: expect.stringContaining("without a width") });
    expect(declarationOf({ ...base, width: "-1", unit: "pixel" })).toMatchObject({ error: expect.stringContaining("positive") });
    expect(declarationOf({ ...base, smooth: false, noise: true, sigma: "0.2" })).toEqual({ error: "Enter the unit of σ (for example K or Jy/beam)." });
    expect(declarationOf({ ...base, smooth: false, construct: true, params: "n_shifts 1000" })).toMatchObject({ error: expect.stringContaining("name = value") });
    const ok = declarationOf({ ...base, unit: "pixel", construct: true, params: "n = 1\nseed = 5" });
    expect(ok).toEqual({ body: { smoothing: { kernel: "boxcar", width: 5, unit: "pixel", note: "" }, noise: null, construction: { kind: "permutation", params: [{ key: "n", value: "1" }, { key: "seed", value: "5" }], note: "" } } });
  });

  it("states controls through the editor: the request carries no author or basis, and the stored answer is shown", async () => {
    const sent: any[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      sent.push({ url, method: init?.method, body: JSON.parse(String(init?.body)) });
      return new Response(JSON.stringify(run({ controls: { smoothing: controls.smoothing, noise: null, construction: null } })), { status: 200 });
    }));
    const saved = vi.fn();
    render(<ControlsEditor run={run()} onSaved={saved} />);
    expect(screen.getByText(/Nothing is stated about smoothing/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "State controls" }));
    await userEvent.click(screen.getByLabelText("State the smoothing"));
    await userEvent.type(screen.getByLabelText("Width"), "3.5");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Choose the unit of the smoothing width.");
    expect(sent).toHaveLength(0);
    await userEvent.selectOptions(screen.getByLabelText("Unit of the width"), "arcmin");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saved).toHaveBeenCalled());
    expect(sent[0]).toEqual({ url: "/api/runs/1/controls", method: "PUT", body: { smoothing: { kernel: "gaussian_fwhm", width: 3.5, unit: "arcmin", note: "" }, noise: null, construction: null } });
  });

  it("shows the stated controls on the reviewer's run line, with who said so", () => {
    render(<RunLine run={{ name: "vgt-ker8", kind: "vgt", role: "psi", gitCommit: "bcbaed4ec980", dirty: false, reproducibleFromCommit: true, generatedAt: "", params: [], metrics: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, controls }} />);
    const list = screen.getByRole("list", { name: "Controls" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    expect(list).toHaveTextContent("declared by the researcher");
    expect(list).toHaveTextContent("verified: run by Vesqira");
  });

  it("shows no controls block on a run line that has none (summaries frozen before they existed)", () => {
    render(<RunLine run={{ name: "r", kind: "vgt", role: "psi", gitCommit: "", dirty: false, reproducibleFromCommit: false, generatedAt: "", params: [], metrics: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" } }} />);
    expect(screen.queryByRole("list", { name: "Controls" })).not.toBeInTheDocument();
  });
});

describe("comparison with the parent run", () => {
  const cmp: Cmp = {
    parentRunId: 1, mode: "best_effort", parentObserved: true, thisObserved: true,
    facts: [
      { key: "code.commit", label: "Code commit", result: "differed", parent: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", this: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", blocking: true, why: "" },
      { key: "julia.manifest_sha256", label: "Hash of Manifest.toml in the code folder", result: "matched", parent: "ab", this: "ab", blocking: false, why: "" },
      { key: "julia.version", label: "Julia version", result: "unknown", parent: "julia version 1.10.0", this: "", blocking: false, why: "it was not observed on this side" },
    ],
  };
  it("says matched, differed and not comparable in words, and never calls a fact one side lacks matched", () => {
    render(<ParentComparison cmp={cmp} parentName="vgt-ker8" />);
    const rows = screen.getAllByRole("row");
    expect(within(rows[1]!).getByText("differed")).toBeInTheDocument();
    expect(rows[1]).toHaveTextContent("a strict rerun requires this to match");
    expect(within(rows[2]!).getByText("matched")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("not comparable")).toBeInTheDocument();
    expect(rows[3]).toHaveTextContent("it was not observed on this side");
    expect(screen.getByText("1 matched · 1 differed · 1 not comparable")).toBeInTheDocument();
    expect(screen.getByText(/Best effort rerun:/)).toBeInTheDocument();
  });

  it("says when a side left no environment record", () => {
    render(<ParentComparison cmp={{ ...cmp, mode: "", thisObserved: false }} parentName="vgt-ker8" />);
    expect(screen.getByRole("note")).toHaveTextContent("This run's job did not leave an environment record");
    expect(screen.getByText(/did not record how closely it repeats/)).toBeInTheDocument();
  });
});

describe("rerun modes in the draft form", () => {
  const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
  const rerun: Rerun = {
    parentRunId: 1, source: "job", hostId: 1, template: "vgt", name: "vgt-ker8-again", params: [], notes: [], configChanged: false, lastJob: null,
    vgt: { repoDir: "/shared/vgt", configPath: "/data/configs/a.toml", baseConfigText: "[vgt]\nker = 8\n", set: {}, cpus: 16, mem: "64G", time: "02:00:00", partition: "", account: "" },
  };
  const created = { id: 9, hostId: 1, name: "x", template: "vgt", script: "", scriptHash: "", state: "draft", detail: "", slurmId: "", workDir: "", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: 1, note: "", autoImport: true, usage: null } as Job;
  function mock() {
    const posts: any[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/job-templates") return new Response(JSON.stringify({ templates: [vgtTemplate, rohsaTemplate] }), { status: 200 });
      if (init?.method === "POST") { posts.push(JSON.parse(String(init.body))); return new Response(JSON.stringify(created), { status: 201 }); }
      if (url === "/api/boards") return new Response(JSON.stringify({ boards: [] }), { status: 200 });
      return new Response(JSON.stringify(rerun), { status: 200 });
    }));
    return posts;
  }
  const observed = (dirty: "yes" | "no" | null) => [
    { key: "code.commit", label: "Code commit", status: "verified" as const, value: REV, reason: "" },
    dirty ? { key: "code.dirty", label: "Tracked", status: "verified" as const, value: dirty, reason: "" } : { key: "code.dirty", label: "Tracked", status: "unresolved" as const, value: "", reason: "git status failed" },
  ];

  it("repeats adapted by default and sends the chosen mode; strict is offered when the parent's code is known and clean", async () => {
    const posts = mock();
    const done = vi.fn();
    render(<RerunDraft run={run({ environment: observed("no") })} hosts={[host]} onBack={() => {}} onPrepared={done} />);
    const group = await screen.findByRole("group", { name: "How closely to repeat it" });
    expect(within(group).getByLabelText(/Adapted/)).toBeChecked();
    await userEvent.click(within(group).getByLabelText(/Strict/));
    expect(within(group).getByLabelText(/Strict/)).toBeChecked();
    expect(screen.getByText(/read on the host \(read-only\) when you submit/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    await waitFor(() => expect(done).toHaveBeenCalled());
    expect(posts[0]).toMatchObject({ parentRunId: 1, rerunMode: "strict" });
  });

  it("explains why strict is not available, and keeps strict from being used with changed settings", async () => {
    mock();
    const { unmount } = render(<RerunDraft run={run({ environment: observed("yes") })} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    const group = await screen.findByRole("group", { name: "How closely to repeat it" });
    expect(within(group).getByLabelText(/Strict/)).toBeDisabled();
    expect(group).toHaveTextContent("Not available: the parent run's code had uncommitted changes");
    unmount();
    mock();
    render(<RerunDraft run={run({ environment: observed("no") })} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    await userEvent.click(await screen.findByLabelText(/Strict/));
    const ker = screen.getByLabelText(/^ker \(smoothing/);
    await userEvent.clear(ker);
    await userEvent.type(ker, "16");
    expect(screen.getByRole("alert")).toHaveTextContent(/1 is changed below\. Choose Adapted/);
    expect(screen.getByRole("button", { name: "Prepare the job" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText(/Adapted/));
    expect(screen.getByRole("button", { name: "Prepare the job" })).toBeEnabled();
  });

  it("mirrors the server's reasons for refusing strict", () => {
    expect(strictRefusal(run({ environment: observed("no") }))).toBe("");
    expect(strictRefusal(run({ environment: observed(null) }))).toMatch(/was not observed/);
    expect(strictRefusal(run({ gitCommit: "" }))).toMatch(/no code revision/);
    expect(strictRefusal(run({ gitCommit: REV + "-dirty" }))).toMatch(/uncommitted/);
    expect(strictRefusal(run({ gitCommit: "abc1234" }))).toMatch(/short revision/);
    expect(strictRefusal(run({ gitCommit: REV }))).toBe("");
  });
});

describe("rerun check on a draft", () => {
  const job = { id: 4, parentRunId: 1, template: "vgt", state: "draft" } as Job;
  const check = (over: Partial<Check>): Check => ({ mode: "strict", parentRunId: 1, codeChecked: true, differences: [], settings: [], settingsKnown: true, blocking: false, messages: [], codePin: { commit: "", basis: "", why: "The script insists on no commit." }, notes: ["Julia version, loaded modules, node and CPU are known only when the job runs; they are compared on the run page afterwards."], ...over });
  const serve = (c: Check | number) => vi.stubGlobal("fetch", vi.fn(async () => (typeof c === "number" ? new Response(JSON.stringify({ error: "this job does not repeat a run" }), { status: c }) : new Response(JSON.stringify(c), { status: 200 }))));

  it("says a strict draft would be refused, listing what differs", async () => {
    serve(check({ blocking: true, messages: ["the code folder is at 123456789abc, the parent run used 0123456789ab"], differences: [{ key: "code.commit", label: "Code commit", result: "differed", parent: REV, this: "123456789abcdef", blocking: true, why: "" }] }));
    render(<RerunCheck job={job} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/refused now, and nothing would be sent/);
    expect(screen.getByRole("alert")).toHaveTextContent("the code folder is at 123456789abc, the parent run used 0123456789ab");
    expect(screen.getByText("differs")).toBeInTheDocument();
  });

  it("marks the settings that changed for an adapted draft, and says when the parent's settings are not known", async () => {
    serve(check({ mode: "adapted", codeChecked: false, settings: [{ key: "vgt.ker", parent: "8", now: "16" }] }));
    const { unmount } = render(<RerunCheck job={job} />);
    expect(await screen.findByText("1 setting changed from the parent run:")).toBeInTheDocument();
    expect(screen.getByText("vgt.ker").closest("li")).toHaveTextContent("vgt.ker: 8 to 16");
    unmount();
    serve(check({ mode: "adapted", codeChecked: false, settingsKnown: false }));
    render(<RerunCheck job={job} />);
    await screen.findByLabelText("Rerun check");
    expect(screen.queryByText(/changed from the parent run/)).not.toBeInTheDocument();
    expect(screen.queryByText("No setting changed from the parent run.")).not.toBeInTheDocument();
  });

  it("lists each difference as a warning for best effort, and shows nothing for a draft that repeats no run", async () => {
    serve(check({ mode: "best_effort", messages: ["Hash of Manifest.toml in the code folder differs: parent aa, now bb"] }));
    const { unmount } = render(<RerunCheck job={job} />);
    expect(await screen.findByRole("note")).toHaveTextContent(/Differences from the parent run \(the job still runs\)/);
    unmount();
    serve(409);
    render(<RerunCheck job={job} />);
    await waitFor(() => expect(screen.getByText(/rerun check is not available/)).toBeInTheDocument());
  });
});

describe("run page", () => {
  it("shows what a rerun observed against its parent and the facts the job verified, from the single-run answer", async () => {
    const parent = run({ id: 1, name: "parent" });
    const child = run({ id: 2, name: "child", parentRunId: 1 });
    const detail: Run = {
      ...child, rerunMode: "strict",
      environment: [{ key: "code.commit", label: "Code commit", status: "verified", value: REV, reason: "" }],
      reproducibility: [{ key: "code", label: "Code version", status: "verified", detail: "Commit 0123456789ab, no tracked file changed. Observed by the job itself when it ran." }],
      parentComparison: { parentRunId: 1, mode: "strict", parentObserved: true, thisObserved: true, facts: [{ key: "code.commit", label: "Code commit", result: "matched", parent: REV, this: REV, blocking: false, why: "" }] },
    };
    vi.stubGlobal("fetch", vi.fn(async (url: string) => (url === "/api/runs/2" ? new Response(JSON.stringify(detail), { status: 200 }) : new Response("{}", { status: 404 }))));
    render(<RunPage run={child} runs={[parent, child]} datasets={[]} job={null} onBack={() => {}} onOpenRun={() => {}} onOpenInViewer={() => {}} />);
    expect(await screen.findByRole("button", { name: /Compared with the parent run/ })).toHaveTextContent("strict");
    expect(screen.getByText(/Strict rerun:/)).toBeInTheDocument();
    expect(screen.getByText("matched")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /What is known about rerunning this/ })).toBeInTheDocument();
  });

  it("works without the single-run answer, shows no comparison for a run that repeats nothing, and offers to state controls", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 500 })));
    render(<RunPage run={run()} runs={[run()]} datasets={[]} job={null} onBack={() => {}} onOpenRun={() => {}} onOpenInViewer={() => {}} />);
    expect(screen.queryByRole("button", { name: /Compared with the parent run/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Controls/ })).toHaveTextContent("not stated");
  });

  it("offers Run again with… for a ROHSA run as well as a VGT run", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 500 })));
    for (const kind of ["vgt", "rohsa"] as const) {
      const r = run({ kind });
      const { unmount } = render(<RunPage run={r} runs={[r]} datasets={[]} job={null} onBack={() => {}} onOpenRun={() => {}} onOpenInViewer={() => {}} onRunAgain={() => {}} />);
      expect(screen.getByRole("button", { name: "Run again with…" })).toBeInTheDocument();
      unmount();
    }
  });
});
