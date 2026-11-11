import type { Controls } from "../api/runs";

export const KERNEL_LABEL: Record<string, string> = {
  gaussian_fwhm: "Gaussian, width is the FWHM",
  gaussian_sigma: "Gaussian, width is σ",
  boxcar: "Boxcar",
  other: "Other kernel",
};
export const NOISE_LABEL: Record<string, string> = {
  scalar_sigma: "One σ for the whole map",
  rms_map: "Per-pixel rms map",
  per_channel_rms: "One rms per channel",
  other: "Other noise model",
};
export const CONSTRUCTION_LABEL: Record<string, string> = {
  shifted_map_null: "Shifted-map null",
  injection_test: "Injection test",
  permutation: "Permutation",
  other: "Other construction",
};
export const WIDTH_UNITS = ["pixel", "arcsec", "arcmin", "deg", "channel", "km/s"] as const;

export type ControlLine = { key: "smoothing" | "noise" | "construction"; label: string; text: string; basis: "declared" | "verified"; stated: string };

/** Who said it, in words: "declared by the researcher" never reads like a check, and "run by Vesqira" says why it is verified. */
export function statedText(b: { basis: string; by: string; at: string }): string {
  const day = b.at ? ` on ${b.at.slice(0, 10)}` : "";
  return b.basis === "verified" ? `verified: run by Vesqira${day}, recorded as it ran` : `declared by the ${b.by}${day}, not checked`;
}

/** The blocks that were stated, as lines for a run page, a reviewer's run line or a derived result. A block that was not stated is left out: it is not "none applied". */
export function controlLines(c: Controls | null | undefined): ControlLine[] {
  const out: ControlLine[] = [];
  if (!c) return out;
  if (c.smoothing) {
    const k = c.smoothing;
    const width = k.width === null || k.width === undefined ? "width not stated" : `${k.width} ${k.unit}`;
    out.push({ key: "smoothing", label: "Smoothing", text: `${KERNEL_LABEL[k.kernel] ?? k.kernel}, ${width}${k.note ? `. ${k.note}` : ""}`, basis: k.basis, stated: statedText(k) });
  }
  if (c.noise) {
    const n = c.noise;
    const sigma = n.sigma === null || n.sigma === undefined ? "" : ` σ = ${n.sigma} ${n.sigmaUnit}`;
    const how = n.estimated ? `. Estimated: ${n.estimated}` : ". How it was estimated is not stated";
    out.push({ key: "noise", label: "Noise model", text: `${NOISE_LABEL[n.kind] ?? n.kind}${sigma}${how}${n.note ? `. ${n.note}` : ""}`, basis: n.basis, stated: statedText(n) });
  }
  if (c.construction) {
    const k = c.construction;
    const params = k.params.length > 0 ? ` (${k.params.map((p) => `${p.key} = ${p.value}`).join(", ")})` : "";
    out.push({ key: "construction", label: "How the control was built", text: `${CONSTRUCTION_LABEL[k.kind] ?? k.kind}${params}${k.note ? `. ${k.note}` : ""}`, basis: k.basis, stated: statedText(k) });
  }
  return out;
}
