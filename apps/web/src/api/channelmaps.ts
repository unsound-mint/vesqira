import { ApiError, requestRaw } from "./http";
import type { components } from "./schema";
import type { Plane, PlaneHeader } from "./planes";
import { parsePlane } from "./planes";

type Schemas = components["schemas"];

/** The plan of a channel-map grid (ADR 0049): channels and spectral labels of each panel, the caps, and the one shared scale. */
export type ChannelGridPlan = Schemas["ChannelGridPlan"];
export type GridPanel = ChannelGridPlan["panels"][number];
/** The stored description of a grid (channel-grid/1). */
export type ChannelGridSpec = Schemas["ChannelGrid"];
export type PvHeader = Schemas["PvHeader"];
export type SlabHeader = Schemas["SlabHeader"];

export type { LineShape } from "./planes";

export type GridRequest = { first: number; last: number; stride: number; average: number; unit: string; convention: string; limits: [number, number] | null };

const base = (id: number) => `/api/datasets/${id}`;

export function getChannelGrid(id: number, g: GridRequest, signal?: AbortSignal): Promise<ChannelGridPlan> {
  const q = new URLSearchParams({ first: String(g.first), last: String(g.last), stride: String(g.stride), average: String(g.average) });
  if (g.unit) q.set("unit", g.unit);
  if (g.unit && g.convention) q.set("convention", g.convention);
  if (g.limits) q.set("limits", `${g.limits[0]},${g.limits[1]}`);
  return requestRaw(`${base(id)}/channel-grid?${q}`, { signal }).then((r) => r.json() as Promise<ChannelGridPlan>);
}

/** One panel: a plane (one channel), or the mean of several channels, at the plan's preview step. */
export function getPanel(id: number, p: { first: number; last: number }, step: number, signal?: AbortSignal): Promise<Plane & { slab: SlabHeader["slab"] | null }> {
  const single = p.first === p.last;
  const url = single
    ? `${base(id)}/plane?channel=${p.first}${step > 1 ? `&step=${step}` : ""}`
    : `${base(id)}/slab?first=${p.first}&last=${p.last}${step > 1 ? `&step=${step}` : ""}`;
  return requestRaw(url, { signal })
    .then((r) => r.arrayBuffer())
    .then((buf) => {
      const plane = parsePlane(buf);
      return { ...plane, slab: single ? null : ((plane.head as PlaneHeader & Partial<SlabHeader>).slab ?? null) };
    });
}

/** A position-velocity diagram: values[channel * offsets + offset] (NaN = no value), counts of finite samples averaged in. */
export type PvDiagram = { head: PvHeader; offsets: Float64Array; axis: Float64Array; values: Float32Array; counts: Uint8Array };

/** Frame: uint32 LE header length, JSON header (space padded), then float64 offsets, float64 axis, float32 values, uint8 counts. */
export function parsePv(buf: ArrayBuffer): PvDiagram {
  const dv = new DataView(buf);
  if (buf.byteLength < 4) throw new ApiError("The position-velocity response is malformed.");
  const n = dv.getUint32(0, true);
  if (4 + n > buf.byteLength) throw new ApiError("The position-velocity response is malformed.");
  const head = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n))) as PvHeader;
  const no = head.offsets, nc = head.channels_returned;
  const cells = no * nc;
  const at = 4 + n;
  if (!Number.isInteger(cells) || no < 1 || nc < 1 || buf.byteLength - at !== 8 * no + 8 * nc + 5 * cells) throw new ApiError("The position-velocity response has the wrong size.");
  const f64 = (off: number, count: number) => {
    const out = new Float64Array(count);
    for (let i = 0; i < count; i++) out[i] = dv.getFloat64(off + i * 8, true);
    return out;
  };
  const values = new Float32Array(cells);
  const vAt = at + 8 * no + 8 * nc;
  for (let i = 0; i < cells; i++) values[i] = dv.getFloat32(vAt + i * 4, true);
  return { head, offsets: f64(at, no), axis: f64(at + 8 * no, nc), values, counts: new Uint8Array(buf.slice(vAt + 4 * cells, vAt + 5 * cells)) };
}

export type PvRequest = { points: [number, number][]; width: number; widthUnit: "pixel" | "arcsec"; interpolation: "linear" | "nearest"; first?: number; last?: number; unit?: string; convention?: string };

export function getPv(id: number, r: PvRequest, signal?: AbortSignal): Promise<PvDiagram> {
  return requestRaw(`${base(id)}/pv`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(r), signal })
    .then((x) => x.arrayBuffer())
    .then(parsePv);
}
