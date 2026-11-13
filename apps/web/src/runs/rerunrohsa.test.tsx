import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import type { Job, RohsaSizing } from "../api/jobs";
import type { Rerun, Run } from "../api/runs";
import { RerunDraft } from "./RerunDraft";
import { rohsaTemplate, vgtTemplate } from "../jobs/templateFixtures";

const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
const run = (over: Partial<Run> = {}): Run => ({
  jobId: null, parentRunId: null, id: 1, name: "rohsa_n16", kind: "rohsa", importedAt: "", generatedAt: "", gitCommit: "", dirty: false,
  reproducibleFromCommit: false, elapsedSeconds: null, params: [], diagnostics: {}, inputName: "", inputSizeBytes: 0, inputHint: null,
  metrics: [], outputs: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over,
});
const cfgText = "[input]\ncrop_size = 2048\n[rohsa]\nn_gauss = [16]\nlambda_amp = 10.0\nnoise = true\n";
const none = { repoDir: null, configPath: null, baseConfigText: null, set: {}, cpus: null, mem: null, time: null, partition: null, account: null };
const fromJob: Rerun = {
  parentRunId: 1, source: "job", hostId: 1, template: "rohsa", name: "rohsa_n16-again", params: [], notes: [], configChanged: false, lastJob: null,
  vgt: none,
  rohsa: { repoDir: "/shared/vgt", configPath: "/data/configs/rohsa/a.toml", baseConfigText: cfgText, set: { lambda: "10" }, cpus: 32, mem: "140G", time: "1-12:00:00", partition: "", account: "" },
  settings: { n_gauss: "16", lambda_amp: "10", lambda_mu: "10", lambda_sig: "10", noise: "true", crop_size: "2048" },
};
const imported: Rerun = {
  parentRunId: 2, source: "imported", hostId: null, template: "rohsa", name: "rohsa_n3-again", params: [], configChanged: null, lastJob: null,
  notes: ["This run was imported, so the host, code folder and config it ran with are not known here.", "The settings below are those the run's trace declares for the pipeline's config."],
  vgt: none, rohsa: none, settings: { n_gauss: "3", lambda_amp: "10", lb_sig: "1" },
};
const created: Job = { id: 9, hostId: 1, name: "x", template: "rohsa", script: "", scriptHash: "", state: "draft", detail: "", slurmId: "", workDir: "", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: 1, note: "", autoImport: false, usage: null };

// the sizing route as the server answers it: the owner's table, as a function of the two numbers
function table(url: string): RohsaSizing {
  const q = new URL(url, "http://x").searchParams;
  const crop = Number(q.get("cropSize") ?? 0);
  const n = Number(q.get("nGauss") ?? 6);
  const big = crop > 1024 && crop <= 2048;
  return {
    cropSize: crop, nGauss: n, assumed: q.get("cropSize") === null ? ["crop_size (read as 0, the whole cube)"] : [], cpus: big ? 32 : 16,
    mem: big ? (n <= 6 ? "100G" : "140G") : "12G", time: big ? (n <= 6 ? "1-00:00:00" : "1-12:00:00") : "03:00:00", scriptPartition: "ZEN4",
    basis: "the sizing table of your pipeline's hpc/irgetas/submit_rohsa.sh",
  };
}
function mockFetch(rerun: Rerun, texts: Record<string, string> = {}) {
  const calls: { url: string; method: string; body: any }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : null });
    if (url === "/api/job-templates") return new Response(JSON.stringify({ templates: [vgtTemplate, rohsaTemplate] }), { status: 200 });
    if (init?.method === "POST") return new Response(JSON.stringify(created), { status: 201 });
    if (url.startsWith("/api/rohsa/sizing")) return new Response(JSON.stringify(table(url)), { status: 200 });
    if (url === "/api/boards") return new Response(JSON.stringify({ boards: [] }), { status: 200 });
    if (url.startsWith("/api/hosts/1/text")) return new Response(JSON.stringify({ text: texts[decodeURIComponent(url.split("path=")[1]!)] ?? "" }), { status: 200 });
    return new Response(JSON.stringify(rerun), { status: 200 });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("run a ROHSA run again with…", () => {
  it("starts from what the job ran with, highlights a changed N, and sends the parent's config text with the changes only", async () => {
    const calls = mockFetch(fromJob);
    const done = vi.fn();
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={done} />);
    const n = await screen.findByLabelText(/^n_gauss/);
    expect(n).toHaveValue("16");
    expect(screen.getByLabelText(/^lambda_amp/)).toHaveValue("10");
    expect(screen.getByLabelText("ROHSA config on the host")).toHaveValue("/data/configs/rohsa/a.toml");
    expect(screen.queryByText(/changed · was/)).not.toBeInTheDocument();
    expect(screen.queryByText(/the input file setting above/)).not.toBeInTheDocument(); // a VGT-only line
    await userEvent.clear(n);
    await userEvent.type(n, "12");
    expect(screen.getByText(/changed · was/)).toHaveTextContent("changed · was 16");
    expect(screen.getByText(/1 changed/)).toBeInTheDocument();
    // "10.0" typed over "10" is the same number: not a change
    const lam = screen.getByLabelText(/^lambda_amp/);
    await userEvent.clear(lam);
    await userEvent.type(lam, "10.0");
    expect(screen.getByText(/1 changed/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Prepare the job" }));
    await waitFor(() => expect(done).toHaveBeenCalledWith(created));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url).toBe("/api/jobs");
    expect(post.body).toMatchObject({ hostId: 1, template: "rohsa", parentRunId: 1, tool: { repoDir: "/shared/vgt", configPath: "/data/configs/rohsa/a.toml", configText: cfgText, set: { lambda: "10", n_gauss: "12" }, cpus: 32, mem: "140G" } });
    expect(post.body.tool.set.output_dir).toBeUndefined();
    expect(post.body.vgt).toBeUndefined();
    expect(post.body.rohsa).toBeUndefined();
  });

  it("shows your own sizing table for the crop and N, follows N, labels its source, and applies nothing by itself", async () => {
    const calls = mockFetch(fromJob);
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    const hint = await screen.findByRole("region", { name: "Suggested size from your sizing table" });
    await waitFor(() => expect(hint).toHaveTextContent("crop_size 2048 and n_gauss 16 your table suggests 32 CPUs, 140G, 1-12:00:00"));
    expect(hint).toHaveTextContent("Source: the sizing table of your pipeline's hpc/irgetas/submit_rohsa.sh");
    expect(calls.some((c) => c.url === "/api/rohsa/sizing?cropSize=2048&nGauss=16")).toBe(true);
    const n = screen.getByLabelText(/^n_gauss/);
    await userEvent.clear(n);
    await userEvent.type(n, "4");
    await waitFor(() => expect(screen.getByRole("region", { name: "Suggested size from your sizing table" })).toHaveTextContent("n_gauss 4 your table suggests 32 CPUs, 100G, 1-00:00:00"));
    expect(screen.getByLabelText("Memory")).toHaveValue("140G"); // not applied by itself
    await userEvent.click(screen.getByRole("button", { name: /Use 32 CPUs, 100G/ }));
    expect(screen.getByLabelText("Memory")).toHaveValue("100G");
    expect(screen.getByLabelText("Time limit")).toHaveValue("1-00:00:00");
  });

  it("warns when the request is smaller than the table's memory", async () => {
    mockFetch({ ...fromJob, rohsa: { ...fromJob.rohsa!, mem: "40G" } });
    render(<RerunDraft run={run()} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    expect(await screen.findByText(/smaller than the 140G the table gives/)).toBeInTheDocument();
  });

  it("guesses nothing for an imported run: only what its trace declares is filled, the crop comes from the chosen config, declared values are sent explicitly", async () => {
    const calls = mockFetch(imported, { "/data/configs/rohsa/b.toml": cfgText });
    render(<RerunDraft run={run({ id: 2, name: "rohsa_n3" })} hosts={[host]} onBack={() => {}} onPrepared={() => {}} />);
    expect(await screen.findByText(/those the run's trace declares/)).toBeInTheDocument();
    expect(screen.getByLabelText("Host")).toHaveValue("");
    expect(screen.getByLabelText(/^n_gauss/)).toHaveValue("3");
    expect(screen.getByLabelText(/^lb_sig \(/)).toHaveValue("1");
    expect(screen.getByLabelText(/^crop_size/)).toHaveValue(""); // not declared by the run; not invented
    expect(screen.getByLabelText(/^noise/)).toHaveValue("");
    const prepare = screen.getByRole("button", { name: "Prepare the job" });
    expect(prepare).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Host"), "Cluster");
    await userEvent.type(screen.getByLabelText("ROHSA-LMC folder on the host"), "/shared/vgt");
    await userEvent.type(screen.getByLabelText("ROHSA config on the host"), "/data/configs/rohsa/b.toml");
    // the crop of the chosen config sizes the request (2048, from the file), the run's own N (3) is the Gaussian count
    await waitFor(() => expect(screen.getByRole("region", { name: "Suggested size from your sizing table" })).toHaveTextContent("crop_size 2048 and n_gauss 3 your table suggests 32 CPUs, 100G"));
    await userEvent.type(screen.getByLabelText("CPUs"), "32");
    await userEvent.type(screen.getByLabelText("Memory"), "100G");
    await userEvent.type(screen.getByLabelText("Time limit"), "1-00:00:00");
    const n = screen.getByLabelText(/^n_gauss/);
    await userEvent.clear(n);
    await userEvent.type(n, "8");
    expect(screen.getByText(/changed · was/)).toHaveTextContent("changed · was 3");
    await userEvent.click(prepare);
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body.tool.configText).toBeUndefined(); // the file on the host is read
    expect(post.body.tool.set).toEqual({ n_gauss: "8", lambda_amp: "10", lb_sig: "1" });
  });
});
