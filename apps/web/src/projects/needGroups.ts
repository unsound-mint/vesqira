import type { Need } from "../api/projects";

/** A set of open "no BUNIT" questions that sit under one top-level folder. */
export type NeedGroup = {
  /** The top-level folder they share. */
  key: string;
  /** The deepest folder all of them are under; the default scope of an answer for the group. */
  ancestor: string;
  needs: Need[];
  folders: number;
  files: number;
  /** Folders an answer can be scoped to, each with the counts it would reach: the ancestor, then each child folder that holds two or more questions. */
  scopes: { folder: string; folders: number; files: number }[];
};

export type NeedItem = { kind: "group"; group: NeedGroup } | { kind: "need"; need: Need };

const parts = (f: string) => (f === "." ? [] : f.split("/"));
const under = (f: string, ancestor: string) => ancestor === "." || f === ancestor || f.startsWith(ancestor + "/");

function commonAncestor(folders: string[]): string {
  const split = folders.map(parts);
  const first = split[0] ?? [];
  let n = first.length;
  for (const p of split) {
    let i = 0;
    while (i < n && i < p.length && p[i] === first[i]) i++;
    n = i;
  }
  return n === 0 ? "." : first.slice(0, n).join("/");
}

function reach(needs: Need[], folder: string) {
  const hit = needs.filter((n) => under(n.scope.folder ?? ".", folder));
  return { folder, folders: hit.length, files: hit.reduce((a, n) => a + n.scope.count, 0) };
}

const groupKey = (n: Need): string | null =>
  n.kind === "missing_bunit" && n.state === "open" && n.scope.folder !== undefined && n.scope.folder !== "." ? (parts(n.scope.folder)[0] ?? null) : null;

/**
 * Groups the open missing_bunit questions by top-level folder. A folder with a single question stays a plain card:
 * a group is only worth a header when one answer replaces several. Order follows the first member of each group, so the
 * list does not jump. Questions that are not about a missing BUNIT, and answered ones, are never grouped.
 */
export function groupNeeds(needs: Need[]): NeedItem[] {
  const byKey = new Map<string, Need[]>();
  for (const n of needs) {
    const key = groupKey(n);
    if (key !== null) byKey.set(key, [...(byKey.get(key) ?? []), n]);
  }
  const out: NeedItem[] = [];
  const emitted = new Set<string>();
  for (const n of needs) {
    const key = groupKey(n);
    const members = key !== null ? byKey.get(key) : undefined;
    if (key === null || !members || members.length < 2) {
      out.push({ kind: "need", need: n });
      continue;
    }
    if (emitted.has(key)) continue;
    emitted.add(key);
    const ancestor = commonAncestor(members.map((m) => m.scope.folder!));
    const depth = parts(ancestor).length + 1;
    const children = new Map<string, Need[]>();
    for (const m of members) {
      const p = parts(m.scope.folder!);
      if (p.length >= depth) {
        const c = p.slice(0, depth).join("/");
        children.set(c, [...(children.get(c) ?? []), m]);
      }
    }
    const total = reach(members, ancestor);
    const scopes = [total, ...[...children.entries()].filter(([f, ms]) => ms.length >= 2 && f !== ancestor).map(([f]) => reach(members, f))];
    out.push({ kind: "group", group: { key, ancestor, needs: members, folders: total.folders, files: total.files, scopes } });
  }
  return out;
}
