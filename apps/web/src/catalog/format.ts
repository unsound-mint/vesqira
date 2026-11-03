/** Unknown is never zero: null renders as an em dash. */
export const EM = "—";

export function show(v: string | number | null | undefined): string {
  return v === null || v === undefined || v === "" ? EM : String(v);
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const u = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 2 : 1)} ${u[i]}`;
}
