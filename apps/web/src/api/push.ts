import { getJSON, postJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** Push to a group server (ADR 0066 sections 1 and 2). The link and the queue exist on a local instance only; the pushed items on
 * the group server only. */
export type GroupLink = Schemas["GroupLink"];
export type OutboxEntry = Schemas["OutboxEntry"];
export type PushPreflight = Schemas["PushPreflight"];
export type PushKind = Schemas["PushTarget"]["kind"];
export type PushedSummary = Schemas["PushedSummary"];
export type PushedItem = Schemas["PushedItem"];

// local instance
export const groupLink = () => getJSON<GroupLink>("/api/group-link");
export const linkGroup = (origin: string, token: string) => postJSON<GroupLink>("/api/group-link", { origin, token });
export const unlinkGroup = () => postJSON<GroupLink>("/api/group-link/unlink");
export const outbox = () => getJSON<Schemas["Outbox"]>("/api/outbox");
export const pushPreflight = (kind: PushKind, id: number) => postJSON<PushPreflight>("/api/outbox/preflight", { kind, id });
export const queuePush = (kind: PushKind, id: number) => postJSON<OutboxEntry>("/api/outbox", { kind, id });
export const retryOutbox = () => postJSON<{ ok?: boolean }>("/api/outbox/retry");

// group server
export const listPushed = () => getJSON<Schemas["PushedList"]>("/api/pushed");
export const getPushed = (id: number) => getJSON<PushedItem>(`/api/pushed/${id}`);
export const archivePushed = (id: number) => postJSON<Schemas["PushedArchived"]>(`/api/pushed/${id}/archive`);
export const pushedAssetURL = (id: number, sha: string) => `/api/pushed/${id}/assets/${sha}`;

/** What a reference means, in the words of ADR 0066 (a name never links). */
export function matchText(r: { match: string; name: string; datasetName?: string }): string {
  if (r.match === "verified") return `on this server: ${r.datasetName ?? r.name} (verified same bytes)`;
  if (r.match === "probable") return `probably on this server as ${r.datasetName ?? r.name} (sampled; not verified)`;
  return "not on this server";
}
