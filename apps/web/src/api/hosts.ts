import { getJSON, request as call, postJSON as post } from "./http";
import type { Dataset } from "./catalog";
import type { components } from "./schema";

export type HostStorage = components["schemas"]["HostStorage"];
export type HostStorageReport = components["schemas"]["HostStorageReport"];
export type QuotaEntry = components["schemas"]["QuotaEntry"];
export type HostFilesystem = components["schemas"]["HostFilesystem"];
export type RunFolder = components["schemas"]["RunFolder"];
export type DiskUsage = components["schemas"]["DiskUsage"];

export type VpnResult = components["schemas"]["HostVpnResult"];
export type Host = {
  id: number; name: string; dest: string; python: string; roots: string[]; createdAt: string;
  /** The user's own VPN client as an argv, run without a shell (ADR 0029). Absent when none is set. */
  vpnCommand?: string[]; vpnDisconnectCommand?: string[];
  /** What this session last learned about reaching the host (a check, the job watcher or a VPN command). */
  reachable?: "yes" | "no" | "unknown"; reachableAt?: string;
  /** The VPN client Vesqira started for this host in this session (running, or how it ended) with its recent output. */
  vpnClient?: { running: boolean; pid: number; startedAt: string; exitCode?: number; output: string };
};
export type HostCheck = { python: string; astropy: string; numpy: string; roots: string[] | null; enforces_roots: boolean };
export type RemoteEntry = { name: string; kind: "dir" | "file"; size?: number };

export type SshAlias = { alias: string; hostName?: string; user?: string };
/** Names written in your own ssh configuration. Only aliases and the plain host name and user are offered. */
export const sshConfigHosts = () => call<{ found: boolean; hosts: SshAlias[] }>("/api/hosts/ssh-config").then((r) => (Array.isArray(r.hosts) ? r.hosts : []));
/** Sub-folders on a host that is not saved yet (empty path = its home), so source folders are chosen, not typed. */
export const browseHostDirs = (dest: string, path?: string) =>
  call<{ path: string; entries: { name: string }[] }>(`/api/hosts/browse?dest=${encodeURIComponent(dest)}${path ? `&path=${encodeURIComponent(path)}` : ""}`);
export const listHosts = () => call<{ hosts: Host[] }>("/api/hosts").then((r) => r.hosts);
export const createHost = (h: { name: string; dest: string; python?: string; roots: string[]; vpnCommand?: string[]; vpnDisconnectCommand?: string[] }) => post<Host>("/api/hosts", h);
/** Whether the server was started with --enable-vpn (ADR 0029). Any failure to find out means off. */
export async function vpnEnabled(): Promise<boolean> {
  try {
    const body = await getJSON<{ features?: Record<string, boolean> } | null>("/api/capabilities");
    return body?.features?.vpn === true;
  } catch {
    return false;
  }
}
/** Runs the host's stored VPN command. The page sends only the action; the server owns the command and checks the host afterwards. */
export const runHostVpn = (id: number, action: "connect" | "disconnect") => post<VpnResult>(`/api/hosts/${id}/vpn`, { action });
/** Replaces both stored VPN commands of a host (a missing one is cleared). */
export const setHostVpn = (id: number, c: { vpnCommand?: string[]; vpnDisconnectCommand?: string[] }) => post<Host>(`/api/hosts/${id}/vpn-config`, c);
export const checkHost = (id: number) => post<HostCheck>(`/api/hosts/${id}/check`, {});
export type PythonCandidate = components["schemas"]["HostPythonCandidate"];
/** Sets the host's Python command (a module's interpreter or a venv), drops its cached worker and checks it again. */
export const setHostPython = (id: number, python: string) => post<HostCheck>(`/api/hosts/${id}/python`, { python });
/** Looks on the host for interpreters with numpy and astropy: login-shell python, environment modules, venvs and conda. Read-only. */
export const findHostPython = (id: number) => post<{ candidates: PythonCandidate[]; minPython: string }>(`/api/hosts/${id}/python/find`, {});
export const deleteHost = (id: number) => post<{ deleted: boolean }>(`/api/hosts/${id}/delete`, {});
export const listRemote = (id: number, path?: string) =>
  call<{ path: string; entries: RemoteEntry[] }>(`/api/hosts/${id}/list${path ? `?path=${encodeURIComponent(path)}` : ""}`);
export const registerRemote = (id: number, path: string) => post<Dataset>(`/api/hosts/${id}/register`, { path });
/** Quota and disk usage of a host. The server reuses a look for 10 minutes, so asking again costs nothing on the host. */
export const getHostStorage = (id: number) => call<HostStorage>(`/api/hosts/${id}/storage`);
/** Looks at the host now (the server still refuses to reach it more than once per 15 seconds). */
export const refreshHostStorage = (id: number) => post<HostStorage>(`/api/hosts/${id}/storage/refresh`, {});
/** Run folders on a host with the sizes measured so far; no call to the host. */
export const listRunFolders = (id: number) => call<{ folders: RunFolder[] }>(`/api/hosts/${id}/folders`).then((r) => r.folders ?? []);
/** `du -sk` of one folder, read-only. */
export const measureFolder = (id: number, path: string) => post<DiskUsage>(`/api/hosts/${id}/folders/size`, { path });
