import type { components } from "./schema";
import { ApiError } from "./catalog";

export type TextVersion = components["schemas"]["TextVersion"];
export type TextFile = components["schemas"]["TextFile"];

/** Whether the server was started with --enable-editor (ADR 0029). Any failure to find out means off. */
export async function editorEnabled(): Promise<boolean> {
  try {
    const res = await fetch("/api/capabilities");
    if (!res.ok) return false;
    const body = (await res.json()) as { features?: Record<string, boolean> };
    return body.features?.editor === true;
  } catch {
    return false;
  }
}

async function failure(res: Response): Promise<ApiError> {
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  return new ApiError(body?.error ?? `Request failed (${res.status})`);
}

export async function openTextFile(path: string): Promise<TextFile> {
  let res: Response;
  try {
    res = await fetch(`/api/files/text?${new URLSearchParams({ path })}`);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  if (!res.ok) throw await failure(res);
  return (await res.json()) as TextFile;
}

export type SaveResult = { status: "saved"; file: TextFile } | { status: "conflict"; current: TextFile; message: string };

/** Saves over the version the file had when it was opened. A changed file is a "conflict" carrying what is on disk now; nothing was written. */
export async function saveTextFile(path: string, content: string, base: TextVersion): Promise<SaveResult> {
  let res: Response;
  try {
    res = await fetch("/api/files/text", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path, content, base: { size: base.size, mtime: base.mtime, sha256: base.sha256 } }) });
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  if (res.status === 409) {
    const body = (await res.json()) as components["schemas"]["TextConflict"];
    return { status: "conflict", current: body.current, message: body.error };
  }
  if (!res.ok) throw await failure(res);
  return { status: "saved", file: (await res.json()) as TextFile };
}

/** Extensions the server will open (it decides; this only hides the entry point for files it would refuse). */
const EDITABLE = new Set(["toml", "yaml", "yml", "json", "jl", "py", "sh", "slurm", "sbatch", "md", "txt", "cfg", "ini", "conf", "log"]);
export function looksEditable(name: string): boolean {
  const i = name.lastIndexOf(".");
  if (i < 0) return true;
  if (i === 0) return false;
  return EDITABLE.has(name.slice(i + 1).toLowerCase());
}

/** Line endings of a text: the browser's text editors read and write "\n" only, so the file's own are put back on save. */
export function lineEnding(text: string): { eol: "\n" | "\r\n"; mixed: boolean } {
  const crlf = (text.match(/\r\n/g) ?? []).length;
  const lf = (text.match(/\n/g) ?? []).length - crlf;
  return { eol: crlf > lf ? "\r\n" : "\n", mixed: crlf > 0 && lf > 0 };
}
export const toEditorText = (text: string) => text.replace(/\r\n/g, "\n");
export const fromEditorText = (text: string, eol: "\n" | "\r\n") => (eol === "\n" ? text : text.replace(/\n/g, "\r\n"));

/** A UTF-8 byte order mark is invisible in the editor, so it is set aside on open and put back first on save. */
export const BOM = "\uFEFF";
export const hasBom = (text: string) => text.startsWith(BOM);
export const withoutBom = (text: string) => (hasBom(text) ? text.slice(1) : text);
/** A "\r" that is not part of "\r\n": the editor treats it as a line break and would write "\n" in its place, so such files are not opened. */
export const hasLoneCR = (text: string) => /\r(?!\n)/.test(text);
