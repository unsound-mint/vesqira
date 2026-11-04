import { ApiError } from "./catalog";
import type { Dataset } from "./catalog";

export type Host = { id: number; name: string; dest: string; python: string; roots: string[]; createdAt: string };
export type HostCheck = { python: string; astropy: string; numpy: string; roots: string[] | null; enforces_roots: boolean };
export type RemoteEntry = { name: string; kind: "dir" | "file"; size?: number };

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

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export const listHosts = () => call<{ hosts: Host[] }>("/api/hosts").then((r) => r.hosts);
export const createHost = (h: { name: string; dest: string; python: string; roots: string[] }) => post<Host>("/api/hosts", h);
export const checkHost = (id: number) => post<HostCheck>(`/api/hosts/${id}/check`, {});
export const deleteHost = (id: number) => post<{ deleted: boolean }>(`/api/hosts/${id}/delete`, {});
export const listRemote = (id: number, path?: string) =>
  call<{ path: string; entries: RemoteEntry[] }>(`/api/hosts/${id}/list${path ? `?path=${encodeURIComponent(path)}` : ""}`);
export const registerRemote = (id: number, path: string) => post<Dataset>(`/api/hosts/${id}/register`, { path });
