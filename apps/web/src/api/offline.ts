import { getJSON, postJSON, putJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** What to prepare for offline (ADR 0059): datasets or a board, a channel range, how much detail, a byte budget. */
export type OfflineRequest = Schemas["OfflineRequest"];
/** One item of the preparation, as works offline, works partially or needs the remote, with the reason in words. */
export type OfflineItem = Schemas["OfflineItem"];
export type OfflineStatus = OfflineItem["status"];
export type OfflinePlan = Schemas["OfflinePlan"];
export type OfflineOverview = Schemas["OfflineOverview"];
export type OfflineDataset = Schemas["OfflineDataset"];
export type OfflinePrepareState = Schemas["OfflinePrepareState"];

export const getOffline = (signal?: AbortSignal) => getJSON<OfflineOverview>("/api/offline", signal);
export const planOffline = (r: OfflineRequest, signal?: AbortSignal) => postJSON<OfflinePlan>("/api/offline/plan", r, signal);
export const startPrepare = (r: OfflineRequest) => postJSON<OfflinePrepareState>("/api/offline/prepare", r);
export const getPrepare = (signal?: AbortSignal) => getJSON<OfflinePrepareState>("/api/offline/prepare", signal);
export const cancelPrepare = () => postJSON<OfflinePrepareState>("/api/offline/prepare/cancel", {});
export const setOfflineCap = (capBytes: number) => putJSON<Schemas["OfflineSettings"]>("/api/offline/settings", { capBytes });
export const forgetOffline = (datasetId: number) => postJSON<Schemas["OfflineForgotten"]>("/api/offline/forget", { datasetId });
