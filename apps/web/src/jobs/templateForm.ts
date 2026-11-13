import { readSetting } from "../api/jobs";
import type { JobTemplate, TemplateKey, TemplateStep } from "../api/jobs";
import { sameValue } from "./rohsaSettings";

/**
 * Helpers for a job form drawn from a tool profile's template (ADR 0068). The template says which keys a person may change, with
 * their kinds and ranges; the server checks every value again and writes the script, so nothing here decides what is valid.
 */

/** The keys a person may change: not the ones Vesqira sets per job (the output folder). */
export const editableKeys = (t: JobTemplate): TemplateKey[] => t.keys.filter((k) => !k.internal);

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
  if (k.default) parts.push(`the tool's documented default is ${k.default.value}`);
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
