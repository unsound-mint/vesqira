/**
 * The one way a length of time is written for reading: "45 s", "90 s", "12 min", "1 h 46 min", "1 day 2 h".
 * Under two minutes it is seconds; from there minutes, then hours and minutes, then days and hours. Never more precise
 * than the next unit up would round to, so a long span does not pretend to know its seconds.
 */
export function formatDuration(seconds: number): string {
  const t = Math.max(0, Math.round(seconds));
  if (t < 120) return `${t} s`;
  if (t < 3600) return `${Math.round(t / 60)} min`;
  if (t < 86400) {
    const h = Math.floor(t / 3600);
    const m = Math.round((t % 3600) / 60);
    // 59.6 min must not print as "0 h 60 min"
    if (m === 60) return `${h + 1} h`;
    return m > 0 ? `${h} h ${m} min` : `${h} h`;
  }
  let d = Math.floor(t / 86400);
  let h = Math.round((t % 86400) / 3600);
  if (h === 24) { d += 1; h = 0; }
  return `${d} ${d === 1 ? "day" : "days"}${h > 0 ? ` ${h} h` : ""}`;
}

/** A Slurm --time value in seconds ("90" minutes, "MM:SS", "HH:MM:SS", "D-HH", "D-HH:MM", "D-HH:MM:SS"), or null when it is not one. */
export function parseTime(text: string): number | null {
  const t = text.trim();
  let m = /^(\d+)-(\d+)(?::(\d+))?(?::(\d+))?$/.exec(t);
  if (m) return Number(m[1]) * 86400 + Number(m[2]) * 3600 + Number(m[3] ?? 0) * 60 + Number(m[4] ?? 0);
  m = /^(\d+)$/.exec(t);
  if (m) return Number(m[1]) * 60;
  m = /^(\d+):(\d+)$/.exec(t);
  if (m) return Number(m[1]) * 60 + Number(m[2]);
  m = /^(\d+):(\d+):(\d+)$/.exec(t);
  if (m) return Number(m[1]) * 3600 + Number(m[2]) * 60 + Number(m[3]);
  return null;
}

/** What a Slurm time limit means in words ("1-00:00:00" is "1 day"), or "" when it is empty or not a Slurm time. */
export function slurmLimitGloss(text: string): string {
  const s = parseTime(text);
  return s === null || text.trim() === "" ? "" : formatDuration(s);
}
