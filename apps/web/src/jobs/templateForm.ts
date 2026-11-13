import { readSetting } from "../api/jobs";
import type { JobTemplate, TemplateKey, TemplateStep } from "../api/jobs";

/**
 * Helpers for a job form drawn from a tool profile's template (ADR 0068). The template says which keys a person may change, with
 * their kinds and ranges; the server checks every value again and writes the script, so nothing here decides what is valid.
 */

/** The keys a person may change: not the ones Vesqira sets per job (the output folder). */
export const editableKeys = (t: JobTemplate): TemplateKey[] => t.keys.filter((k) => !k.internal);

/** The same quantity written two ways ("10" and "10.0") is not a change. */
export function sameValue(a: string, b: string): boolean {
  const x = a.trim();
  const y = b.trim();
  if (x === y) return true;
  const nx = Number(x);
  const ny = Number(y);
  return x !== "" && y !== "" && Number.isFinite(nx) && Number.isFinite(ny) && nx === ny;
}

/**
 * What a sweep may vary: the template's ties first (one setting standing for several keys moved together, with the reason), then
 * every editable key that is not a path. The server refuses anything else.
 */
export function sweepKeys(t: JobTemplate): { key: string; label: string; description: string }[] {
  const ties = (t.ties ?? []).map((x) => ({ key: x.name, label: x.label, description: x.reason }));
  const keys = editableKeys(t).filter((k) => k.kind !== "path").map((k) => ({ key: k.name, label: k.label, description: keyDescription(k) }));
  return [...ties, ...keys];
}

/** A key's plain description with what it accepts and the tool's documented default, as the job form words it. */
export function keyDescription(k: TemplateKey): string {
  const hint = keyHint(k);
  if (!hint) return k.description;
  const end = /[.!?]$/.exec(k.description)?.[0] ?? "";   // the hint goes inside the sentence: "Bins (2 to 64; ...)." not "Bins. (...)"
  return `${end ? k.description.slice(0, -1) : k.description} (${hint})${end}`;
}

/**
 * A setting read from a config's text, for the "was" of a field only: the first line that has the key, as a person would type it
 * (a count written as a list, "[16]", is 16). The server reads the config properly when it renders the job and is the authority.
 */
export function readConfigSetting(text: string, k: TemplateKey): string {
  const v = readSetting(text, k.name);
  return k.list ? v.replace(/^\[\s*/, "").replace(/\s*\]$/, "").split(",")[0]!.trim() : v;
}

/** The options of a key that is a choice among fixed words, or null for a key typed as text (a word followed by a number is typed). */
export function choices(k: TemplateKey): string[] | null {
  if (k.kind === "bool") return ["true", "false"];
  if (k.kind === "enum" && (k.enumNumeric ?? []).length === 0) return k.enum ?? [];
  return null;
}

/** What a field accepts, in one line: the template's own words when it has them, else the range, then the tool's documented default. */
export function keyHint(k: TemplateKey): string {
  const parts: string[] = [];
  if (k.expect) parts.push(k.expect);
  else if (k.min !== undefined && k.max !== undefined) parts.push(`${k.min} to ${k.max}`);
  else if (k.min !== undefined) parts.push(`at least ${k.min}`);
  else if (k.max !== undefined) parts.push(`at most ${k.max}`);
  if (k.default) parts.push(`the tool's documented default is ${k.default.value}${k.default.reason ? `, ${k.default.reason}` : ""}`);
  return parts.join("; ");
}

/** The settings to send: what the person changed from what the config said, so every other line of the config stays as it was. */
export function changedSettings(t: JobTemplate, vals: Record<string, string>, was: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const k of editableKeys(t)) {
    const x = (vals[k.name] ?? "").trim();
    if (x && !sameValue(x, was[k.name] ?? "")) out[k.name] = x;
  }
  return out;
}

const plainValue = (v: string) => v.replace(/\{\{repo\}\}/g, "the tool folder").replace(/\{\{cpus\}\}/g, "one per allocated CPU").replace(/\{\{var:([a-z0-9_]+)\}\}/g, "$1").replace(/\{\{config\}\}/g, "the config");

/** One preparation step of the script in plain words, or null for a step that is only a comment. The script itself is shown before anything is sent. */
export function stepLine(s: TemplateStep): string | null {
  switch (s.kind) {
    case "export": return `Sets ${s.name} to ${plainValue(s.value ?? "")}${s.keepIfSet ? " unless you already set it" : ""}.`;
    case "set": return `Keeps ${s.name} as ${plainValue(s.value ?? "")} for the lines below.`;
    case "announce": return `Writes the job id, the host and the date to the log${(s.show ?? []).length ? `, with ${(s.show ?? []).map((x) => x.label).join(", ")}` : ""}.`;
    case "require": return `Stops with a plain message if ${s.title} (${s.command}) is not available.`;
    case "modules": return `${s.purge ? "Clears the loaded modules" : "Keeps the loaded modules"}${(s.modules ?? []).length ? ` and loads ${(s.modules ?? []).join(", ")}` : ""}.`;
    case "build_if_stale": return `Builds ${s.binary} in ${s.dir} (make ${(s.target ?? []).join(" ")}) when a source file is newer than the binary.`;
    case "ulimit": return `Sets the shell limit -${s.name} to ${s.value}.`;
    default: return null;
  }
}

/** The command as the script writes it, with the config as the file the script makes. */
export const commandLine = (t: JobTemplate): string => t.command.map((w) => w.replace(/\{\{config\}\}/g, "$" + t.config.var).replace(/\{\{cpus\}\}/g, "$SLURM_CPUS_PER_TASK").replace(/\{\{var:([a-z0-9_]+)\}\}/g, "$$$1").replace(/\{\{repo\}\}/g, "<folder>")).join(" ");

/** The checkbox that asks a job to insist on the commit the profile was written against (ADR 0068). A rerun's own commit wins when both apply. */
export const pinLabel = (t: JobTemplate): string =>
  `Stop the job unless the folder is at commit ${t.repo.commit.slice(0, 7)}, with no changed tracked files (the revision this profile was written against)`;

/** Said under the checkbox of a form that can also carry a parent run's commit. */
export const PIN_RERUN_NOTE = "A strict rerun insists on the parent run's own commit instead; that is the stronger claim and wins when both apply. Each approval screen says which commit the script insists on.";
