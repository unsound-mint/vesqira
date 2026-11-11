import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import type { Job, JobUsage } from "../api/jobs";
import { JobsView, previousUse } from "./JobsView";
import { SizingHint, UsageSummary } from "./JobUsage";
import { parseMem, parseTime, span, usageLines } from "./usageText";

const GiB = 2 ** 30;
const usage = (over: Partial<JobUsage> = {}): JobUsage => ({
  state: "COMPLETED", exitCode: "0:0", elapsedSeconds: 6 * 3600 + 12 * 60 + 3, timelimitSeconds: 3 * 86400, reqMemBytes: 140 * GiB, maxRssBytes: 79 * GiB, allocCpus: 32,
  totalCpuSeconds: 85000, cpuEfficiency: 0.43, cpuNotAccounted: false, oomKilled: false,
  suggest: { mem: "103G", time: "09:19:00", memBytes: 103 * GiB, timeSeconds: 33540, basis: "1.3 x the measured peak memory; 1.5 x the elapsed time" }, ...over,
});

describe("what a finished job used, in words", () => {
  it("reads like the owner's own sentence: memory used of requested, time of the limit, CPU efficiency", () => {
    const [first] = usageLines(usage());
    expect(first).toBe("Used 79.0 GiB of 140.0 GiB requested; ran 6 h 12 min of a 3 d limit; CPU efficiency 43 %.");
  });

  it("says the CPU time is not accounted instead of showing 0 %", () => {
    const lines = usageLines(usage({ cpuEfficiency: null, cpuNotAccounted: true, totalCpuSeconds: 0 }));
    expect(lines[0]).toContain("CPU time not accounted by Slurm for this job");
    expect(lines.join(" ")).not.toMatch(/efficiency 0 %/);
    expect(lines[1]).toMatch(/child process the scheduler does not account/);
  });

  it("never turns a value Slurm did not report into 0", () => {
    const [first] = usageLines(usage({ maxRssBytes: null, reqMemBytes: null, timelimitSeconds: null, cpuEfficiency: null }));
    expect(first).toBe("Memory use not recorded by Slurm; ran 6 h 12 min (no time limit recorded); CPU time not recorded.");
  });

  it("names the columns a site's sacct does not report, without turning them into zeros", () => {
    const lines = usageLines(usage({ reqMemBytes: null, maxRssBytes: null, unsupported: ["ReqTRES", "MaxRSS"] }));
    expect(lines[0]).toMatch(/^Memory use not recorded by Slurm;/);
    expect(lines[1]).toBe("This site's sacct does not report ReqTRES, MaxRSS, so what depends on them is shown as not reported. The job's state is unaffected.");
    expect(usageLines(usage({ unsupported: [] }))).toHaveLength(1);
  });

  it("explains an out-of-memory stop and a time-limit stop in plain words", () => {
    const oom = usageLines(usage({ state: "FAILED", oomKilled: true, maxRssBytes: 140 * GiB })).join(" ");
    expect(oom).toMatch(/stopped this job because it used more memory than was requested \(140.0 GiB\)/);
    expect(oom).toMatch(/real need is higher/);
    const to = usageLines(usage({ state: "TIMEOUT", elapsedSeconds: 3 * 86400 })).join(" ");
    expect(to).toMatch(/stopped this job at its time limit \(3 d\)/);
    expect(to).toMatch(/floor on how long the work needs/);
    expect(to).not.toMatch(/success/i);
  });

  it("renders as a labelled region with the source named", () => {
    render(<UsageSummary usage={usage()} />);
    expect(screen.getByRole("region", { name: "What this job used" })).toHaveTextContent(/As recorded by sacct/);
  });

  it("parses Slurm memory and time requests, and refuses what it does not understand", () => {
    expect(parseMem("64G")).toBe(64 * GiB);
    expect(parseMem("4000M")).toBe(4000 * 2 ** 20);
    expect(parseMem("2048")).toBe(2048 * 2 ** 20); // a bare number is MiB for --mem
    expect(parseMem("lots")).toBeNull();
    expect(parseTime("1-00:00:00")).toBe(86400);
    expect(parseTime("02:30:00")).toBe(9000);
    expect(parseTime("90")).toBe(5400);
    expect(parseTime("2-12")).toBe(2 * 86400 + 12 * 3600);
    expect(parseTime("soon")).toBeNull();
    expect(span(45)).toBe("45 s");
    expect(span(86400 + 7200)).toBe("1 d 2 h");
  });
});

describe("sizing the next request from a measured one", () => {
  it("shows the measured peak and time next to what is requested, and applies a suggestion only when a button is clicked", async () => {
    const mem = vi.fn();
    const time = vi.fn();
    render(<SizingHint jobId={12} jobName="vgt-a" usage={usage()} mem="64G" time="1-00:00:00" onUseMem={mem} onUseTime={time} />);
    const box = screen.getByRole("region", { name: "Measured use of job 12" });
    expect(box).toHaveTextContent("peak memory 79.0 GiB (requested 140.0 GiB)");
    expect(box).toHaveTextContent("ran 6 h 12 min (limit 3 d)");
    expect(box).toHaveTextContent("You are asking for 64G and 1-00:00:00");
    expect(box).toHaveTextContent("Suggested from job 12’s measured use: memory 103G, time 09:19:00");
    expect(box).toHaveTextContent(/partition.s own maximum time is not known here/);
    expect(mem).not.toHaveBeenCalled(); // nothing is applied by showing it
    expect(time).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Use memory 103G" }));
    expect(mem).toHaveBeenCalledWith("103G");
    expect(time).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Use time 09:19:00" }));
    expect(time).toHaveBeenCalledWith("09:19:00");
  });

  it("warns when the request is below what the previous job reached, and offers nothing for a job that says little", () => {
    const { rerender } = render(<SizingHint jobId={3} jobName="r" usage={usage()} mem="64G" time="00:30:00" onUseMem={() => {}} onUseTime={() => {}} />);
    expect(screen.getByText(/memory request is smaller than the 79.0 GiB that job reached/)).toBeInTheDocument();
    expect(screen.getByText(/time limit is shorter than the 6 h 12 min that job needed/)).toBeInTheDocument();
    rerender(<SizingHint jobId={3} jobName="r" usage={usage({ state: "CANCELLED", suggest: null })} mem="200G" time="3-00:00:00" onUseMem={() => {}} onUseTime={() => {}} />);
    expect(screen.queryByText(/memory request is smaller/)).not.toBeInTheDocument();
    expect(screen.getByText(/No suggestion: that job did not run to its end or its limit/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Use / })).not.toBeInTheDocument();
  });

  it("an OOM job's hint says its peak understates the need", () => {
    render(<SizingHint jobId={3} jobName="rohsa" usage={usage({ state: "FAILED", oomKilled: true })} mem="140G" time="1-00:00:00" onUseMem={() => {}} onUseTime={() => {}} />);
    expect(screen.getByText(/peak understates what the work needs/)).toBeInTheDocument();
  });
});

const host: Host = { id: 1, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
const baseJob: Job = { id: 1, hostId: 1, name: "rohsa-1", template: "rohsa", script: "#!/bin/bash\n", scriptHash: "a".repeat(64), state: "failed", detail: "", slurmId: "9", workDir: "w", outputDir: "", createdAt: "", submittedAt: null, finishedAt: null, runId: null, parentRunId: null, note: "", autoImport: false, usage: usage({ state: "FAILED", oomKilled: true, suggest: { mem: "210G", time: "", memBytes: 210 * GiB, timeSeconds: 0, basis: "x" } }) };

describe("the new-job form", () => {
  it("picks the newest measured job of the same kind on the same host", () => {
    const jobs = [baseJob, { ...baseJob, id: 5, usage: null }, { ...baseJob, id: 4, template: "vgt" as const }, { ...baseJob, id: 3, hostId: 2 }, { ...baseJob, id: 2 }];
    expect(previousUse(jobs, 1, "rohsa")?.id).toBe(2);
    expect(previousUse(jobs, 1, "vgt")?.id).toBe(4);
    expect(previousUse(jobs, 7, "vgt")).toBeNull();
  });

  it("offers the suggestion for ROHSA, and the default memory stays until the researcher clicks", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(url === "/api/jobs" ? { jobs: [baseJob] } : {}), { status: 200 })));
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: "+ New job" }));
    await userEvent.selectOptions(screen.getByLabelText("Kind"), "rohsa");
    expect(await screen.findByRole("region", { name: "Measured use of job 1" })).toBeInTheDocument();
    expect(screen.getByLabelText("Memory")).toHaveValue("64G"); // the form's default, untouched
    await userEvent.click(screen.getByRole("button", { name: "Use memory 210G" }));
    expect(screen.getByLabelText("Memory")).toHaveValue("210G");
    vi.unstubAllGlobals();
  });

  it("shows what a finished job used on the job itself", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(url === "/api/jobs" ? { jobs: [baseJob] } : {}), { status: 200 })));
    render(<JobsView hosts={[host]} onImported={() => {}} onOpenRun={() => {}} />);
    await userEvent.click(await screen.findByRole("button", { name: /rohsa-1/ }));
    const box = screen.getByRole("region", { name: "What this job used" });
    expect(box).toHaveTextContent(/Used 79.0 GiB of 140.0 GiB requested/);
    expect(box).toHaveTextContent(/stopped this job because it used more memory than was requested/);
    vi.unstubAllGlobals();
  });
});
