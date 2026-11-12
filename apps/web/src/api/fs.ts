import { ApiError, getJSON, requestRaw } from "./http";

export type PathEntry = { name: string; path: string; dir: boolean };
export type Completion = { base: string; entries: PathEntry[]; truncated: boolean };
export type PathKind = "dir" | "file" | "any";

/** Names in the folder of `prefix` on this computer, or on a host. Names only; never contents (ADR 0031). */
export async function completePath(prefix: string, o: { kind?: PathKind; ext?: string[]; hostId?: number }, signal?: AbortSignal): Promise<Completion> {
  const q = new URLSearchParams({ prefix });
  if (o.kind) q.set("kind", o.kind);
  if (o.ext?.length) q.set("ext", o.ext.join(","));
  const url = o.hostId !== undefined ? `/api/hosts/${o.hostId}/complete` : "/api/fs/complete";
  const body = await getJSON<Completion>(`${url}?${q}`, signal);
  return { base: body.base, entries: Array.isArray(body.entries) ? body.entries : [], truncated: !!body.truncated };
}

export type Chosen = {
  path: string;
  /** The registered host the folder belongs to when it came through a file-manager mount of that host. */
  hostId: number | null;
  /** The folder's path on that host. */
  remotePath: string | null;
  /** A mount of a host that is not registered yet: what the page needs to offer adding it. */
  gvfs: { host: string; user?: string; port?: string } | null;
  /** The path is inside the Documents portal's mount: what a sandbox can read, not the file's real location. */
  documentPortal?: boolean;
};

export type ChooseResult = { status: "chosen"; chosen: Chosen } | { status: "cancelled" } | { status: "unavailable" };

/** Opens the desktop's own file chooser through the local server. "unavailable" means this desktop has none. */
export async function chooseFile(o: { mode: "folder" | "file"; title: string; ext?: string[] }): Promise<ChooseResult> {
  const res = await requestRaw("/api/fs/choose", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: o.mode, title: o.title, ext: o.ext ?? [] }) }, { allow: [501] });
  if (res.status === 501) return { status: "unavailable" };
  if (res.status === 204) return { status: "cancelled" };
  const body = (await res.json().catch(() => null)) as Chosen | null;
  if (!body) throw new ApiError(`The file chooser failed (${res.status})`, [], res.status);
  return { status: "chosen", chosen: body };
}
