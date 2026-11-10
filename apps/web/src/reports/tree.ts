import type { ReportSummary } from "../api/reports";

export type TreeRow = { r: ReportSummary; depth: number; hasChildren: boolean };

/** Siblings read newest first by the date they are about; undated pages follow, by title (numbers in order). */
export function siblingOrder(a: ReportSummary, b: ReportSummary): number {
  if (a.date && b.date) return b.date.localeCompare(a.date) || a.title.localeCompare(b.title, undefined, { numeric: true });
  if (a.date || b.date) return a.date ? -1 : 1;
  return a.title.localeCompare(b.title, undefined, { numeric: true });
}

export function childrenOf(list: ReportSummary[]): Map<number, ReportSummary[]> {
  const ids = new Set(list.map((r) => r.id));
  const m = new Map<number, ReportSummary[]>();
  for (const r of list) {
    const p = ids.has(r.parent) && r.parent !== r.id ? r.parent : 0; // a parent that is gone puts the page at the top
    m.set(p, [...(m.get(p) ?? []), r]);
  }
  for (const v of m.values()) v.sort(siblingOrder);
  return m;
}

/** The rows a tree shows: every top-level page, and the children of the pages that are open. */
export function visibleRows(list: ReportSummary[], open: Set<number>): TreeRow[] {
  const kids = childrenOf(list);
  const out: TreeRow[] = [];
  const seen = new Set<number>();
  const walk = (parent: number, depth: number) => {
    for (const r of kids.get(parent) ?? []) {
      if (seen.has(r.id)) continue; // never loop on bad data
      seen.add(r.id);
      const hasChildren = (kids.get(r.id)?.length ?? 0) > 0;
      out.push({ r, depth, hasChildren });
      if (hasChildren && open.has(r.id)) walk(r.id, depth + 1);
    }
  };
  walk(0, 0);
  const reachable = new Set<number>();
  const stack = [0];
  while (stack.length) for (const c of kids.get(stack.pop() as number) ?? []) if (!reachable.has(c.id)) { reachable.add(c.id); stack.push(c.id); }
  for (const r of list) if (!reachable.has(r.id)) out.push({ r, depth: 0, hasChildren: false }); // caught in a loop: the server refuses one, but never hide a page
  return out;
}

/** The ids above a page, nearest first. */
export function ancestors(list: ReportSummary[], id: number): number[] {
  const byId = new Map(list.map((r) => [r.id, r]));
  const out: number[] = [];
  for (let p = byId.get(id)?.parent ?? 0; p && p !== id && !out.includes(p) && byId.has(p); p = byId.get(p)?.parent ?? 0) out.push(p);
  return out;
}

/** A page and everything under it: where it cannot be moved. */
export function subtree(list: ReportSummary[], id: number): Set<number> {
  const kids = childrenOf(list);
  const out = new Set<number>([id]);
  const stack = [id];
  while (stack.length) for (const c of kids.get(stack.pop() as number) ?? []) if (!out.has(c.id)) { out.add(c.id); stack.push(c.id); }
  return out;
}

/** The page to open first: the newest dated report, else the one changed last. */
export function firstPage(list: ReportSummary[]): number | null {
  const dated = list.filter((r) => r.date).sort(siblingOrder);
  return dated[0]?.id ?? list[0]?.id ?? null;
}
