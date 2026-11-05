import { ApiError } from "./catalog";

export type PathEntry = { name: string; path: string; dir: boolean };
export type Completion = { base: string; entries: PathEntry[]; truncated: boolean };
export type PathKind = "dir" | "file" | "any";

/** Names in the folder of `prefix` on this computer, or on a host. Names only; never contents (ADR 0031). */
export async function completePath(prefix: string, o: { kind?: PathKind; ext?: string[]; hostId?: number }, signal?: AbortSignal): Promise<Completion> {
  const q = new URLSearchParams({ prefix });
  if (o.kind) q.set("kind", o.kind);
  if (o.ext?.length) q.set("ext", o.ext.join(","));
  const url = o.hostId !== undefined ? `/api/hosts/${o.hostId}/complete` : "/api/fs/complete";
  const res = await fetch(`${url}?${q}`, { signal });
  if (!res.ok) throw new ApiError(`Completion failed (${res.status})`);
  const body = (await res.json()) as Completion;
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
};

export type ChooseResult = { status: "chosen"; chosen: Chosen } | { status: "cancelled" } | { status: "unavailable" };

/** Opens the desktop's own file chooser through the local server. "unavailable" means this desktop has none. */
export async function chooseFile(o: { mode: "folder" | "file"; title: string; ext?: string[] }): Promise<ChooseResult> {
  let res: Response;
  try {
    res = await fetch("/api/fs/choose", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: o.mode, title: o.title, ext: o.ext ?? [] }) });
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  if (res.status === 501) return { status: "unavailable" };
  if (res.status === 204) return { status: "cancelled" };
  const body = (await res.json().catch(() => null)) as (Chosen & { error?: string }) | null;
  if (!res.ok || !body) throw new ApiError(body?.error ?? `The file chooser failed (${res.status})`);
  return { status: "chosen", chosen: body };
}
