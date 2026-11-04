import { ApiError } from "./catalog";

export type Provenance = "declared" | "inferred" | "unresolved";

export type SpectralFacts = {
  fits_axis: number;
  ctype: string | null;
  name: string;
  is_spectral: boolean;
  resolved: boolean;
  unit: string | null;
  unit_status: Provenance;
  velocity: "declared" | "unresolved";
  note?: string;
  specsys?: string | null;
  restfrq?: number | null;
};

export type SkyFacts =
  | { kind: "pixel"; note: string }
  | { kind: "celestial"; lon_name: string; lat_name: string; projection: string | null; frame: string | null; frame_status: Provenance; unit: "deg" };

export type Flags = { declared: boolean; source?: string; channels: number[]; note?: string };

export type View = {
  width: number;
  height: number;
  channels: number;
  bunit: string | null;
  bunit_status: "declared" | "unresolved";
  fits_axes: { x: number; y: number; channel: number | null };
  sky: SkyFacts;
  spectral: SpectralFacts | null;
  flags: Flags;
};

export type PlaneStats = { finite_count: number; nonfinite_count: number; min: number | null; max: number | null; p01: number | null; p99: number | null; overflow_count: number };

export type PlaneHeader = {
  width: number;
  height: number;
  channel: number;
  channels: number;
  nan_meaning: string;
  stats: PlaneStats;
  spectral_value: number | null;
  spectral_unit: string | null;
  /** null means the file declares no flags at all, which is not the same as "not flagged". */
  flagged: boolean | null;
  source_bitpix: number | null;
  scaled: boolean;
};

/** Row 0 is FITS y = 1 (the bottom row when north is up). Non-finite values mean "no value", never zero. */
export type Plane = { head: PlaneHeader; data: Float32Array };

export type Coord = {
  x: number;
  y: number;
  channel: number;
  sky: { lon: number; lat: number } | null;
  spectral: { value: number | null } | null;
};

async function request(url: string, signal?: AbortSignal): Promise<Response> {
  let res: Response;
  try {
    res = await fetch(url, { signal });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError("Cannot reach the Vesqira API. Is it running on 127.0.0.1:8787?");
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new ApiError(body?.error ?? `Request failed (${res.status})`);
  }
  return res;
}

const base = (id: number) => `/api/datasets/${id}`;

export const getView = (id: number, signal?: AbortSignal) => request(`${base(id)}/view`, signal).then((r) => r.json() as Promise<View>);

export const getCoord = (id: number, x: number, y: number, channel: number, signal?: AbortSignal) =>
  request(`${base(id)}/coord?x=${x}&y=${y}&channel=${channel}`, signal).then((r) => r.json() as Promise<Coord>);

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

export const getPlane = (id: number, channel: number, signal?: AbortSignal) =>
  request(`${base(id)}/plane?channel=${channel}`, signal).then((r) => r.arrayBuffer()).then(parsePlane);

export type RegionShape = { shape: "rect"; x0: number; y0: number; x1: number; y1: number } | { shape: "circle"; cx: number; cy: number; r: number };

export type SpectrumHeader = {
  channels: number;
  region: { pixels: number; requested_pixels: number; clipped: boolean; bbox: { x0: number; y0: number; x1: number; y1: number } };
  size: { pixel_scale_arcsec: [number, number]; area_arcmin2: number; note: string } | null;
  axis: { kind: "spectral" | "channel"; name: string | null; unit: string | null };
  mask_policy: string;
  bunit: string | null;
};

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

export const getSpectrum = (id: number, r: RegionShape, signal?: AbortSignal) =>
  request(`${base(id)}/spectrum?${regionQuery(r)}`, signal).then((x) => x.arrayBuffer()).then(parseSpectrum);

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
