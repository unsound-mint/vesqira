/**
 * ROHSA Gaussian components in the spectrum view (ADR 0046): the small pieces of arithmetic and policy the panel
 * shows. The curves, the channel to km/s conversion and the 1-based mean are done in the worker; nothing here
 * re-derives them.
 */

/** The pipeline's scan script default (--cnm-sigma 2.03 km/s; T_D = 121.1 sigma^2 < 500 K). A starting value, not a result. */
export const PIPELINE_CNM_SIGMA_KMS = 2.03;

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
export const T_DOPPLER_NOTE = "T_D = 121.1 K (km/s)⁻² × σ²: the pipeline's constant, m_H σ² / k_B for atomic hydrogen, an upper limit on the kinetic temperature (thermal broadening only)";
