import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Job } from "../api/jobs";
import type { Sweep, SweepGrid as Grid, SweepReview } from "../api/sweeps";
import { parseValues } from "../api/sweeps";
import { SweepForm } from "./SweepForm";
import { SweepGrid, cell, sortRows } from "./SweepGrid";
import { SubmitDialog } from "./SweepView";

const base = { id: 3, hostId: 1, name: "vgt-base", template: "vgt", state: "completed" } as Job;
const totals = { jobs: 4, cpusEach: 8, cpusTotal: 32, memBytesEach: 16 * 2 ** 30, memBytesTotal: 64 * 2 ** 30, timeLimitSecondsEach: 7200, timeLimitSecondsTotal: 28800, cpuSecondsAtLimit: 0 };

function mock(routes: Record<string, (init?: RequestInit) => unknown>) {
  const calls: { url: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    return new Response(JSON.stringify(key ? routes[key]!(init) : {}), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("sweep form", () => {
  it("reads a list of values as typed", () => {
    expect(parseValues("8, 12 ,16,,")).toEqual(["8", "12", "16"]);
  });

  it("counts the jobs and what they request before creating anything, and creates drafts only after that", async () => {
    const created = { id: 9, name: "vgt-sweep", jobs: [], counts: {}, waitingOn: [], pending: 0, state: "draft" } as unknown as Sweep;
    const calls = mock({
      "/api/sweeps/preview": () => ({ count: 4, max: 64, baseJob: { id: 3, name: "vgt-base", hostId: 1 }, jobs: [{ name: "a", label: "dn = 8, κ = 2", values: {} }], totals, estimate: { baseJobId: 3, baseJobName: "vgt-base", elapsedSeconds: 600, maxRssBytes: null, totalCpuSeconds: null, basis: "a guide" } }),
      "/api/sweeps": () => created,
    });
    const onCreated = vi.fn();
    render(<SweepForm jobs={[base]} onCreated={onCreated} />);
    const create = screen.getByRole("button", { name: /Create/ });
    expect(create).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Values/), "8, 16");
    await userEvent.click(screen.getByRole("button", { name: "Count the jobs" }));
    expect(await screen.findByText(/4 jobs/)).toBeInTheDocument();
    expect(screen.getByText(/32 CPUs, 64 GiB/)).toBeInTheDocument();
    expect(screen.getByText(/40 min/)).toBeInTheDocument(); // 4 x the base's 10 minutes, as a guide
    expect(calls.filter((c) => c.url === "/api/sweeps")).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: /Create 4 drafts/ }));
    expect(onCreated).toHaveBeenCalledWith(created);
    expect(calls.find((c) => c.url === "/api/sweeps")?.body).toMatchObject({ baseJobId: 3, params: [{ key: "dn", values: ["8", "16"] }] });
  });

  it("drops the count when a value changes, so what is created is what was counted", async () => {
    mock({ "/api/sweeps/preview": () => ({ count: 2, max: 64, baseJob: { id: 3, name: "b", hostId: 1 }, jobs: [], totals, estimate: null }) });
    render(<SweepForm jobs={[base]} onCreated={() => {}} />);
    await userEvent.type(screen.getByLabelText(/Values/), "8, 16");
    await userEvent.click(screen.getByRole("button", { name: "Count the jobs" }));
    expect(await screen.findByText(/no estimate is offered/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/Values/), ", 24");
    expect(screen.queryByText(/no estimate is offered/)).toBeNull();
    expect(screen.getByRole("button", { name: /Create/ })).toBeDisabled();
  });

  it("allows at most three parameters", async () => {
    render(<SweepForm jobs={[base]} onCreated={() => {}} />);
    const add = screen.getByRole("button", { name: "Add a parameter" });
    await userEvent.click(add);
    await userEvent.click(add);
    expect(add).toBeDisabled();
  });

  it("says Count before Create inline, and the Create button reads cleanly before and after counting", async () => {
    mock({ "/api/sweeps/preview": () => ({ count: 4, max: 64, baseJob: { id: 3, name: "b", hostId: 1 }, jobs: [], totals, estimate: null }) });
    render(<SweepForm jobs={[base]} onCreated={() => {}} />);
    expect(screen.getByText(/First count the jobs/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create drafts" }).textContent).not.toMatch(/\s\s/);
    await userEvent.type(screen.getByLabelText(/Values/), "8, 16");
    const count = screen.getByRole("button", { name: "Count the jobs" });
    await userEvent.click(count);
    const create = await screen.findByRole("button", { name: "Create 4 drafts" });
    // the explanation of what will be made sits between the two buttons
    expect(count.compareDocumentPosition(screen.getByText(/4 jobs/)) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText(/4 jobs/).compareDocumentPosition(create) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

const review: SweepReview = {
  sweep: { id: 9, name: "s" } as Sweep, baseJobId: 3, baseName: "vgt-base", baseScript: "#!/bin/bash\n", baseEditedLines: 0, keys: ["dn"], sweepHash: "h".repeat(64), totals, estimate: null, maxConcurrency: 16,
  template: "vgt", defaultConcurrency: 1, concurrencyNote: "",
  jobs: [{ jobId: 10, name: "s-dn8", label: "dn = 8", values: { dn: "8" }, scriptHash: "a".repeat(64), script: "#!/bin/bash\ndn = 8\n", unexpected: 0, requested: { cpus: 8, memBytes: 0, timeSeconds: 0 }, sizing: null,
    diff: [{ op: "-", kind: "parameter", text: "dn = 20" }, { op: "+", kind: "parameter", text: "dn = 8" }, { op: "+", kind: "output", text: 'output_dir = "/r/x"' }] }],
};

describe("submit dialog", () => {
  it("shows only the differing lines with why, the total request, defaults to one at a time, and submits by the hash it showed", async () => {
    const calls = mock({ "/api/sweeps/9/review": () => review, "/api/sweeps/9/submit": () => ({ id: 9, state: "running" }) });
    const onSubmitted = vi.fn();
    render(<SubmitDialog sweep={{ id: 9, name: "s" } as Sweep} onClose={() => {}} onSubmitted={onSubmitted} />);
    expect(await screen.findByText(/No other line differs/)).toBeInTheDocument();
    expect(screen.getByText(/32 CPUs, 64 GiB/)).toBeInTheDocument();
    const pre = screen.getByLabelText("Differences for dn = 8");
    expect(pre.textContent).toContain("- dn = 20");
    expect(pre.textContent).toContain("+ dn = 8");
    expect(pre.textContent).toContain("output folder (set per job)");
    expect(screen.getByLabelText(/Jobs at the same time/)).toHaveValue("1");
    expect(screen.getByText(/one after another/)).toBeInTheDocument();
    expect(calls.filter((c) => c.url.endsWith("/submit"))).toHaveLength(0); // reading submits nothing
    await userEvent.click(screen.getByRole("button", { name: /Submit 1 jobs, 1 at a time/ }));
    expect(onSubmitted).toHaveBeenCalled();
    expect(calls.find((c) => c.url.endsWith("/submit"))?.body).toEqual({ sweepHash: review.sweepHash, concurrency: 1, stopOnFailure: false });
  });

  it("warns about a line that is not expected and refuses an invalid limit", async () => {
    mock({ "/api/sweeps/9/review": () => ({ ...review, jobs: [{ ...review.jobs[0]!, unexpected: 1, diff: [{ op: "+", kind: "other", text: "rm -rf" }] }] }) });
    render(<SubmitDialog sweep={{ id: 9, name: "s" } as Sweep} onClose={() => {}} onSubmitted={() => {}} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/1 differing lines are not/);
    const limit = screen.getByLabelText(/Jobs at the same time/);
    await userEvent.clear(limit);
    await userEvent.type(limit, "0");
    expect(screen.getByRole("button", { name: /Submit/ })).toBeDisabled();
    await userEvent.clear(limit);
    await userEvent.type(limit, "2");
    expect(screen.getByRole("button", { name: /2 at a time/ })).toBeEnabled();
  });
});

const m = (key: string, value: number | null, unit = "K") => ({ key, value, unit, uncertainty: null, definition: "d" });
const grid: Grid = {
  sweepId: 9, name: "s", plotNote: "", plot: null,
  params: [{ key: "N", label: "", values: ["8", "12"] }, { key: "lambda", label: "λ", values: ["3", "10"] }],
  metrics: [{ key: "rms", unit: "K", units: ["K"], unitsDiffer: false, definitionsDiffer: false, runs: 3, values: 2 }],
  rows: [
    { jobId: 1, name: "a", label: "N = 8, λ = 3", values: { N: "8", lambda: "3" }, state: "completed", runId: 1, runName: "r1", metrics: [m("rms", 0.5)] },
    { jobId: 2, name: "b", label: "N = 8, λ = 10", values: { N: "8", lambda: "10" }, state: "completed", runId: 2, runName: "r2", metrics: [m("rms", null)] },
    { jobId: 3, name: "c", label: "N = 12, λ = 3", values: { N: "12", lambda: "3" }, state: "completed", runId: 3, runName: "r3", metrics: [m("rms", 0.4)] },
    { jobId: 4, name: "d", label: "N = 12, λ = 10", values: { N: "12", lambda: "10" }, state: "submitted", runId: null, runName: "", metrics: [] },
  ],
};

describe("sweep grid", () => {
  it("shows declared values with their unit, 'not recorded' for a null and nothing for a job without a run, never zero", () => {
    expect(cell(grid.rows[0]!, "rms")).toBe("0.5 K");
    expect(cell(grid.rows[1]!, "rms")).toBe("not recorded");
    expect(cell(grid.rows[3]!, "rms")).toBe("—");
  });

  it("sorts by a parameter numerically and by a metric with missing values last", () => {
    expect(sortRows(grid.rows, { kind: "param", key: "N" }, -1).map((r) => r.jobId)).toEqual([3, 4, 1, 2]);
    expect(sortRows(grid.rows, { kind: "metric", key: "rms" }, 1).map((r) => r.jobId)).toEqual([3, 1, 2, 4]);
    expect(sortRows(grid.rows, { kind: "metric", key: "rms" }, -1).map((r) => r.jobId)).toEqual([1, 3, 2, 4]);
  });

  it("renders the table in parameter order with sortable columns and names no best run", async () => {
    mock({ "/api/sweeps/9/grid": () => grid });
    const { container } = render(<SweepGrid sweepId={9} />);
    const table = await screen.findByRole("table");
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("a");
    await userEvent.click(within(table).getByRole("button", { name: /^N/ }));
    expect(within(table).getByRole("columnheader", { name: /^N/ })).toHaveAttribute("aria-sort", "ascending");
    await userEvent.click(within(table).getByRole("button", { name: /^N/ }));
    expect(within(table).getByRole("columnheader", { name: /^N/ })).toHaveAttribute("aria-sort", "descending");
    expect(within(table).getByRole("columnheader", { name: /λ/ })).toBeInTheDocument();
    expect(container.textContent?.toLowerCase()).not.toContain("best");
  });

  it("draws one line per combination of the other parameters, breaks at a missing value and never draws it as zero", async () => {
    const plot = {
      metric: "rms", unit: "K", xKey: "N", xName: "N", numeric: true, xValues: ["8", "12"], notes: [],
      lines: [
        { label: "λ = 3", points: [{ x: "8", xNum: 8, value: 0.5, uncertainty: null, jobId: 1 }, { x: "12", xNum: 12, value: 0.4, uncertainty: null, jobId: 3 }] },
        { label: "λ = 10", points: [{ x: "8", xNum: 8, value: null, uncertainty: null, jobId: 2 }, { x: "12", xNum: 12, value: null, uncertainty: null, jobId: 4 }] },
      ],
    };
    mock({ "/api/sweeps/9/grid": () => ({ ...grid, plot }) });
    render(<SweepGrid sweepId={9} />);
    const svg = await screen.findByRole("img", { name: /rms \(K\) against N/ });
    expect(svg.querySelectorAll("polyline")).toHaveLength(1);
    expect(svg.querySelectorAll("circle, rect, polygon")).toHaveLength(2);
    expect(svg.textContent).toContain("λ = 3");
  });

  it("says why a metric with mixed units is not drawn", async () => {
    mock({ "/api/sweeps/9/grid": () => ({ ...grid, plot: null, plotNote: "the runs declare rms in different units (K, Jy/beam); they are not one quantity, so no line is drawn" }) });
    render(<SweepGrid sweepId={9} />);
    await userEvent.selectOptions(await screen.findByLabelText("Plot metric"), "rms");
    expect(await screen.findByText(/different units/)).toBeInTheDocument();
  });
});

const rohsaBase = { id: 5, hostId: 1, name: "rohsa-base", template: "rohsa", state: "completed" } as Job;
const GiB = 2 ** 30;
const sizing = (n: number, mem: string) => ({ cropSize: 2048, nGauss: n, assumed: [] as string[], cpus: 32, mem, time: "1-12:00:00", scriptPartition: "ZEN4", basis: "the sizing table of your pipeline's hpc/irgetas/submit_rohsa.sh" });
const rohsaReview: SweepReview = {
  ...review,
  template: "rohsa", baseName: "rohsa-base", keys: ["n_gauss"],
  concurrencyNote: "ROHSA jobs share one source folder on the host. A rebuild replaces the binary while another job may be starting it. Run one job at a time unless you know the binary is current.",
  jobs: [
    { ...review.jobs[0]!, jobId: 11, name: "rs-n4", label: "N = 4", values: { n_gauss: "4" }, requested: { cpus: 32, memBytes: 100 * GiB, timeSeconds: 129600 }, sizing: sizing(4, "100G"),
      diff: [{ op: "-", kind: "parameter", text: "n_gauss = [16]" }, { op: "+", kind: "parameter", text: "n_gauss = [4]" }] },
    { ...review.jobs[0]!, jobId: 12, name: "rs-n8", label: "N = 8", values: { n_gauss: "8" }, requested: { cpus: 32, memBytes: 100 * GiB, timeSeconds: 129600 }, sizing: sizing(8, "140G"),
      diff: [{ op: "-", kind: "parameter", text: "n_gauss = [16]" }, { op: "+", kind: "parameter", text: "n_gauss = [8]" }] },
  ],
};

describe("ROHSA sweeps", () => {
  it("offers the ROHSA settings with the tied lambda for a ROHSA base, and the VGT ones for a VGT base", async () => {
    mock({});
    render(<SweepForm jobs={[base, rohsaBase]} baseJobId={5} onCreated={() => {}} />);
    const setting = screen.getByLabelText("Setting 1") as HTMLSelectElement;
    const keys = Array.from(setting.options).map((o) => o.value);
    expect(keys).toEqual(expect.arrayContaining(["lambda", "n_gauss", "lb_sig", "maxiter", "noise", "crop_size"]));
    expect(keys).not.toContain("output_dir");
    expect(keys).not.toContain("input");
    expect(keys).not.toContain("dn");
    expect(setting.value).toBe("n_gauss");
    expect(screen.getByText(/one job at a time unless you raise that/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Sweep name/)).toHaveValue("rohsa-sweep");
    await userEvent.selectOptions(screen.getByLabelText(/Base job/), "3");
    expect(Array.from((screen.getByLabelText("Setting 1") as HTMLSelectElement).options).map((o) => o.value)).toContain("dn");
    expect(screen.queryByText(/one job at a time unless you raise that/)).toBeNull();
  });

  it("sends the tied lambda and the Gaussian counts as typed", async () => {
    const calls = mock({ "/api/sweeps/preview": () => ({ count: 4, max: 64, baseJob: { id: 5, name: "rohsa-base", hostId: 1 }, jobs: [], totals, estimate: null }) });
    render(<SweepForm jobs={[rohsaBase]} onCreated={() => {}} />);
    await userEvent.type(screen.getByLabelText(/Values/), "8, 12");
    await userEvent.click(screen.getByRole("button", { name: "Add a parameter" }));
    const second = screen.getAllByLabelText(/^Setting/)[1]!;
    await userEvent.selectOptions(second, "lambda");
    await userEvent.type(screen.getAllByLabelText(/Values/)[1]!, "3, 10");
    await userEvent.click(screen.getByRole("button", { name: "Count the jobs" }));
    await screen.findByText(/4 jobs/);
    expect(calls.find((c) => c.url === "/api/sweeps/preview")?.body).toMatchObject({ baseJobId: 5, params: [{ key: "n_gauss", values: ["8", "12"] }, { key: "lambda", values: ["3", "10"] }] });
  });

  it("defaults to one job at a time, says why, warns when it is raised, and shows the table's size for each draft", async () => {
    const calls = mock({ "/api/sweeps/9/review": () => rohsaReview, "/api/sweeps/9/submit": () => ({ id: 9, state: "running" }) });
    render(<SubmitDialog sweep={{ id: 9, name: "rs" } as Sweep} onClose={() => {}} onSubmitted={() => {}} />);
    expect(await screen.findByText(/A rebuild replaces the binary/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Jobs at the same time/)).toHaveValue("1");
    // the table gives 100G for N = 4 (the draft asks 100G: no warning) and 140G for N = 8 (it asks 100G: warned, in words)
    const lines = screen.getAllByText(/your sizing table suggests/);
    expect(lines).toHaveLength(2);
    expect(lines[0]).toHaveTextContent("n_gauss 4 your sizing table suggests 32 CPUs, 100G, 1-12:00:00; this draft requests 32 CPUs and 100 GiB.");
    expect(lines[0]).not.toHaveTextContent(/less memory/);
    expect(lines[1]).toHaveTextContent(/n_gauss 8.*140G.*That is less memory than the table gives for this size/);
    const alerts = screen.getAllByRole("alert");
    expect(alerts.filter((a) => /1 of 2 drafts request less memory/.test(a.textContent ?? ""))).toHaveLength(1); // said above the list, not only inside a closed draft
    expect(screen.getByText(/N = 8/, { selector: "summary" })).toHaveTextContent("asks 100 GiB, table suggests 140G");
    expect(alerts.some((a) => /replaces the binary/.test(a.textContent ?? ""))).toBe(false); // at the default limit the reason is a note, not a warning
    // raising the limit is the owner's call, with the reason put in front of them
    const limit = screen.getByLabelText(/Jobs at the same time/);
    await userEvent.clear(limit);
    await userEvent.type(limit, "2");
    expect(screen.getAllByRole("alert").some((a) => /replaces the binary/.test(a.textContent ?? ""))).toBe(true);
    await userEvent.clear(limit);
    await userEvent.type(limit, "1");
    await userEvent.click(screen.getByRole("button", { name: /Submit 2 jobs, 1 at a time/ }));
    expect(calls.find((c) => c.url.endsWith("/submit"))?.body).toEqual({ sweepHash: review.sweepHash, concurrency: 1, stopOnFailure: false });
  });

  it("takes its default limit from the server's review", async () => {
    mock({ "/api/sweeps/9/review": () => ({ ...rohsaReview, defaultConcurrency: 1, maxConcurrency: 16 }) });
    render(<SubmitDialog sweep={{ id: 9, name: "rs" } as Sweep} onClose={() => {}} onSubmitted={() => {}} />);
    expect(await screen.findByLabelText(/Jobs at the same time \(1 to 16\)/)).toHaveValue("1");
  });
});
