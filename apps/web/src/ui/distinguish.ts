/**
 * Shortest labels that tell a list of names, or of paths, apart. Real names are long and share prefixes
 * (vgt_Tb_combined_..._v2p0_to_8p0kms, vgt_Tb_combined_..._vm2p0_to_2p0kms), so a row that cuts the end shows the same text five times.
 * Display only: the full name stays in the title and the accessible name.
 */

const ELLIPSIS = "…";
/** Where a cut may fall: after one of these, so a label starts at a whole word of the name. "=" is not one: a "key=value" stays whole. */
const BOUNDARY = /[_\-./\s·:,]/;
/** A common tail is only cut when it is long enough to be worth an ellipsis. */
const MIN_SUFFIX = 10;

const commonPrefix = (xs: string[]): number => {
  let n = xs[0]!.length;
  for (const x of xs) {
    let i = 0;
    while (i < n && x[i] === xs[0]![i]) i++;
    n = i;
  }
  return n;
};
const commonSuffix = (xs: string[]): number => {
  let n = xs[0]!.length;
  for (const x of xs) {
    let i = 0;
    while (i < n && x[x.length - 1 - i] === xs[0]![xs[0]!.length - 1 - i]) i++;
    n = i;
  }
  return n;
};

/**
 * Labels for names that mostly share a prefix: the part all of them share is elided ("…v2p0_to_8p0kms"), back to a word boundary. A long
 * shared tail is elided too. Names are never cut inside a word, and a name equal to another is returned whole (nothing tells them apart).
 */
export function distinguishNames(names: string[]): string[] {
  const uniq = [...new Set(names)];
  if (uniq.length < 2) return names.slice();
  let p = commonPrefix(uniq);
  while (p > 0 && !BOUNDARY.test(uniq[0]![p - 1]!)) p--;
  // a name may be the start of another: keep at least one character of each
  if (p >= Math.min(...uniq.map((u) => u.length))) p = 0;
  const rest = uniq.map((u) => u.slice(p));
  let sf = commonSuffix(rest);
  while (sf > 0 && !BOUNDARY.test(rest[0]![rest[0]!.length - sf]!)) sf--;
  if (sf < MIN_SUFFIX || rest.some((u) => u.length - sf < 1)) sf = 0;
  const cut = new Map(uniq.map((u, i) => [u, `${p > 0 ? ELLIPSIS : ""}${rest[i]!.slice(0, rest[i]!.length - sf)}${sf > 0 ? ELLIPSIS : ""}`]));
  return names.map((n) => cut.get(n)!);
}

/**
 * Labels for paths: the shortest run of trailing segments that is unique among them, so "fg_slabs/run/psi.fits" and "fg_sliding/run/psi.fits"
 * are told apart by the folder that differs, and a path that is already unique is just its last segment. Identical paths cannot be told
 * apart and keep the longest text. The result has no leading "…/": a caller that wants one adds it.
 * With `collapseShared`, a folder that is the same in every label ("run" above) becomes "…", so the folder that differs stands out
 * ("fg_slabs/…/psi.fits"); the last segment is never collapsed.
 */
export function distinguishPaths(paths: string[], opts: { collapseShared?: boolean } = {}): string[] {
  const split = paths.map((p) => p.split("/").filter(Boolean));
  const full = split.map((x) => x.join("/"));
  const depth = split.map(() => 1);
  const label = (i: number) => split[i]!.slice(-depth[i]!).join("/");
  for (;;) {
    const groups = new Map<string, number[]>();
    split.forEach((_, i) => groups.set(label(i), [...(groups.get(label(i)) ?? []), i]));
    let grew = false;
    for (const idx of groups.values()) {
      if (idx.length < 2) continue;
      for (const i of idx) {
        if (depth[i]! < split[i]!.length && idx.some((j) => full[j] !== full[i] || depth[j]! < split[j]!.length)) {
          depth[i] = depth[i]! + 1;
          grew = true;
        }
      }
    }
    if (!grew) break;
  }
  const labels = split.map((_, i) => label(i));
  if (!opts.collapseShared || labels.length < 2 || new Set(labels).size < labels.length) return labels;
  const segs = labels.map((l) => l.split("/"));
  const same = (k: number) => segs.every((x) => x.length > k + 1 && x[x.length - 1 - k] === segs[0]![segs[0]!.length - 1 - k]);
  return segs.map((x) => {
    const out: string[] = [];
    x.forEach((seg, i) => {
      const k = x.length - 1 - i;
      const shared = k > 0 && same(k) && x.length > 1;
      if (!shared) out.push(seg);
      else if (out[out.length - 1] !== ELLIPSIS) out.push(ELLIPSIS);
    });
    return out.join("/");
  });
}
