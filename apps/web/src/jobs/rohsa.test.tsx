import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import { JobsView } from "./JobsView";
import { ROHSA_SETTINGS, ROHSA_SWEEP_KEYS, readRohsaSetting, sameValue } from "./rohsaSettings";

describe("ROHSA settings", () => {
  it("never offers the input cube or the output folder, which Vesqira owns, and ties the three smoothing weights for sweeps", () => {
    const keys = ROHSA_SETTINGS.map((s) => s.key);
    for (const bad of ["output_dir", "input", "reuse", "force_full_cube"]) expect(keys).not.toContain(bad);
    expect(keys).toEqual(expect.arrayContaining(["n_gauss", "lambda_amp", "lambda_mu", "lambda_sig", "lb_sig", "ub_sig", "maxiter", "noise", "memory_gb", "crop_size", "spectral_kernel"]));
    expect(ROHSA_SWEEP_KEYS[0]!.key).toBe("lambda");
    expect(ROHSA_SWEEP_KEYS.map((s) => s.key)).toEqual(expect.arrayContaining(keys));
  });

  it("treats 10 and 10.0 as the same number and a different number as a change", () => {
    expect(sameValue("10", "10.0")).toBe(true);
    expect(sameValue(" 16 ", "16")).toBe(true);
    expect(sameValue("12", "16")).toBe(false);
    expect(sameValue("true", "false")).toBe(false);
    expect(sameValue("", "0")).toBe(false);
  });

  it("reads a Gaussian count written as a list as the number a person types", () => {
    const text = '[rohsa]\nn_gauss = [16]   # N\nlambda_amp = 10.0\nnoise = true\n';
    expect(readRohsaSetting(text, "n_gauss")).toBe("16");
    expect(readRohsaSetting(text, "lambda_amp")).toBe("10.0");
    expect(readRohsaSetting(text, "noise")).toBe("true");
    expect(readRohsaSetting(text, "crop_size")).toBe("");
  });
});

const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };

describe("new ROHSA job", () => {
  it("loads the config's settings and sends only what was changed, with the sizing table beside the request", async () => {
    const posts: any[] = [];
    const text = "[input]\ncrop_size = 1024\n[rohsa]\nn_gauss = [16]\nlambda_amp = 10.0\nnoise = true\n";
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "POST") { posts.push({ url, body: JSON.parse(String(init.body)) }); return new Response(JSON.stringify({ id: 4, hostId: 1, name: "rohsa-run", template: "rohsa", script: "", scriptHash: "", state: "draft", detail: "", slurmId: "", workDir: "", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: null, note: "", autoImport: false, usage: null }), { status: 201 }); }
      if (url === "/api/jobs") return new Response(JSON.stringify({ jobs: [] }), { status: 200 });
      if (url === "/api/sweeps") return new Response(JSON.stringify({ sweeps: [] }), { status: 200 });
      if (url.startsWith("/api/hosts/1/text")) return new Response(JSON.stringify({ text }), { status: 200 });
      if (url.startsWith("/api/rohsa/sizing")) return new Response(JSON.stringify({ cropSize: 1024, nGauss: 16, assumed: [], cpus: 16, mem: "40G", time: "12:00:00", scriptPartition: "ZEN4", basis: "the sizing table of your pipeline's hpc/irgetas/submit_rohsa.sh" }), { status: 200 });
      return new Response("{}", { status: 200 });
    }));
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ New job" }));
    await userEvent.selectOptions(screen.getByLabelText("Kind"), "rohsa");
    expect(screen.getByText(/The output folder is set for each job/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("ROHSA-LMC folder on the host"), "/shared/vgt");
    await userEvent.type(screen.getByLabelText("ROHSA config on the host"), "/data/configs/rohsa/a.toml");
    await userEvent.click(screen.getByRole("button", { name: "Load settings" }));
    expect(await screen.findByLabelText(/^n_gauss/)).toHaveValue("16");
    expect(screen.getByLabelText(/^crop_size/)).toHaveValue("1024");
    const hint = await screen.findByRole("region", { name: "Suggested size from your sizing table" });
    expect(hint).toHaveTextContent("crop_size 1024 and n_gauss 16 your table suggests 16 CPUs, 40G, 12:00:00");
    const n = screen.getByLabelText(/^n_gauss/);
    await userEvent.clear(n);
    await userEvent.type(n, "12");
    await userEvent.click(screen.getByRole("button", { name: "Create draft and show the script" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].url).toBe("/api/jobs");
    expect(posts[0].body.rohsa.set).toEqual({ n_gauss: "12" }); // the rest of the config stays as the file has it
    expect(posts[0].body.rohsa.configPath).toBe("/data/configs/rohsa/a.toml");
    vi.unstubAllGlobals();
  });
});
