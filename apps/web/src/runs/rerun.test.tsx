import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import type { Job } from "../api/jobs";
import type { Rerun, Run } from "../api/runs";
import { RunsUsingFile } from "../catalog/RunPanel";
import { RerunDraft, duration } from "./RerunDraft";

const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
const run = (over: Partial<Run> = {}): Run => ({
  jobId: 7, parentRunId: null, id: 1, name: "vgt-ker8", kind: "vgt", importedAt: "", generatedAt: "", gitCommit: "bcbaed4ec9800ef9", dirty: true,
  reproducibleFromCommit: false, elapsedSeconds: 6360, params: [], diagnostics: {}, inputName: "lmc_hi_cube.fits", inputSizeBytes: 4007885760, inputHint: null,
  metrics: [], outputs: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over,
});
const baseText = '[vgt]\nker = 8\ndn = 10\nnoise = 0.37\nnoise_mode = "scalar"\n';
const fromJob: Rerun = {
  parentRunId: 1, source: "job", hostId: 1, template: "vgt", name: "vgt-ker8-again", params: [], notes: [], configChanged: false,
  vgt: { repoDir: "/shared/vgt", configPath: "/data/configs/a.toml", baseConfigText: baseText, set: { dn: "12" }, cpus: 16, mem: "64G", time: "02:00:00", partition: "", account: "" },
};
const imported: Rerun = {
  parentRunId: 2, source: "imported", hostId: null, template: "vgt", name: "run-b-again", configChanged: null,
  notes: ["This run was imported: how it was started is not known."],
  params: [{ key: "ker", value: "4", source: "config" }],
  vgt: { repoDir: null, configPath: null, baseConfigText: null, set: {}, cpus: null, mem: null, time: null, partition: null, account: null },
};
const created: Job = { id: 9, hostId: 1, name: "x", template: "vgt", script: "", scriptHash: "", state: "draft", detail: "", slurmId: "", workDir: "", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: 1, note: "", autoImport: true };

const boardList = { boards: [{ id: 5, title: "LMC paper", createdAt: "", updatedAt: "", items: [] }] };
function mockFetch(rerun: Rerun, addFails = false) {
  const posts: { url: string; body: any }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      posts.push({ url, body: JSON.parse(String(init.body)) });
      if (url.startsWith("/api/boards/") && addFails) return new Response(JSON.stringify({ error: "board is locked" }), { status: 409 });
      return new Response(JSON.stringify(url.startsWith("/api/boards/") ? boardList.boards[0] : created), { status: url.startsWith("/api/boards/") ? 200 : 201 });
    }
    if (url === "/api/boards") return new Response(JSON.stringify(boardList), { status: 200 });
    return new Response(JSON.stringify(rerun), { status: 200 });
  }));
  return posts;
}
afterEach(() => vi.unstubAllGlobals());

describe("run again with…", () => {
  it("prefills from the job's request, marks only the changed setting, and sends the parent's text with the merged settings", async () => {
    const posts = mockFetch(fromJob);
    const done = vi.fn();
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={done} />);
    const ker = await screen.findByLabelText(/^ker \(smoothing/);
    expect(ker).toHaveValue("8");
    expect(screen.getByLabelText(/^dn \(window/)).toHaveValue("12"); // the parent's own override wins over the text
    expect(screen.queryByText(/changed · was/)).not.toBeInTheDocument();
    expect(screen.getByText("Nothing is submitted until you press Submit on the next screen.")).toBeInTheDocument();
    expect(screen.getByText("The last run took 1 h 46 min.")).toBeInTheDocument();
    await userEvent.clear(ker);
    await userEvent.type(ker, "16");
    expect(screen.getByText(/changed · was/)).toHaveTextContent("changed · was 8");
    await userEvent.type(screen.getByLabelText(/Why/), "wider smoothing");
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    await waitFor(() => expect(done).toHaveBeenCalledWith(created));
    expect(posts).toHaveLength(1);
    expect(posts[0]!.url).toBe("/api/jobs");
    expect(posts[0]!.body).toMatchObject({ hostId: 1, template: "vgt", parentRunId: 1, note: "wider smoothing", vgt: { repoDir: "/shared/vgt", configPath: "/data/configs/a.toml", configText: baseText, set: { dn: "12", ker: "16" }, cpus: 16, mem: "64G", time: "02:00:00" } });
  });

  it("warns when the config file on the host changed, and when that could not be checked", async () => {
    mockFetch({ ...fromJob, configChanged: true });
    const { unmount } = render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/config file on the host has changed since this run/);
    unmount();
    mockFetch({ ...fromJob, configChanged: null });
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    expect(await screen.findByText(/Could not check whether the config file on the host changed/)).toBeInTheDocument();
  });

  it("guesses nothing for an imported run: host, folder and config stay empty and required, declared values are labelled", async () => {
    const posts = mockFetch(imported);
    render(<RerunDraft run={run({ id: 2, name: "run-b", jobId: null })} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    expect(await screen.findByText(/how it was started is not known/)).toBeInTheDocument();
    expect(screen.getByLabelText("Host")).toHaveValue("");
    expect(screen.getByLabelText("VGT folder on the host")).toHaveValue("");
    expect(screen.getByLabelText("Config on the host")).toHaveValue("");
    expect(screen.getByLabelText("CPUs")).toHaveValue("");
    expect(screen.getByLabelText(/^ker \(smoothing/)).toHaveValue("4");
    expect(screen.getAllByText("declared by the run")[0]).toBeInTheDocument();
    const prepare = screen.getByRole("button", { name: "Prepare the job" });
    expect(prepare).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Host"), "Cluster");
    await userEvent.type(screen.getByLabelText("VGT folder on the host"), "/shared/vgt");
    await userEvent.type(screen.getByLabelText("Config on the host"), "/data/c.toml");
    await userEvent.type(screen.getByLabelText("CPUs"), "4");
    await userEvent.type(screen.getByLabelText("Memory"), "8G");
    await userEvent.type(screen.getByLabelText("Time limit"), "01:00:00");
    expect(prepare).toBeEnabled();
    await userEvent.click(prepare);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.body.vgt.configText).toBeUndefined(); // the file on the host is read, no stored text exists
    expect(posts[0]!.body.vgt.set).toEqual({ ker: "4" });
  });

  it("shows the API message when the draft cannot be prepared", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_u: string, init?: RequestInit) =>
      init?.method === "POST" ? new Response(JSON.stringify({ error: "the note is at most 500 characters" }), { status: 422 }) : new Response(JSON.stringify(_u === "/api/boards" ? boardList : fromJob), { status: 200 })));
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "Prepare the job" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the note is at most 500 characters");
  });

  it("does not touch a board unless one is chosen", async () => {
    const posts = mockFetch(fromJob);
    const done = vi.fn();
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={done} />);
    expect(await screen.findByLabelText("Add to a board (optional)")).toHaveValue("");
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    await waitFor(() => expect(done).toHaveBeenCalledWith(created));
    expect(posts.map((p) => p.url)).toEqual(["/api/jobs"]);
  });

  it("adds the new draft to the chosen board and names it", async () => {
    const posts = mockFetch(fromJob);
    const done = vi.fn();
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={done} />);
    await userEvent.selectOptions(await screen.findByLabelText("Add to a board (optional)"), "LMC paper");
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    expect(await screen.findByRole("status")).toHaveTextContent("The draft was added to the board LMC paper.");
    expect(posts.map((p) => p.url)).toEqual(["/api/jobs", "/api/boards/5/items"]);
    expect(posts[1]!.body).toEqual({ jobId: 9 });
    expect(done).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Open the job" }));
    expect(done).toHaveBeenCalledWith(created);
  });

  it("says the draft exists when it could not be added to the board, and retries only the add", async () => {
    const posts = mockFetch(fromJob, true);
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    await userEvent.selectOptions(await screen.findByLabelText("Add to a board (optional)"), "LMC paper");
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The draft was created but was not added to the board LMC paper: board is locked");
    await userEvent.click(screen.getByRole("button", { name: "Try adding again" }));
    await waitFor(() => expect(posts.filter((p) => p.url === "/api/boards/5/items")).toHaveLength(2));
    expect(posts.filter((p) => p.url === "/api/jobs")).toHaveLength(1);
  });

  it("formats durations", () => {
    expect(duration(45)).toBe("45 s");
    expect(duration(720)).toBe("12 min");
    expect(duration(6360)).toBe("1 h 46 min");
  });
});

describe("runs that used a file like this", () => {
  it("lists every candidate, says the match is not verified, and picks none", async () => {
    const hit = { datasetId: 3, basis: "name and size" };
    const runs = [run({ id: 1, name: "a", inputHint: hit }), run({ id: 2, name: "b", inputHint: hit }), run({ id: 3, name: "c", inputHint: { datasetId: 4, basis: "x" } }), run({ id: 4, name: "d", kind: "other", inputHint: hit })];
    const again = vi.fn();
    render(<RunsUsingFile runs={runs} datasetId={3} onRunAgain={again} />);
    expect(screen.getAllByText("matched by name and size, not verified")).toHaveLength(2);
    expect(screen.queryByText("c")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Run again with… b" }));
    expect(again).toHaveBeenCalledWith(2);
  });
});
