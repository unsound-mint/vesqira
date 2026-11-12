import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset } from "../api/catalog";
import type { Job } from "../api/jobs";
import type { Run, RerunOnInputSummary } from "../api/runs";
import { evaluateRuns } from "../catalog/runQuery";
import { RerunOnCube, RerunSummary, SameConfiguration } from "./RerunOnCube";

const run = (over: Partial<Run> = {}): Run => ({
  jobId: 7, parentRunId: null, id: 1, name: "vgt-ker6", kind: "vgt", importedAt: "", generatedAt: "", gitCommit: "bcbaed4ec9800ef9", dirty: false,
  reproducibleFromCommit: true, elapsedSeconds: 600, params: [], diagnostics: {}, inputName: "old.fits", inputSizeBytes: 100, inputHint: { datasetId: 10, basis: "inferred" },
  metrics: [], outputs: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over,
});
const job = { id: 7, hostId: 1, name: "vgt-ker6", template: "vgt" } as Job;
const ds = (id: number, name: string, over: Partial<Dataset> = {}) => ({ id, name, hostId: 1, sizeBytes: 4096, kind: "fits", archived: false, ...over }) as unknown as Dataset;
const datasets = [
  ds(10, "old.fits"), // the run's own input
  ds(11, "new.fits"),
  ds(12, "elsewhere.fits", { hostId: 2 }),
  ds(13, "laptop.fits", { hostId: 0 }),
  ds(14, "stars.csv", { kind: "catalog" }),
  ds(15, "gone.fits", { archived: true }),
];

const hash = "c".repeat(64);
const summary: RerunOnInputSummary = {
  sourceRunId: 1, sourceRunName: "vgt-ker6", sourceJobId: 7, template: "vgt", hostId: 1, mode: "best_effort", name: "vgt-ker6-on-new",
  sourceConfigHash: hash, configHash: hash,
  input: { datasetId: 11, name: "new.fits", hostId: 1, path: "/data/new.fits", sizeBytes: 4096, modifiedAt: 1700000000, sampledFp: "a".repeat(64), fullSha256: "" },
  previousInput: "/data/old.fits",
  changed: [{ key: "input", parent: "/data/old.fits", now: "/data/new.fits" }],
  carried: [{ key: "vgt.dn", value: "20" }, { key: "vgt.ker", value: "6" }, { key: "resources.cpus", value: "8" }],
  sourceEditedLines: 0,
  notes: ["The pixel scale differs: the previous input has 36 x 36 arcsec per pixel, the new cube 18 x 18 arcsec."],
};
const drafted = { id: 9, hostId: 1, name: "vgt-ker6-on-new", template: "vgt", state: "draft" } as Job;

afterEach(() => vi.unstubAllGlobals());

describe("the draft summary", () => {
  it("says what changed, what was carried, what the input is and what could not be known", () => {
    render(<RerunSummary summary={summary} />);
    expect(screen.getByText("Input cube")).toBeInTheDocument();
    expect(screen.getByText("/data/old.fits")).toBeInTheDocument();
    expect(screen.getAllByText("/data/new.fits")).toHaveLength(2); // in the change and in the identity
    expect(screen.getByText(/Carried over unchanged \(3 settings/)).toBeInTheDocument();
    expect(screen.getByText("vgt.dn")).toBeInTheDocument();
    // a sampled fingerprint is not called a hash of the file, and a missing full hash says the job will take it
    expect(screen.getByText(/not of the whole file/)).toBeInTheDocument();
    expect(screen.getByText(/the job hashes the file itself before it starts/)).toBeInTheDocument();
    expect(screen.getByText(/The pixel scale differs/)).toBeInTheDocument();
    expect(screen.getByText("cccccccccccc")).toBeInTheDocument();
  });
});

describe("run this configuration on another cube", () => {
  it("offers only cubes on the run's host, shows the check, and makes the draft", async () => {
    const posts: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push({ url, body: JSON.parse(String(init.body)) });
        return new Response(JSON.stringify({ job: drafted, summary }), { status: 201 });
      }
      expect(url).toBe("/api/runs/1/rerun-on-input?datasetId=11");
      return new Response(JSON.stringify(summary), { status: 200 });
    }));
    const done = vi.fn();
    render(<RerunOnCube run={run()} job={job} datasets={datasets} onDrafted={done} />);
    const select = screen.getByLabelText("Cube on the same host");
    const options = Array.from(select.querySelectorAll("option")).map((o) => o.textContent);
    expect(options).toEqual(["Choose a cube…", "new.fits (4.00 KiB)"]); // not the run's own input, another host, this computer, a catalog or an archived file
    expect(screen.queryByRole("button", { name: "Make the draft job" })).not.toBeInTheDocument();
    await userEvent.selectOptions(select, "11");
    await screen.findByText(/Carried over unchanged/);
    await userEvent.click(screen.getByRole("button", { name: "Make the draft job" }));
    await waitFor(() => expect(done).toHaveBeenCalledWith(drafted));
    expect(posts).toEqual([{ url: "/api/runs/1/rerun-on-input", body: { datasetId: 11 } }]);
  });

  it("shows the server's sentence when the settings do not fit the cube and offers no draft", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "the recorded settings do not fit the chosen cube (new.fits): dn is 400 pixels and the new cube is 300 x 300 pixels on the sky" }), { status: 422 })));
    render(<RerunOnCube run={run()} job={job} datasets={datasets} onDrafted={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Cube on the same host"), "11");
    expect(await screen.findByRole("alert")).toHaveTextContent("Not possible: the recorded settings do not fit the chosen cube (new.fits): dn is 400 pixels");
    expect(screen.queryByRole("button", { name: "Make the draft job" })).not.toBeInTheDocument();
  });

  it("says so when no other cube is on the host", () => {
    render(<RerunOnCube run={run()} job={job} datasets={[ds(10, "old.fits")]} onDrafted={() => {}} />);
    expect(screen.getByText(/No other cube is registered on this run's host/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Cube on the same host")).not.toBeInTheDocument();
  });
});

describe("a run made on another cube", () => {
  it("links to its source, lists the input identity and the other runs of the configuration", async () => {
    const open = vi.fn();
    const record = { sourceRunId: 1, sourceConfigHash: hash, configHash: hash, sameConfiguration: true, previousInput: "/data/old.fits", notes: [], input: { ...summary.input, fullSha256: "d".repeat(64) } };
    render(<SameConfiguration record={record} sourceName="vgt-ker6" others={[run({ id: 5, name: "third" })]} fullHashObserved onOpenRun={open} />);
    expect(screen.getByText(/The same configuration as/)).toBeInTheDocument();
    expect(screen.getByText(/The job hashed the input itself when it ran/)).toBeInTheDocument();
    expect(screen.getByText("dddddddddddd")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "vgt-ker6" }));
    expect(open).toHaveBeenCalledWith(1);
    await userEvent.click(screen.getByRole("button", { name: "third" }));
    expect(open).toHaveBeenCalledWith(5);
  });

  it("does not call a run the same configuration when the hashes disagree", () => {
    const record = { sourceRunId: 1, sourceConfigHash: hash, configHash: "e".repeat(64), sameConfiguration: false, previousInput: "", notes: [], input: summary.input };
    render(<SameConfiguration record={record} sourceName={null} others={[]} fullHashObserved={false} onOpenRun={() => {}} />);
    expect(screen.getByText(/did not report a SHA-256 of the input/)).toBeInTheDocument();
    expect(screen.getByText(/no longer hash to the same configuration/)).toBeInTheDocument();
    expect(screen.queryByText(/The same configuration as/)).not.toBeInTheDocument();
  });
});

describe("config: in the run query", () => {
  it("groups the runs of one configuration and leaves out runs that kept none", () => {
    const a = run({ id: 1, name: "a", configHash: hash });
    const b = run({ id: 2, name: "b", configHash: hash });
    const c = run({ id: 3, name: "c", configHash: "f".repeat(64) });
    const d = run({ id: 4, name: "d", configHash: null });
    const ev = evaluateRuns([a, b, c, d], "config:cccccc", () => ({ status: "", host: "" }));
    expect(ev.matched.map((r) => r.name)).toEqual(["a", "b"]);
    expect(ev.notRecorded.map((x) => x.run.name)).toEqual(["d"]);
  });
});
