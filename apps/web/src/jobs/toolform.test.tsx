import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import { JobsView } from "./JobsView";
import { synthTemplate, vgtTemplate } from "./templateFixtures";
import { changedSettings, choices, commandLine, editableKeys, keyHint, readConfigSetting, stepLine } from "./templateForm";

const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };

afterEach(() => vi.unstubAllGlobals());

describe("a job form drawn from a template", () => {
  it("offers the declared keys with their ranges and descriptions, never the output folder, and sends only what changed", async () => {
    const posts: any[] = [];
    const text = '[input]\ninput_cube = "x.fits"\n[fit]\nn_bins = 16\nthreshold = 2.5\nmode = "fast"\n';
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "POST") { posts.push({ url, body: JSON.parse(String(init.body)) }); return new Response(JSON.stringify({ id: 9, hostId: 1, name: "synthfit-run", template: "synthfit", script: "", scriptHash: "", state: "draft", detail: "", slurmId: "", workDir: "", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: null, note: "", autoImport: false, usage: null }), { status: 201 }); }
      if (url === "/api/job-templates") return new Response(JSON.stringify({ templates: [vgtTemplate, synthTemplate] }), { status: 200 });
      if (url === "/api/jobs") return new Response(JSON.stringify({ jobs: [] }), { status: 200 });
      if (url === "/api/sweeps") return new Response(JSON.stringify({ sweeps: [] }), { status: 200 });
      if (url.startsWith("/api/hosts/1/text")) return new Response(JSON.stringify({ text }), { status: 200 });
      return new Response("{}", { status: 200 });
    }));
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ New job" }));
    await userEvent.selectOptions(await screen.findByLabelText("Kind"), "synthfit");
    expect(screen.getByRole("heading", { name: "New SynthFit job" })).toBeInTheDocument();
    expect(screen.getByText(/Declared by the profile SynthFit@1/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Tool folder on the host"), "/shared/synthfit");
    await userEvent.type(screen.getByLabelText("Base config on the host"), "/data/run.toml");
    await userEvent.click(screen.getByRole("button", { name: "Load settings" }));
    const bins = await screen.findByLabelText("n_bins");
    expect(bins).toHaveValue("16");
    expect(bins).toHaveAccessibleDescription(/Number of histogram bins\. \(a whole number from 2 to 64; the tool's documented default is 16\)/);
    expect(screen.getByLabelText("threshold")).toHaveAccessibleDescription(/0 to 10/);
    expect(screen.queryByLabelText("Output folder")).toBeNull(); // Vesqira's own per job
    const mode = screen.getByLabelText("mode");
    expect(within(mode).getAllByRole("option").map((o) => o.textContent)).toEqual(["(keep)", "fast", "exact"]);
    expect(screen.getByLabelText("smooth")).toHaveValue(""); // a key the config does not have shows empty, and stays out of the request unless set
    await userEvent.clear(bins);
    await userEvent.type(bins, "32");
    await userEvent.selectOptions(mode, "exact");
    expect(screen.getByLabelText("CPUs")).toHaveValue("4"); // the template's starting request
    await userEvent.click(screen.getByRole("checkbox", { name: /Stop the job unless the folder is at commit 0123456/ }));
    await userEvent.click(screen.getByRole("button", { name: "Create draft and show the script" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({
      hostId: 1,
      template: "synthfit",
      name: "synthfit-run",
      tool: { repoDir: "/shared/synthfit", configPath: "/data/run.toml", set: { n_bins: "32", mode: "exact" }, cpus: 4, mem: "8G", time: "02:00:00", partition: "", account: "", pinProfileCommit: true },
    });
  });

  it("says what the script does before the command, in plain words", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(url === "/api/job-templates" ? { templates: [synthTemplate] } : url === "/api/jobs" ? { jobs: [] } : {}), { status: 200 })));
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ New job" }));
    const prep = (await screen.findByText("What the script does before and when it runs")).closest("details")!;
    expect(prep).toHaveTextContent("Stops with a plain message if Python (python3) is not available.");
    expect(prep).toHaveTextContent("srun --ntasks=1 python3 tool.py --config $cfg");
  });

  it("says so when no tool profile declares a template", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(url === "/api/job-templates" ? { templates: [] } : url === "/api/jobs" ? { jobs: [] } : {}), { status: 200 })));
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ New job" }));
    expect(await screen.findByText("No tool profile declares a job template.")).toBeInTheDocument();
  });
});

describe("template form helpers", () => {
  it("reads a list-written count as the number a person types, and leaves other values alone", () => {
    const k = { name: "n_gauss", section: "rohsa", kind: "int" as const, label: "n", description: "", expect: "", list: true };
    expect(readConfigSetting("n_gauss = [16]  # N", k)).toBe("16");
    expect(readConfigSetting("n_gauss = [16, 8]", k)).toBe("16");
    expect(readConfigSetting("ker = 4", { ...k, name: "ker", list: false })).toBe("4");
  });

  it("describes a range, an explicit expectation and a documented default", () => {
    const k = { name: "a", section: "s", kind: "number" as const, label: "a", description: "", expect: "" };
    expect(keyHint({ ...k, min: 0, max: 10 })).toBe("0 to 10");
    expect(keyHint({ ...k, min: 1 })).toBe("at least 1");
    expect(keyHint({ ...k, max: 9 })).toBe("at most 9");
    expect(keyHint({ ...k, expect: "a number of GB", min: 1, default: { value: "auto", reason: "r" } })).toBe("a number of GB; the tool's documented default is auto");
    expect(keyHint(k)).toBe("");
  });

  it("offers a choice list for a bool or a closed enum, and a text field when a word may be followed by a number", () => {
    const k = { name: "a", section: "s", label: "a", description: "", expect: "" };
    expect(choices({ ...k, kind: "bool" })).toEqual(["true", "false"]);
    expect(choices({ ...k, kind: "enum", enum: ["x", "y"] })).toEqual(["x", "y"]);
    expect(choices({ ...k, kind: "enum", enum: ["none"], enumNumeric: ["gauss"] })).toBeNull();
    expect(choices({ ...k, kind: "number" })).toBeNull();
  });

  it("sends a setting only when it differs from the config, and treats 10 and 10.0 as the same", () => {
    expect(changedSettings(synthTemplate, { n_bins: "16", threshold: "2.50", mode: " exact ", smooth: "" }, { n_bins: "16.0", threshold: "2.5", mode: "fast" })).toEqual({ mode: "exact" });
    expect(editableKeys(synthTemplate).map((x) => x.name)).not.toContain("output_dir");
  });

  it("writes steps and the command the way the script reads", () => {
    expect(stepLine({ kind: "export", name: "OMP_NUM_THREADS", value: "{{var:threads}}" })).toBe("Sets OMP_NUM_THREADS to threads.");
    expect(stepLine({ kind: "comment", text: "x" })).toBeNull();
    expect(commandLine(synthTemplate)).toBe("srun --ntasks=1 python3 tool.py --config $cfg");
  });
});
