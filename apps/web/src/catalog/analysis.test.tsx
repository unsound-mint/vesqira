import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset } from "../api/catalog";
import { AnalysisPanel, StatsView } from "./AnalysisPanel";
import type { StatsSummary } from "../api/analysis";

const ds = (id: number, name: string, hostId = 0): Dataset => ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId, header: {} as Dataset["header"] });
const cube = ds(1, "cube.fits");

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("analysis panel", () => {
  it("sends a moment request with 0-based channels and the options the user typed", async () => {
    const calls = mockFetch({ "/api/analysis": () => ({ status: 201, body: { dataset: ds(5, "moment-1.fits"), summary: {} } }) });
    const created: Dataset[] = [];
    render(<AnalysisPanel dataset={cube} datasets={[cube]} channels={64} onCreated={(d) => created.push(d)} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Moment"), "1");
    await userEvent.type(screen.getByLabelText(/Only values at or above/), "1.5");
    await userEvent.type(screen.getByLabelText("From channel"), "10");
    await userEvent.type(screen.getByLabelText("To channel"), "50");
    await userEvent.click(screen.getByRole("button", { name: "Create the new file" }));
    await waitFor(() => expect(created).toHaveLength(1));
    expect(JSON.parse(String(calls[0]!.init!.body))).toEqual({ op: "moment", datasetId: 1, params: { order: "1", threshold: "1.5", chanStart: "9", chanEnd: "49" } });
  });

  it("explains alignment honestly and needs a reference", async () => {
    mockFetch({});
    render(<AnalysisPanel dataset={cube} datasets={[cube, ds(2, "other.fits")]} channels={64} onCreated={() => {}} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "align");
    expect(screen.getByText(/does not match angular resolution; outside the footprint is NaN/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create the new file" })).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Align onto the grid of"), "2");
    expect(screen.getByRole("button", { name: "Create the new file" })).toBeEnabled();
  });

  it("offers only files on the same machine as references, and turns a remote cube's analysis into a job", async () => {
    const calls = mockFetch({ "/api/analysis/job": () => ({ status: 201, body: { id: 9 } }) });
    const jobs: number[] = [];
    const remote = ds(3, "remote.fits", 1);
    render(<AnalysisPanel dataset={remote} datasets={[remote, ds(4, "local.fits", 0), ds(5, "alsoRemote.fits", 1)]} channels={64} onCreated={() => {}} onJob={(j) => jobs.push(j.id)} />);
    expect(screen.getByText(/becomes a Slurm job whose script you read and submit/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "align");
    const refs = Array.from((screen.getByLabelText("Align onto the grid of") as HTMLSelectElement).options).map((o) => o.text);
    expect(refs).toEqual(["Choose a file…", "alsoRemote.fits"]);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "stats");
    await userEvent.click(screen.getByRole("button", { name: "Create a job on the host" }));
    await waitFor(() => expect(jobs).toEqual([9]));
    expect(calls[0]!.url).toBe("/api/analysis/job");
  });

  it("shows statistics with the missing-data rule and counts no value separately", () => {
    const st: StatsSummary = { channels: [0, 63], finite: 4000, no_value: 576, min: -1, max: 12.6, mean: 0.01, std: 0.3, peak: { value: 12.6, channel: 40, x: 20, y: 9 }, per_channel: [{ channel: 0, finite: 60, rms: 0.2, mad_sigma: 0.2 }], policy: "NaN, BLANK and infinite values are excluded and counted; nothing is treated as zero" };
    render(<StatsView st={st} />);
    expect(screen.getByText("576")).toBeInTheDocument();
    expect(screen.getByText(/x 21, y 10, channel 41/)).toBeInTheDocument();
    expect(screen.getByText(/nothing is treated as zero/)).toBeInTheDocument();
  });
});
