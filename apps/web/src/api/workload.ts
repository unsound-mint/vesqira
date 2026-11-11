import { requestRaw } from "./http";
import type { components } from "./schema";

export type Workload = components["schemas"]["Workload"];
export type WorkloadClass = components["schemas"]["WorkloadClass"];
export type WorkloadRequest = components["schemas"]["WorkloadRequest"];

/** The work queue's state; with `id` (the X-Request-Id a slow request was sent with) also whether that request waits and why. */
export const getWorkload = (id?: string, signal?: AbortSignal) =>
  requestRaw(`/api/workload${id ? `?request=${encodeURIComponent(id)}` : ""}`, { signal }, { quiet: true }).then((r) => r.json() as Promise<Workload>);
