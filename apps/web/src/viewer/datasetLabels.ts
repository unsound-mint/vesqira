import type { Dataset } from "../api/catalog";
import { distinguishPaths } from "../ui/distinguish";

/**
 * Option labels for a list of datasets in a picker. A name that two datasets share gets the folder that tells them apart (the shortest
 * unique tail of their paths, a folder they share shown as "…") and the host's name when it is not this computer; a unique name stays bare,
 * unless `hostAlways` asks for the host of every file that is not on this computer.
 */
export function datasetLabels(ds: Dataset[], hostName: (id: number) => string, opts: { hostAlways?: boolean } = {}): Map<number, string> {
  const byName = new Map<string, Dataset[]>();
  for (const d of ds) byName.set(d.name, [...(byName.get(d.name) ?? []), d]);
  const out = new Map<number, string>();
  for (const same of byName.values()) {
    const first = same[0]!;
    if (same.length < 2) {
      out.set(first.id, opts.hostAlways && first.hostId !== 0 ? `${first.name} (${hostName(first.hostId)})` : first.name);
      continue;
    }
    // files on different hosts are told apart by the host; the folder is only needed among the files of one host
    const dirs = new Map<number, string>();
    for (const host of new Set(same.map((d) => d.hostId))) {
      const there = same.filter((d) => d.hostId === host);
      const labels = distinguishPaths(there.map((d) => d.path), { collapseShared: true });
      there.forEach((d, i) => dirs.set(d.id, there.length > 1 ? labels[i]!.split("/").slice(0, -1).join("/") : ""));
    }
    for (const d of same) out.set(d.id, `${d.name} (${[d.hostId !== 0 ? hostName(d.hostId) : "", dirs.get(d.id)].filter(Boolean).join(", ")})`.replace(/ \(\)$/, ""));
  }
  return out;
}
