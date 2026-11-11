import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import type { JobUsage } from "../api/jobs";
import type { Rerun, Run } from "../api/runs";
import { RerunDraft } from "./RerunDraft";

const GiB = 2 ** 30;
const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
const run = { jobId: 7, parentRunId: null, id: 1, name: "rohsa-lmc", kind: "vgt", importedAt: "", generatedAt: "", gitCommit: "", dirty: false, reproducibleFromCommit: false, elapsedSeconds: 22323, params: [], diagnostics: {}, inputName: "", inputSizeBytes: 0, inputHint: null, metrics: [], outputs: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" } } as unknown as Run;
const usage: JobUsage = {
  state: "COMPLETED", exitCode: "0:0", elapsedSeconds: 22323, timelimitSeconds: 86400, reqMemBytes: 64 * GiB, maxRssBytes: 79 * GiB, allocCpus: 16, totalCpuSeconds: null, cpuEfficiency: null, cpuNotAccounted: true, oomKilled: false,
  suggest: { mem: "103G", time: "09:19:00", memBytes: 103 * GiB, timeSeconds: 33540, basis: "1.3 x the measured peak memory; 1.5 x the elapsed time" },
};
const rerun = (lastJob: Rerun["lastJob"]): Rerun => ({
  parentRunId: 1, source: "job", hostId: 1, template: "vgt", name: "again", params: [], notes: [], configChanged: false, lastJob,
  vgt: { repoDir: "/shared/vgt", configPath: "/data/c.toml", baseConfigText: "[vgt]\nker = 8\n", set: {}, cpus: 16, mem: "64G", time: "02:00:00", partition: "", account: "" },
});
function mockFetch(r: Rerun) {
  const posts: { url: string; body: any }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      posts.push({ url, body: JSON.parse(String(init.body)) });
      return new Response(JSON.stringify({ id: 9 }), { status: 201 });
    }
    return new Response(JSON.stringify(url === "/api/boards" ? { boards: [] } : r), { status: 200 });
  }));
  return posts;
}
afterEach(() => vi.unstubAllGlobals());

describe("run again with… and the previous run's measured use", () => {
  it("shows measured peak memory and time beside the requested values and applies nothing until a button is clicked", async () => {
    const posts = mockFetch(rerun({ id: 7, name: "rohsa-lmc", usage }));
    render(<RerunDraft run={run} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    const hint = await screen.findByRole("region", { name: "Measured use of job 7" });
    expect(hint).toHaveTextContent("peak memory 79.0 GiB (requested 64.0 GiB)");
    expect(hint).toHaveTextContent("You are asking for 64G and 02:00:00");
    expect(hint).toHaveTextContent("Suggested from job 7’s measured use");
    expect(screen.getByLabelText("Memory")).toHaveValue("64G"); // not applied by itself
    expect(screen.getByLabelText("Time limit")).toHaveValue("02:00:00");
    expect(screen.getByText(/memory request is smaller than the 79.0 GiB that job reached/)).toBeInTheDocument();

    // preparing the job without clicking sends the values as they were
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.body.vgt).toMatchObject({ mem: "64G", time: "02:00:00" });
  });

  it("applies the suggestion when the researcher clicks, and sends it", async () => {
    const posts = mockFetch(rerun({ id: 7, name: "rohsa-lmc", usage }));
    render(<RerunDraft run={run} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "Use memory 103G" }));
    expect(screen.getByLabelText("Memory")).toHaveValue("103G");
    expect(screen.getByLabelText("Time limit")).toHaveValue("02:00:00"); // time is a separate choice
    await userEvent.click(screen.getByRole("button", { name: "Use time 09:19:00" }));
    expect(screen.getByLabelText("Time limit")).toHaveValue("09:19:00");
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.body.vgt).toMatchObject({ mem: "103G", time: "09:19:00" });
  });

  it("offers nothing for a run whose job has no recorded use (imported, or ended before usage was kept)", async () => {
    mockFetch(rerun(null));
    const { unmount } = render(<RerunDraft run={run} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    await screen.findByText("The last run took 6 h 12 min.");
    expect(screen.queryByRole("region", { name: /Measured use/ })).not.toBeInTheDocument();
    unmount();
    mockFetch(rerun({ id: 7, name: "x", usage: null }));
    render(<RerunDraft run={run} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    await screen.findByText("The last run took 6 h 12 min.");
    expect(screen.queryByRole("region", { name: /Measured use/ })).not.toBeInTheDocument();
  });
});
