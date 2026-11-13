import { getJSON, postJSON, putJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** Relayed run requests (ADR 0066 section 3). The inboxes and requests live on the group server; the pull state, the drafts and
 * the approval on the key holder's local instance. */
export type RunInbox = Schemas["RunInbox"];
export type RelayInbox = Schemas["RelayInbox"];
export type RelayLabel = Schemas["RelayLabel"];
export type TemplateBounds = Schemas["RelayTemplateBounds"];
export type Bound = Schemas["RelayBound"];
export type RelayAsk = Schemas["RelayAsk"];
export type RelayResources = Schemas["RelayResources"];
export type RunRequest = Schemas["RunRequest"];
export type RunRequestDetail = Schemas["RunRequestDetail"];
export type RunRequestEvent = Schemas["RunRequestEvent"];
export type RunRequestFile = Schemas["RunRequestFile"];
export type RequestState = RunRequest["state"];
export type RelayLocal = Schemas["RelayLocal"];
export type Relayed = Schemas["Relayed"];
export type RelayedDetail = Schemas["RelayedDetail"];
export type RelayedApprove = Schemas["RelayedApproveRequest"];
export type RelayedApproved = Schemas["RelayedApproved"];
export type RelayedDraft = Schemas["RelayedDraftRequest"];
export type GrantOffer = Schemas["RelayGrantOffer"];
export type Grant = Schemas["RelayGrant"];
export type GrantList = Schemas["RelayGrantList"];
export type GrantCreate = Schemas["RelayGrantCreate"];
export type RelayMember = Schemas["RelayMember"];
export type RunRequestPreview = Schemas["RunRequestPreview"];
export type GrantState = NonNullable<Relayed["grantState"]>;

// group server, session
export const listRunInboxes = () => getJSON<Schemas["RunInboxList"]>("/api/run-inboxes").then((r) => r.inboxes);
export const listRunRequests = () => getJSON<Schemas["RunRequestList"]>("/api/run-requests").then((r) => r.requests);
export const getRunRequest = (id: number) => getJSON<RunRequestDetail>(`/api/run-requests/${id}`);
export const fileRunRequest = (b: RunRequestFile) => postJSON<RunRequest>("/api/run-requests", b);
export const withdrawRunRequest = (id: number) => postJSON<Schemas["RelayState"]>(`/api/run-requests/${id}/withdraw`);
export const noteRunRequest = (id: number, text: string) => postJSON<unknown>(`/api/run-requests/${id}/note`, { text });
export const previewRunRequest = (b: RunRequestFile) => postJSON<RunRequestPreview>("/api/run-requests/preview", b);
export const declineRunRequest = (id: number, note: string) => postJSON<Schemas["RelayState"]>(`/api/run-requests/${id}/decline`, { note });

// local instance
export const relayLocal = () => getJSON<RelayLocal>("/api/relay/local");
export const setRelayInbox = (inbox: RelayInbox, hosts: Record<string, number>) => putJSON<RelayLocal>("/api/relay/local/inbox", { inbox, hosts });
export const pollRelay = () => postJSON<RelayLocal>("/api/relay/local/poll");
export const getRelayed = (id: number) => getJSON<RelayedDetail>(`/api/relayed/${id}`);
export const draftRelayed = (id: number, b: RelayedDraft) => postJSON<Relayed>(`/api/relayed/${id}/draft`, b);
export const approveRelayed = (id: number, b: RelayedApprove) => postJSON<RelayedApproved>(`/api/relayed/${id}/approve`, b);
export const declineRelayed = (id: number, note: string) => postJSON<Relayed>(`/api/relayed/${id}/decline`, { note });
export const detachRelayed = (id: number) => postJSON<Relayed>(`/api/relayed/${id}/detach`);
// standing approvals (ADR 0074), local instance
export const listGrants = () => getJSON<GrantList>("/api/relay/local/grants");
export const createGrant = (b: GrantCreate) => postJSON<Grant>("/api/relay/local/grants", b);
export const revokeGrant = (id: string) => postJSON<Grant>(`/api/relay/local/grants/${id}/revoke`, {});
export const relayMembers = () => getJSON<Schemas["RelayMembers"]>("/api/relay/local/members").then((r) => r.members);

const STATE_TEXT: Record<RequestState, string> = {
  requested: "waiting for the key holder's computer to fetch it",
  waiting_review: "waiting for the key holder to review",
  approved: "approved",
  submitted: "submitted",
  running: "running",
  finished: "finished, results not yet checked",
  failed: "failed",
  unknown: "unknown",
  declined: "declined",
  withdrawn: "withdrawn",
  expired: "expired",
};
export const stateText = (s: RequestState) => STATE_TEXT[s];
/** States in which the request can still be withdrawn or declined (before approval). */
export const isOpen = (s: RequestState) => s === "requested" || s === "waiting_review";

/** One bound as the requester reads it: "min 1, max 16" or "one of a, b". */
export function boundText(b: Bound): string {
  if (b.choices?.length) return `one of ${b.choices.join(", ")}`;
  const parts: string[] = [];
  if (b.min !== undefined) parts.push(`min ${b.min}`);
  if (b.max !== undefined) parts.push(`max ${b.max}`);
  return parts.join(", ") || "any value";
}

/** Why a request that named a standing approval waits for the key holder, in the server's fixed words (relay.GrantCodeText). */
const GRANT_TEXT: Record<GrantState, string> = {
  "": "",
  waiting: "waiting for a free slot under the standing approval",
  pending: "the group server did not answer the claim yet; tried again at the next check",
  used: "submitted without a click under the standing approval",
  stopped: "claimed under the standing approval, but not submitted (see the note)",
  unknown: "the standing approval it named is not one this computer has for the requester",
  revoked: "the standing approval was revoked",
  expired: "the standing approval has expired",
  stale: "filed before the standing approval was made, or no longer open",
  outside: "it asks for settings or resources outside the standing approval",
  grid: "a grid always waits for you",
  base: "its base run needs you to choose it",
  input: "the base run's input is outside the standing approval",
  commit: "the draft would run another code commit than the standing approval allows",
  budget: "the standing approval's budget is used up",
  edited: "you changed the draft",
};
export const grantStateText = (g: GrantState | undefined) => (g ? GRANT_TEXT[g] : "");
/** A grant state that sends the request back to the key holder (a code, not waiting, pending or used). */
export const grantFellBack = (g: GrantState | undefined) => !!g && g !== "waiting" && g !== "pending" && g !== "used";

/** Core-hours with one decimal, without a trailing .0. */
export const hours = (h: number) => (Math.round(h * 10) / 10).toString();
