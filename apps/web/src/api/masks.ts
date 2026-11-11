import { postJSON as post, requestRaw } from "./http";

import type { components } from "./schema";

const call = (url: string, signal?: AbortSignal) => requestRaw(url, { signal });

/** A typed mask (ADR 0055): a derived file flagging pixels of a dataset for a stated reason. It changes nothing in the dataset. */
export type Mask = components["schemas"]["Mask"];
export type MaskType = Mask["type"];
export type MaskPolicy = "exclude" | "keep" | "weight";
export type MaskUse = { id: number; policy: MaskPolicy; weight?: number | null };
export type CreateMask = components["schemas"]["CreateMaskRequest"];

export const MASK_TYPES: MaskType[] = ["missing", "invalid", "low_snr", "saturated", "user_excluded", "other"];

/** The name of a type in a sentence. */
export const maskTypeLabel = (t: MaskType): string =>
  ({ missing: "No value", invalid: "Invalid", low_snr: "Low signal-to-noise", saturated: "Saturated", user_excluded: "Excluded by you", other: "Other" })[t];

export const listMasks = (datasetId: number, signal?: AbortSignal) => call(`/api/datasets/${datasetId}/masks`, signal).then((r) => r.json() as Promise<{ masks: Mask[] }>).then((r) => r.masks);
export const createMask = (datasetId: number, body: CreateMask) => post<Mask>(`/api/datasets/${datasetId}/masks`, body);
export const forgetMask = (id: number) => post<{ removed: number }>(`/api/masks/${id}/delete`, {});

/** One plane of a mask: uint8, row 0 = FITS y 1, 1 = flagged. */
export type MaskPlane = { width: number; height: number; data: Uint8Array };

export function parseMaskPlane(buf: ArrayBuffer): MaskPlane {
  const dv = new DataView(buf);
  const n = dv.getUint32(0, true);
  if (4 + n > buf.byteLength) throw new Error("The mask response is malformed.");
  const head = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n))) as { width: number; height: number };
  if (buf.byteLength - 4 - n !== head.width * head.height) throw new Error("The mask response has the wrong size.");
  return { width: head.width, height: head.height, data: new Uint8Array(buf, 4 + n, head.width * head.height) };
}

export const getMaskPlane = (id: number, channel: number, signal?: AbortSignal) =>
  call(`/api/masks/${id}/plane?channel=${channel}`, signal).then((r) => r.arrayBuffer()).then(parseMaskPlane);
