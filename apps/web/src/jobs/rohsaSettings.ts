import { readSetting } from "../api/jobs";

/**
 * The settings of a ROHSA config that a person may change (the server's whitelist is authoritative: jobs.ROHSAEditableKeys). The
 * input cube and the output folder are not here: the output folder is Vesqira's per job, so a repeated run never writes over an
 * earlier one.
 */
export type RohsaSetting = { key: string; label: string; kind: "text" | "bool" | "kernel" };

export const ROHSA_SETTINGS: RohsaSetting[] = [
  { key: "n_gauss", label: "n_gauss (Gaussians per pixel)", kind: "text" },
  { key: "lambda_amp", label: "lambda_amp (smoothing, amplitude)", kind: "text" },
  { key: "lambda_mu", label: "lambda_mu (smoothing, centre)", kind: "text" },
  { key: "lambda_sig", label: "lambda_sig (smoothing, width)", kind: "text" },
  { key: "lambda_var_amp", label: "lambda_var_amp", kind: "text" },
  { key: "lambda_var_mu", label: "lambda_var_mu", kind: "text" },
  { key: "lambda_var_sig", label: "lambda_var_sig (width term)", kind: "text" },
  { key: "lb_sig", label: "lb_sig (width lower bound, channels)", kind: "text" },
  { key: "ub_sig", label: "ub_sig (width upper bound, channels)", kind: "text" },
  { key: "sig_init", label: "sig_init", kind: "text" },
  { key: "lb_sig_init", label: "lb_sig_init", kind: "text" },
  { key: "ub_sig_init", label: "ub_sig_init", kind: "text" },
  { key: "maxiter", label: "maxiter (iteration limit)", kind: "text" },
  { key: "maxiter_init", label: "maxiter_init", kind: "text" },
  { key: "noise", label: "noise (use a noise estimate)", kind: "bool" },
  { key: "spectral_kernel", label: "spectral_kernel", kind: "kernel" },
  { key: "crop_size", label: "crop_size (0 = whole cube)", kind: "text" },
  { key: "crop_x0", label: "crop_x0", kind: "text" },
  { key: "crop_y0", label: "crop_y0", kind: "text" },
];

export const KERNELS = ["none", "hanning", "gauss1", "gauss2"];

/** The tied setting of a sweep: lambda_amp = lambda_mu = lambda_sig together, as the owner's `smoothNNN` configs do. */
export const ROHSA_SWEEP_KEYS: { key: string; label: string }[] = [
  { key: "lambda", label: "lambda (amp, mu and sig together)" },
  ...ROHSA_SETTINGS.map((x) => ({ key: x.key, label: x.key })),
];

/** The same quantity written two ways ("10" and "10.0") is not a change. */
export function sameValue(a: string, b: string): boolean {
  const x = a.trim();
  const y = b.trim();
  if (x === y) return true;
  const nx = Number(x);
  const ny = Number(y);
  return x !== "" && y !== "" && Number.isFinite(nx) && Number.isFinite(ny) && nx === ny;
}

/**
 * A setting read from a config's text, for the "was" of a field only: the first line that has the key, as a person would type
 * it ("[16]" is 16). The server reads the config properly when it renders the job and is the authority.
 */
export function readRohsaSetting(text: string, key: string): string {
  const v = readSetting(text, key);
  return v.replace(/^\[\s*/, "").replace(/\s*\]$/, "").split(",")[0]!.trim();
}
