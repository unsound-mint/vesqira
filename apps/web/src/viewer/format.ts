import type { Plane, Provenance, SpectralFacts, View } from "../api/planes";
import { EM } from "../catalog/format";

const trim = (n: number, digits: number) => n.toLocaleString("en-US", { maximumFractionDigits: digits, useGrouping: false });

/** A spectral world value in a readable unit. Display conversion only: m/s to km/s, Hz to MHz. */
export function spectralText(value: number | null, unit: string | null): string {
  if (value === null || !Number.isFinite(value)) return EM;
  if (unit === "m/s") return `${trim(value / 1000, 4)} km/s`;
  if (unit === "Hz") return `${trim(value / 1e6, 6)} MHz`;
  return unit ? `${trim(value, 6)} ${unit}` : trim(value, 6);
}

/** A brightness value. NaN and infinities are "no value", zero is a number. */
export function valueText(v: number | null, bunit: string | null): string {
  if (v === null) return EM;
  if (!Number.isFinite(v)) return "no value";
  const n = Number(v.toPrecision(7)).toString();
  return bunit ? `${n} ${bunit}` : n;
}

export const degrees = (v: number) => `${v.toFixed(5)}°`;

export function provenanceNote(p: Provenance): string {
  return p === "declared" ? "" : p === "inferred" ? " (unit inferred from the CTYPE standard)" : " (unresolved)";
}

export type FlagState = { tone: "neutral" | "warn"; text: string };

/** Channel flags exist only if the file declares them. Absent is never "valid" and never "not flagged". */
export function flagState(view: View | null, plane: Plane | null): FlagState {
  if (!view || !plane) return { tone: "neutral", text: EM };
  if (!view.flags.declared) return { tone: "neutral", text: "No channel flags declared" };
  return plane.head.flagged ? { tone: "warn", text: "Channel flagged by provider" } : { tone: "neutral", text: "Channel not flagged" };
}

export function channelLabel(channel: number, count: number, s: SpectralFacts | null, value: number | null): string {
  const base = `Channel ${channel + 1} of ${count}`;
  return s && s.resolved && value !== null ? `${base}, ${s.ctype ?? "spectral"} ${spectralText(value, s.unit)}` : base;
}

/** Planes travel and display as float32. Say so when the file stores more precision than that. */
export function precisionNote(bitpix: number | null): string {
  return bitpix === 32 || bitpix === 64 || bitpix === -64 ? ` Shown as float32; the file stores BITPIX ${bitpix}.` : "";
}
