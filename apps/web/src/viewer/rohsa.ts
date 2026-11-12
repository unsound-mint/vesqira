/**
 * ROHSA Gaussian components in the spectrum view (ADR 0046): the small pieces of arithmetic and policy the panel
 * shows. The curves, the channel to km/s conversion and the 1-based mean are done in the worker; nothing here
 * re-derives them.
 */

/**
 * Doppler temperature of a Gaussian of width sigma (km/s, one-dimensional dispersion) for atomic hydrogen: T_D = m_H sigma^2 / k_B,
 * 121.2 K per (km/s)^2 from m_H = 1.6735e-27 kg and k_B = 1.380649e-23 J/K, the constant the worker uses too (rohsa.DOPPLER_K_PER_KMS2).
 * Results before 2026-11-13 used 121.1. Thermal broadening only, so T_D is an upper limit on the kinetic temperature.
 */
export const DOPPLER_K_PER_KMS2 = 121.2;
export const tdOfSigma = (sigmaKms: number): number => DOPPLER_K_PER_KMS2 * sigmaKms * sigmaKms;
export const sigmaOfTd = (tdK: number): number => Math.sqrt(tdK / DOPPLER_K_PER_KMS2);

/** A common upper bound on the kinetic temperature of the cold neutral medium. The starting cut is this bound as a width. */
export const CNM_TD_MAX_K = 500;
/** sigma = sqrt(500 / 121.2) = 2.0311 km/s, cut to two decimals: 2.03 km/s, the value earlier results and class maps used. A starting value, not a result. */
export const CNM_SIGMA_DEFAULT_KMS = Number(sigmaOfTd(CNM_TD_MAX_K).toFixed(2));

/** What the width cut is typed in. */
export type CutBasis = "sigma" | "td";

/** The cut as a width in km/s from the text typed in the chosen basis, or null when it is not a positive number. */
export function cutSigma(text: string, basis: CutBasis): number | null {
  const v = parseCut(text);
  return v === null ? null : basis === "sigma" ? v : sigmaOfTd(v);
}

/** The same cut in the other basis, as text (six figures, so that switching back and forth does not drift). */
export function cutText(sigmaKms: number, basis: CutBasis): string {
  return String(Number((basis === "sigma" ? sigmaKms : tdOfSigma(sigmaKms)).toPrecision(6)));
}

export type Phase = "cnm" | "other";

/** A width cut the person typed: a positive, finite number, or null. */
export function parseCut(text: string): number | null {
  const t = text.trim();
  if (!/^\d*\.?\d+(e[+-]?\d+)?$/i.test(t)) return null;
  const v = Number(t);
  return Number.isFinite(v) && v > 0 ? v : null;
}

/**
 * Two phases only. CNM is sigma strictly below the cut, the pipeline's own rule (`s < cut`): a component whose width is exactly
 * the cut is "others". There is no further split of the others.
 */
export function classify(sigmaKms: number, cutKms: number): Phase {
  return sigmaKms < cutKms ? "cnm" : "other";
}

/**
 * Residual for display: data minus model, per channel. A channel with no data value has no residual (NaN), never zero.
 * The pipeline's own residual maps use the opposite sign (model minus data); the panel says which one it draws.
 */
export function residual(data: ArrayLike<number>, model: ArrayLike<number>): Float64Array {
  const out = new Float64Array(data.length);
  for (let i = 0; i < data.length; i++) out[i] = Number.isFinite(data[i]!) ? data[i]! - model[i]! : NaN;
  return out;
}

export const SIGN_NOTE = "residual = data − model (the pipeline's residual maps use model − data)";
export const T_DOPPLER_NOTE = "T_D = m_H σ² / k_B for atomic hydrogen, 121.2 K (km/s)⁻² × σ², an upper limit on the kinetic temperature (thermal broadening only)";
