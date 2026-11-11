import { request as call, postJSON as post } from "./http";
import type { Dataset } from "./catalog";

export type Host = { id: number; name: string; dest: string; python: string; roots: string[]; createdAt: string };
export type HostCheck = { python: string; astropy: string; numpy: string; roots: string[] | null; enforces_roots: boolean };
export type RemoteEntry = { name: string; kind: "dir" | "file"; size?: number };

export type SshAlias = { alias: string; hostName?: string; user?: string };
/** Names written in your own ssh configuration. Only aliases and the plain host name and user are offered. */
export const sshConfigHosts = () => call<{ found: boolean; hosts: SshAlias[] }>("/api/hosts/ssh-config").then((r) => (Array.isArray(r.hosts) ? r.hosts : []));
/** Sub-folders on a host that is not saved yet (empty path = its home), so source folders are chosen, not typed. */
export const browseHostDirs = (dest: string, path?: string) =>
  call<{ path: string; entries: { name: string }[] }>(`/api/hosts/browse?dest=${encodeURIComponent(dest)}${path ? `&path=${encodeURIComponent(path)}` : ""}`);
export const listHosts = () => call<{ hosts: Host[] }>("/api/hosts").then((r) => r.hosts);
export const createHost = (h: { name: string; dest: string; python?: string; roots: string[] }) => post<Host>("/api/hosts", h);
export const checkHost = (id: number) => post<HostCheck>(`/api/hosts/${id}/check`, {});
export const deleteHost = (id: number) => post<{ deleted: boolean }>(`/api/hosts/${id}/delete`, {});
export const listRemote = (id: number, path?: string) =>
  call<{ path: string; entries: RemoteEntry[] }>(`/api/hosts/${id}/list${path ? `?path=${encodeURIComponent(path)}` : ""}`);
export const registerRemote = (id: number, path: string) => post<Dataset>(`/api/hosts/${id}/register`, { path });
