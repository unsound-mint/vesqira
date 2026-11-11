import { bytes } from "../catalog/format";
import type { JobUsage } from "../api/jobs";
import { formatDuration } from "../ui/duration";

/** A Slurm --mem value ("64G", "4000M", "2T", a bare number is MiB) in bytes, or null when it is not one. */
export function parseMem(text: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([KMGT]?)\s*$/i.exec(text);
  if (!m) return null;
  const unit = { "": 2 ** 20, K: 2 ** 10, M: 2 ** 20, G: 2 ** 30, T: 2 ** 40 }[m[2]!.toUpperCase() as "" | "K" | "M" | "G" | "T"];
  return Math.round(Number(m[1]) * unit);
}

/** The sentences that describe what a finished job used. Each says what the scheduler reported; none is a verdict on the result. */
export function usageLines(u: JobUsage): string[] {
  const out: string[] = [];
  const mem =
    u.maxRssBytes === null
      ? "memory use not recorded by Slurm"
      : u.reqMemBytes === null
        ? `used ${bytes(u.maxRssBytes)} (the request is not recorded)`
        : `used ${bytes(u.maxRssBytes)} of ${bytes(u.reqMemBytes)} requested`;
  const time = u.timelimitSeconds === null ? `ran ${formatDuration(u.elapsedSeconds)} (no time limit recorded)` : `ran ${formatDuration(u.elapsedSeconds)} of a ${formatDuration(u.timelimitSeconds)} limit`;
  const cpu = u.cpuNotAccounted ? "CPU time not accounted by Slurm for this job" : u.cpuEfficiency === null ? "CPU time not recorded" : `CPU efficiency ${Math.round(u.cpuEfficiency * 100)} %`;
  const sentence = [mem, time, cpu].join("; ");
  out.push(`${sentence.charAt(0).toUpperCase()}${sentence.slice(1)}.`);
  if (u.unsupported && u.unsupported.length > 0) {
    out.push(`This site's sacct does not report ${u.unsupported.join(", ")}, so what depends on ${u.unsupported.length === 1 ? "it" : "them"} is shown as not reported. The job's state is unaffected.`);
  }
  if (u.cpuNotAccounted) {
    out.push("Slurm's TotalCPU is about zero although the job ran for a long time. That happens when the work runs in a child process the scheduler does not account, so the efficiency is not shown rather than shown as 0 %.");
  }
  if (u.oomKilled) {
    out.push(
      `Slurm stopped this job because it used more memory than was requested${u.reqMemBytes === null ? "" : ` (${bytes(u.reqMemBytes)})`}. ` +
        "The peak above is what it had reached when it was stopped, so the real need is higher: request more memory for the next run.",
    );
  }
  if (u.state === "TIMEOUT") {
    out.push(
      `Slurm stopped this job at its time limit${u.timelimitSeconds === null ? "" : ` (${formatDuration(u.timelimitSeconds)})`}. ` +
        "Anything it wrote before then may be incomplete, and the elapsed time is a floor on how long the work needs, not an estimate of it.",
    );
  }
  return out;
}
