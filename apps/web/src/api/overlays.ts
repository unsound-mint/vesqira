import { ApiError, requestRaw } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];
export type OverlayGeometryHead = Schemas["OverlayGeometryHeader"];

/** One polyline in BASE pixels (0-based, pixel centres at integers, y up): x0, y0, x1, y1, ... */
export type GeometryPath = { group: number; xy: Float32Array };

/**
 * Overlay geometry as the worker placed it on the base (ADR 0042, section 3). contours, beam and grid are polylines (group
 * is the level index, 0 for the beam, 0/1 for lines of constant longitude/latitude); orientation is segments, four numbers
 * each: x, y, angle (degrees counter-clockwise from the base's +x pixel axis), weight in [0, 1] (NaN: reliability unknown).
 */
export type OverlayGeometry =
  | { kind: "contours" | "beam" | "grid"; head: OverlayGeometryHead; paths: GeometryPath[] }
  | { kind: "orientation"; head: OverlayGeometryHead; segments: Float32Array };

/** Frame: uint32 LE header length, JSON header (space padded to 8 bytes), then little-endian float32 as the header says. */
export function parseGeometry(buf: ArrayBuffer): OverlayGeometry {
  const dv = new DataView(buf);
  const n = dv.getUint32(0, true);
  if (4 + n > buf.byteLength) throw new ApiError("The overlay response is malformed.");
  const head = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n))) as OverlayGeometryHead;
  const count = (buf.byteLength - 4 - n) / 4;
  if (!Number.isInteger(count)) throw new ApiError("The overlay response has the wrong size.");
  // copied out (never a view), so an unaligned header cannot break the read and the numbers are always little-endian
  const nums = new Float32Array(count);
  for (let i = 0; i < count; i++) nums[i] = dv.getFloat32(4 + n + i * 4, true);
  if (head.kind === "orientation") {
    if (count !== 4 * (head.segments ?? -1)) throw new ApiError("The overlay response has the wrong size.");
    return { kind: "orientation", head, segments: nums };
  }
  const np = head.paths ?? -1;
  if (np < 0 || count !== 2 * np + 2 * (head.vertices ?? -1)) throw new ApiError("The overlay response has the wrong size.");
  const paths: GeometryPath[] = [];
  let at = 2 * np;
  for (let i = 0; i < np; i++) {
    const len = nums[2 * i + 1]!;
    paths.push({ group: nums[2 * i]!, xy: nums.subarray(at, at + 2 * len) });
    at += 2 * len;
  }
  if (at !== count) throw new ApiError("The overlay response has the wrong size.");
  return { kind: head.kind, head, paths };
}

export function getOverlayGeometry(baseId: number, overlay: Schemas["SceneOverlay"], spacing: number | null, signal?: AbortSignal): Promise<OverlayGeometry> {
  const body = { overlay, ...(spacing !== null ? { spacing } : {}) };
  return requestRaw(`/api/datasets/${baseId}/overlay-geometry`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal })
    .then((r) => r.arrayBuffer())
    .then(parseGeometry);
}
