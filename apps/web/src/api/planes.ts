import { ApiError, idHeader, requestRaw } from "./http";
import type { components } from "./schema";
import type { MaskUse } from "./masks";

type Schemas = components["schemas"];

export type Provenance = Schemas["SpectralFacts"]["unit_status"];
export type SpectralFacts = Schemas["SpectralFacts"];
export type SkyFacts = Schemas["View"]["sky"];
export type Flags = Schemas["Flags"];
export type View = Schemas["View"];
export type PlaneStats = Schemas["PlaneStats"];
export type PlaneHeader = Schemas["PlaneHeader"];

/** Row 0 is FITS y = 1 (the bottom row when north is up). Non-finite values mean "no value", never zero. */
export type Plane = { head: PlaneHeader; data: Float32Array };

export type Coord = {
  x: number;
  y: number;
  channel: number;
  sky: { lon: number; lat: number } | null;
  spectral: { value: number | null } | null;
};

/** What the file allows (ADR 0040): facts only; the browser offers what is enabled and says why the rest is not. */
export type ConvTarget = { unit: string; enabled: boolean; reason: string | null; kind?: string; needs_convention?: boolean; cross?: boolean };
export type Conversions = {
  spectral: {
    native_unit: string | null;
    native_kind: "freq" | "wave" | "vel" | null;
    ctype: string | null;
    file_convention: string | null;
    specsys: string | null;
    rest: { value: number; unit: string; source: string } | null;
    reference_frame_note: string;
    targets: ConvTarget[];
  } | null;
  sky: { frame: string | null; frame_status: "declared" | "inferred" | "assumed"; source: string | null; refused: string | null; targets: { id: string; label: string; enabled: boolean; reason: string | null }[] } | null;
  value: { unit: string | null; unit_status: string; targets: ConvTarget[]; reason: string | null };
};

type Part<N, C> = { native: N | null; converted: C | null; refused: string | null; basis: string | null };
export type Converted = {
  x: number;
  y: number;
  channel: number;
  spectral: (Part<{ value: number | null; unit: string; unit_status: string }, { value: number; unit: string }> & {
    convention?: string | null; convention_source?: "file" | "user" | null; rest?: { value: number; unit: string; source: string } | null; specsys?: string | null; reference_frame_note?: string;
  }) | null;
  sky: (Part<{ lon: number; lat: number; frame: string; frame_status: string; lon_name: string; lat_name: string }, { lon: number; lat: number; frame: string; lon_name: string; lat_name: string }> & { frame_note?: string | null }) | null;
  value: Part<{ value: number | null; unit: string | null; unit_status: string }, { value: number; unit: string; unit_status: string }> | null;
};
export type ConvertRequest = { spectral: string; convention: string; frame: string; to: string; value: number | null };

/** The checked Response: these bodies are binary (planes, spectra) or JSON, and the caller reads them. */
const request = (url: string, signal?: AbortSignal) => requestRaw(url, { signal });

const base = (id: number) => `/api/datasets/${id}`;

export const getView = (id: number, signal?: AbortSignal) => request(`${base(id)}/view`, signal).then((r) => r.json() as Promise<View>);

export const getCoord = (id: number, x: number, y: number, channel: number, signal?: AbortSignal) =>
  request(`${base(id)}/coord?x=${x}&y=${y}&channel=${channel}`, signal).then((r) => r.json() as Promise<Coord>);

export const getConversions = (id: number, signal?: AbortSignal) => request(`${base(id)}/conversions`, signal).then((r) => r.json() as Promise<Conversions>);

export function getConvert(id: number, x: number, y: number, channel: number, c: ConvertRequest, signal?: AbortSignal) {
  const q = new URLSearchParams({ x: String(x), y: String(y), channel: String(channel) });
  if (c.spectral) q.set("spectral", c.spectral);
  if (c.spectral && c.convention) q.set("convention", c.convention);
  if (c.frame) q.set("frame", c.frame);
  if (c.to) q.set("to", c.to);
  if (c.to && c.value !== null && Number.isFinite(c.value)) q.set("value", String(c.value));
  return request(`${base(id)}/convert?${q}`, signal).then((r) => r.json() as Promise<Converted>);
}

const LITTLE_ENDIAN = new Uint8Array(new Uint16Array([1]).buffer)[0] === 1;

/** Frame: uint32 LE header length, JSON header (space padded), then width*height float32 LE. */
export function parsePlane(buf: ArrayBuffer): Plane {
  const dv = new DataView(buf);
  const n = dv.getUint32(0, true);
  if (4 + n > buf.byteLength) throw new ApiError("The plane response is malformed.");
  const head = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n))) as PlaneHeader;
  const count = head.width * head.height;
  if (!Number.isInteger(count) || count < 0 || buf.byteLength - 4 - n !== count * 4) throw new ApiError("The plane response has the wrong size.");
  let data: Float32Array;
  if (LITTLE_ENDIAN && (4 + n) % 4 === 0) {
    data = new Float32Array(buf, 4 + n, count);
  } else {
    data = new Float32Array(count);
    for (let i = 0; i < count; i++) data[i] = dv.getFloat32(4 + n + i * 4, true);
  }
  return { head, data };
}

export const getPlane = (id: number, channel: number, signal?: AbortSignal, step = 1) =>
  request(`${base(id)}/plane?channel=${channel}${step > 1 ? `&step=${step}` : ""}`, signal).then((r) => r.arrayBuffer()).then(parsePlane);

/** Reduction factor for a first look at a large plane: about a quarter of a megapixel, never below 2. */
export function previewStep(width: number, height: number): number {
  return Math.min(64, Math.max(2, Math.ceil(Math.sqrt((width * height) / 262144))));
}

/**
 * A reduced preview drawn on the full grid (each block repeated over its pixels), so every pixel index, readout and region
 * keeps meaning the same cube pixel while the full plane loads. Values stay the block means; `head.preview` stays set so the
 * viewer can say it is a preview. NaN blocks stay NaN.
 */
export function expandPreview(p: Plane): Plane {
  const pv = p.head.preview;
  if (!pv) return p;
  const { step, source_width: w, source_height: h } = pv;
  const sw = p.head.width;
  const data = new Float32Array(w * h);
  for (let y = 0; y < h; y++) {
    const row = Math.floor(y / step) * sw;
    for (let x = 0; x < w; x++) data[y * w + x] = p.data[row + Math.floor(x / step)] ?? Number.NaN;
  }
  return { head: { ...p.head, width: w, height: h }, data };
}

/** An area: a rectangle (inclusive pixel bounds) or a circle. Spectra, histograms and orientation statistics are taken over these. */
export type RegionShape = { shape: "rect"; x0: number; y0: number; x1: number; y1: number } | { shape: "circle"; cx: number; cy: number; r: number };

/** A path of whole-pixel vertices (0-based, y from the bottom) with a width across it: the cut of a position-velocity diagram. It has no area. */
export type LineShape = { shape: "line"; points: [number, number][]; width: number; widthUnit: "pixel" | "arcsec" };

export type SpectrumHeader = {
  channels: number;
  region: { pixels: number; requested_pixels: number; clipped: boolean; bbox: { x0: number; y0: number; x1: number; y1: number } };
  size: { pixel_scale_arcsec: [number, number]; area_arcmin2: number; note: string } | null;
  axis: { kind: "spectral" | "channel"; name: string | null; unit: string | null };
  mask_policy: string;
  bunit: string | null;
  /** declared: the file's BUNIT; user: a unit you declared for a file with none; unresolved: neither */
  bunit_status?: "declared" | "user" | "unresolved";
  /** Present when masks were applied (ADR 0055): each with its policy and counts, the region's pixel-channels with no value, and the ones with a value that masks left out. */
  masks?: AppliedRegionMask[];
  no_value?: number;
  excluded_by_mask?: number;
};

export type AppliedRegionMask = Schemas["RegionMaskApplied"];

/** mean[c] is NaN when the channel has no finite pixel in the region: unknown, never zero. */
export type Spectrum = { head: SpectrumHeader; axis: Float64Array; mean: Float64Array; count: Uint32Array };

export function regionQuery(r: RegionShape): string {
  return r.shape === "rect" ? `shape=rect&x0=${r.x0}&y0=${r.y0}&x1=${r.x1}&y1=${r.y1}` : `shape=circle&cx=${r.cx}&cy=${r.cy}&r=${r.r}`;
}

export function parseSpectrum(buf: ArrayBuffer): Spectrum {
  const dv = new DataView(buf);
  const n = dv.getUint32(0, true);
  if (4 + n > buf.byteLength) throw new ApiError("The spectrum response is malformed.");
  const head = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n))) as SpectrumHeader;
  const c = head.channels;
  if (!Number.isInteger(c) || c < 0 || buf.byteLength - 4 - n !== c * 20) throw new ApiError("The spectrum response has the wrong size.");
  const at = 4 + n;
  const f64 = (off: number) => {
    const out = new Float64Array(c);
    for (let i = 0; i < c; i++) out[i] = dv.getFloat64(off + i * 8, true);
    return out;
  };
  const count = new Uint32Array(c);
  for (let i = 0; i < c; i++) count[i] = dv.getUint32(at + c * 16 + i * 4, true);
  return { head, axis: f64(at), mean: f64(at + c * 8), count };
}

/** `&mask=<id>:<policy>` for each mask an operation names; none named, nothing is applied. */
export const maskQuery = (uses: MaskUse[] = []): string => uses.map((u) => `&mask=${u.id}:${u.policy}`).join("");

export const getSpectrum = (id: number, r: RegionShape, signal?: AbortSignal, requestId?: string, masks?: MaskUse[]) =>
  requestRaw(`${base(id)}/spectrum?${regionQuery(r)}${maskQuery(masks)}`, { signal, headers: idHeader(requestId) }).then((x) => x.arrayBuffer()).then(parseSpectrum);

export type RegionHistogramResult = Schemas["RegionHistogram"];

/** The histogram of a region of one channel with masks applied, counted by the worker (the browser has no mask bytes). */
export const getRegionHistogram = (id: number, channel: number, r: RegionShape, min: number, max: number, bins: number, masks: MaskUse[], signal?: AbortSignal) =>
  request(`${base(id)}/region-histogram?channel=${channel}&min=${min}&max=${max}&bins=${bins}&${regionQuery(r)}${maskQuery(masks)}`, signal).then((x) => x.json() as Promise<RegionHistogramResult>);

export type AxialSummary = { n: number; resultant: number | null; mean_deg: number | null; defined: boolean; reason: string | null };
export type AxialStats = {
  values_fit_radians: boolean;
  pixels: number;
  angles_finite: number;
  angles_excluded: number;
  outside_zero_to_180: number;
  unweighted: AxialSummary;
  weighted: (AxialSummary & { unknown_weight_excluded: number }) | null;
};

export const getAxial = (id: number, r: RegionShape | null, signal?: AbortSignal) =>
  request(`${base(id)}/axial${r ? `?${regionQuery(r)}` : ""}`, signal).then((x) => x.json() as Promise<{ stats: AxialStats; method: string }>);
