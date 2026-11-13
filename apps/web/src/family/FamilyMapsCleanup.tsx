import { useCallback, useEffect, useMemo, useState } from "react";
import { Button, Checkbox } from "../ui";
import { listFamilyMaps, removeFamilyMaps } from "../api/family";
import type { FamilyMapRemoval, FamilyMapView } from "../api/family";
import { ApiError } from "../api/http";
import s from "./Family.module.css";

const kb = (n: number) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MiB` : `${Math.max(1, Math.round(n / 1024))} KiB`);

/** The comparison and spread maps earlier family runs registered (ADR 0070): list them, remove the ones nothing uses. Sources are never in this list. */
export function FamilyMapsCleanup({ refreshKey, onChanged }: { refreshKey: unknown; onChanged: (removed: number[]) => void }) {
  const [maps, setMaps] = useState<FamilyMapView[]>([]);
  const [chosen, setChosen] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [results, setResults] = useState<FamilyMapRemoval[]>([]);

  const load = useCallback(() => listFamilyMaps().then((m) => { setMaps(m); setChosen((c) => c.filter((id) => m.some((x) => x.datasetId === id && x.holds.length === 0))); }).catch(() => setMaps([])), []);
  useEffect(() => { void load(); }, [load, refreshKey]);

  const runs = useMemo(() => {
    const by = new Map<string, FamilyMapView[]>();
    for (const m of maps) by.set(m.run, [...(by.get(m.run) ?? []), m]);
    return [...by.values()];
  }, [maps]);
  const free = (ms: FamilyMapView[]) => ms.filter((m) => m.holds.length === 0).map((m) => m.datasetId);

  async function remove() {
    if (chosen.length === 0) return;
    if (!window.confirm(`Remove ${chosen.length} comparison ${chosen.length === 1 ? "map" : "maps"}? The maps you compared are not touched. Files Vesqira wrote on this computer are deleted; files on a host are left there.`)) return;
    setBusy(true);
    setErr(null);
    try {
      const res = (await removeFamilyMaps(chosen)).results;
      setResults(res);
      onChanged(res.filter((r) => r.removed).map((r) => r.datasetId));
    } catch (e) {
      const body = e instanceof ApiError ? (e.body as { results?: FamilyMapRemoval[] } | null) : null;
      setResults(body?.results ?? []);
      setErr(e instanceof Error ? e.message : "Could not remove them.");
    }
    setChosen([]);
    await load();
    setBusy(false);
  }

  if (maps.length === 0 && results.length === 0) return null;
  return (
    <section aria-label="Earlier comparison maps" className={s.block}>
      <h3 className={s.h3}>Saved comparison maps</h3>
      <p className={s.note}>Each family run saves its difference maps and its spread map as datasets. A map that a figure, board, report or published review uses, or that something was made from, is kept and says why. There is no automatic clean-up.</p>
      {runs.map((ms) => (
        <fieldset key={ms[0]!.run} className={s.runBox}>
          <legend className={s.note}>{new Date(ms[0]!.createdAt).toLocaleString()}{ms[0]!.hostId ? " · on a host" : ""}</legend>
          <ul className={s.pick}>
            {ms.map((m) => (
              <li key={m.datasetId}>
                <Checkbox label={`${m.name} (${m.role === "spread" ? "spread" : "difference"}, ${kb(m.sizeBytes)})`} checked={chosen.includes(m.datasetId)} disabled={m.holds.length > 0}
                  onChange={() => setChosen((c) => (c.includes(m.datasetId) ? c.filter((x) => x !== m.datasetId) : [...c, m.datasetId]))} />
                {m.holds.length > 0 ? <span className={s.note}> Kept: {m.holds.join(" ")}</span> : null}
              </li>
            ))}
          </ul>
          {free(ms).length > 0 ? <Button onClick={() => setChosen((c) => [...new Set([...c, ...free(ms)])])}>Choose the maps of this run that can go</Button> : null}
        </fieldset>
      ))}
      <div className={s.row}>
        <Button variant="primary" disabled={busy || chosen.length === 0} onClick={remove}>{busy ? "Removing…" : `Remove ${chosen.length} chosen`}</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {results.length > 0 ? (
        <ul className={s.note} aria-label="What was removed" role="status">
          {results.map((r) => <li key={r.datasetId}>{r.name || `file ${r.datasetId}`}: {r.removed ? (r.bytes === "kept" ? "removed from the catalog; the file on the host was left in place" : r.bytes === "failed" ? "removed from the catalog; its file could not be deleted" : "removed") : r.reason}</li>)}
        </ul>
      ) : null}
    </section>
  );
}
