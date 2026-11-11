import { request as call, postJSON as post } from "./http";
import type { Dataset } from "./catalog";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** One entry of the transfer queue between this computer and a host (ADR 0048). */
export type Transfer = Schemas["Transfer"];
export type TransferVerification = Schemas["TransferVerification"];
export type TransferRequest = Schemas["TransferRequest"];
export type TransferPolicy = NonNullable<TransferRequest["policy"]>;

export const listTransfers = () => call<{ transfers: Transfer[] }>("/api/transfers").then((r) => r?.transfers ?? []);
/** Queues a transfer. Nothing is sent to the host until the queue runs it; both paths are authorised on the server. */
export const queueTransfer = (r: TransferRequest) => post<{ transfer: Transfer }>("/api/transfers", r).then((x) => x.transfer);
export const cancelTransfer = (id: number, discardPartial = false) => post<{ transfer: Transfer }>(`/api/transfers/${id}/cancel`, { discardPartial }).then((x) => x.transfer);
export const retryTransfer = (id: number) => post<{ transfer: Transfer }>(`/api/transfers/${id}/retry`, {}).then((x) => x.transfer);
export const resolveTransfer = (id: number, policy: Exclude<TransferPolicy, "ask">) => post<{ transfer: Transfer }>(`/api/transfers/${id}/resolve`, { policy }).then((x) => x.transfer);
export const removeTransfer = (id: number) => post<{ removed: boolean }>(`/api/transfers/${id}/remove`, {});
export const registerTransfer = (id: number) => post<{ dataset: Dataset; transfer: Transfer }>(`/api/transfers/${id}/register`, {});
