/** One measured result of a run. A value that was not recorded is null and reads as unknown, never as zero. */
export type Metric = {
  key: string;
  value: number | null;
  unit: string;
  uncertainty: number | null;
  /** Empty when the run did not say what kind of uncertainty it is; it is not guessed. */
  uncertaintyKind: "" | "standard_deviation" | "standard_error" | "confidence_68" | "confidence_95" | "other";
  n: number | null;
  definition: string;
  status: "declared";
};

const KIND: Record<string, string> = {
  standard_deviation: "standard deviation",
  standard_error: "standard error",
  confidence_68: "68% interval half-width",
  confidence_95: "95% interval half-width",
  other: "other uncertainty",
};

const num = (x: number) => String(Number(x.toPrecision(6)));

/** "0.344 ± 0.02 (standard error), n = 2695", or "not recorded". The definition is shown separately. */
export function metricText(m: Metric): string {
  if (m.value === null) return "not recorded";
  let t = num(m.value);
  if (m.unit) t += ` ${m.unit}`;
  if (m.uncertainty !== null) t += ` ± ${num(m.uncertainty)}${m.uncertaintyKind ? ` (${KIND[m.uncertaintyKind] ?? m.uncertaintyKind})` : " (kind not stated)"}`;
  if (m.n !== null) t += `, n = ${m.n}`;
  return t;
}
