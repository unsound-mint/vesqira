/** Share of a limit used, in percent, rounded; 0 when there is no limit to take a share of. */
export function pct(used: number, of: number): number {
  return of > 0 ? Math.round((used / of) * 100) : 0;
}

/** From this share of a hard limit the meter turns to warning (and the sentence says so: the colour is never the only cue). */
export const QUOTA_WARN_PCT = 90;

/** Meter colour of a quota or file system: `warn` from 90 % of the limit, `ok` below it. */
export function quotaTone(used: number, limit: number): "ok" | "warn" {
  return limit > 0 && (used / limit) * 100 >= QUOTA_WARN_PCT ? "warn" : "ok";
}
