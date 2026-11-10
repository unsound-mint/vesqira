import { ApiError } from "./catalog";

export type TreeEntry = { name: string; kind: "dir" | "file" | "link"; size?: number };
export type Tree = { path: string; entries: TreeEntry[]; truncated: boolean };
export type OpenedProject = { path: string; name: string };

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch {
    throw new ApiError("Cannot reach the Vesqira API.");
  }
  const body = (await res.json().catch(() => null)) as { error?: string } | null;
  if (!res.ok) throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  return body as T;
}

/** Makes a folder readable (like opening a folder in an editor). Owner only; refused for the home directory itself. */
export const openProject = (path: string) => call<OpenedProject>("/api/projects/open", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });
/** The names in one folder inside an opened project. */
export const fileTree = (path: string) => call<Tree>(`/api/files/tree?path=${encodeURIComponent(path)}`);
export const roots = () => call<{ roots: string[] }>("/api/roots").then((r) => r.roots);
