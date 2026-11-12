import type { SavedRegion } from "../api/regions";
import type { SubsetDefinition, SubsetInput } from "../api/subsets";

/** Pixel and channel numbers are shown from 1, like the cutout form; the API's are 0-based. */
const one = (v: number) => String(v + 1);

export type SubsetFields = {
  name: string;
  reason: string;
  area: "whole" | "box" | "region";
  x0: string; x1: string; y0: string; y1: string;
  regionId: string;
  step: string;
  spectral: "all" | "channels" | "world";
  c0: string; c1: string;
  lo: string; hi: string; unit: string; frame: string;
};

export const EMPTY_FIELDS: SubsetFields = { name: "", reason: "", area: "whole", x0: "", x1: "", y0: "", y1: "", regionId: "", step: "1", spectral: "all", c0: "", c1: "", lo: "", hi: "", unit: "km/s", frame: "" };

/** The form as it stands for a subset's current definition (an edit starts from it). A box that came from a region is a box again: the region may have changed since. */
export function fieldsOf(name: string, d: SubsetDefinition): SubsetFields {
  return {
    ...EMPTY_FIELDS, name,
    area: d.box ? "box" : "whole",
    ...(d.box ? { x0: one(d.box.x0), x1: one(d.box.x1), y0: one(d.box.y0), y1: one(d.box.y1) } : {}),
    step: String(d.step || 1),
    spectral: d.channels ? "channels" : d.world ? "world" : "all",
    ...(d.channels ? { c0: one(d.channels.c0), c1: one(d.channels.c1) } : {}),
    ...(d.world ? { lo: String(d.world.lo), hi: String(d.world.hi), unit: d.world.unit, frame: d.world.frame ?? "" } : {}),
  };
}

const whole = (t: string) => /^\d+$/.test(t.trim()) && Number(t) >= 1;

/** The request for the fields, or the sentence that says what is missing. The server checks every bound against the real file. */
export function inputOf(f: SubsetFields, expectVersion?: number): { input: SubsetInput } | { problem: string } {
  const out: SubsetInput = { name: f.name.trim(), reason: f.reason.trim() };
  if (!out.name) return { problem: "Give the subset a name." };
  if (!out.reason) return { problem: "Say why: a reason is kept with every version." };
  if (f.area === "box") {
    if (![f.x0, f.x1, f.y0, f.y1].every(whole)) return { problem: "The box needs four pixel numbers, counting from 1." };
    out.box = { x0: Number(f.x0) - 1, x1: Number(f.x1) - 1, y0: Number(f.y0) - 1, y1: Number(f.y1) - 1 };
  }
  if (f.area === "region") {
    if (!f.regionId) return { problem: "Choose a saved region." };
    out.regionId = Number(f.regionId);
  }
  if (!whole(f.step)) return { problem: "Keep every N-th pixel: N is a whole number from 1." };
  out.step = Number(f.step);
  if (f.spectral === "channels") {
    if (!whole(f.c0) || !whole(f.c1)) return { problem: "The channel range needs two channel numbers, counting from 1." };
    out.channels = { c0: Number(f.c0) - 1, c1: Number(f.c1) - 1 };
  }
  if (f.spectral === "world") {
    const lo = Number(f.lo), hi = Number(f.hi);
    if (f.lo.trim() === "" || f.hi.trim() === "" || !Number.isFinite(lo) || !Number.isFinite(hi)) return { problem: "The interval needs two numbers." };
    if (!f.unit.trim()) return { problem: "The interval needs its unit, for example km/s or MHz." };
    out.world = { lo, hi, unit: f.unit.trim(), ...(f.frame.trim() ? { frame: f.frame.trim() } : {}) };
  }
  if (expectVersion) out.expectVersion = expectVersion;
  return { input: out };
}

const ordinal = (n: number) => `${n}${n % 100 >= 11 && n % 100 <= 13 ? "th" : n % 10 === 1 ? "st" : n % 10 === 2 ? "nd" : n % 10 === 3 ? "rd" : "th"}`;

/** A definition in plain words, pixels and channels counted from 1 (the box is the source's pixels). */
export function describe(d: SubsetDefinition): string {
  const parts: string[] = [];
  parts.push(d.box ? `x ${one(d.box.x0)} to ${one(d.box.x1)}, y ${one(d.box.y0)} to ${one(d.box.y1)}${d.region ? ` (box of the region ${d.region.name})` : ""}` : "whole image");
  if (d.step > 1) parts.push(`every ${ordinal(d.step)} pixel`);
  if (d.channels) parts.push(`channels ${one(d.channels.c0)} to ${one(d.channels.c1)}`);
  else if (d.world) parts.push(`${d.world.lo} to ${d.world.hi} ${d.world.unit}${d.world.frame ? ` (${d.world.frame})` : ""}`);
  else parts.push("all channels");
  return parts.join("; ");
}

/** The saved regions a subset may take its box from: a line has no area. */
export const boxRegions = (rs: SavedRegion[]) => rs.filter((r) => r.shape.shape !== "line");
