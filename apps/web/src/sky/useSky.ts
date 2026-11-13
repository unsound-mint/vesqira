import { useEffect, useMemo, useRef, useState } from "react";
import { ApiError } from "../api/http";
import { getSkyFootprint, getSkyPoint } from "../api/sky";
import type { SkyFootprint, SkyFrame } from "../api/sky";
import { isCatalog, isHealpix, type DatasetSummary } from "../api/catalog";
import type { SubsetView } from "../api/subsets";
import { virtualId } from "../api/planes";

/** At most this many footprints are asked for; the panel says how many were left out. Each is one worker read of a header. */
export const MAX_FOOTPRINTS = 120;
const CONCURRENCY = 3;

export type Outline = { polygon: [number, number][]; center: { lon: number; lat: number }; extent: number };
export type SkyItem = {
  id: number;
  name: string;
  /** ready: has an outline; none: has no footprint, and `reason` says why; loading; error: could not be read now. */
  state: "ready" | "none" | "loading" | "error";
  reason: string | null;
  outline: Outline | null;
  /** How the dataset's own frame is known (declared, corrected, inferred, assumed), and what it is, for the list. */
  fromFrame: string | null;
  fromStatus: string | null;
};

/**
 * One row of the panel: a registered dataset (its id) or a virtual subset (the viewer's id for it, the negative of the subset's id,
 * ADR 0063). `none` is set for a row that is listed with a reason and never asked of the worker. `stamp` is what its footprint
 * rests on: its identity, the corrections in force on the file, and for a subset the version of the window.
 */
type Entry = { id: number; name: string; stamp: string; none: string | null };

const stamp = (d: DatasetSummary) => `${d.id}|${d.modifiedAt}|${JSON.stringify(d.corrections ?? null)}`;

const STALE = "the file this subset was defined on has changed, so its window is not drawn until it is re-pinned";

function entriesOf(list: DatasetSummary[], subsets: SubsetView[]): Entry[] {
  const out: Entry[] = list.map((d) => ({
    id: d.id,
    name: d.name,
    stamp: stamp(d),
    none: isCatalog(d) ? "a point catalog has no image grid, so no footprint is drawn" : isHealpix(d) ? "a HEALPix map has no WCS of its own, so no footprint is drawn" : null,
  }));
  for (const sub of subsets) {
    const src = list.find((d) => d.id === sub.sourceId); // an archived, deleted or non-image source leaves the subset out
    if (!src || isCatalog(src) || isHealpix(src)) continue;
    out.push({ id: virtualId(sub.id), name: `${sub.name} (subset)`, stamp: `s${sub.id}|v${sub.version}|${stamp(src)}`, none: sub.stale ? STALE : null });
  }
  return out;
}

type Done = { fp: SkyFootprint } | { error: string };
const cache = new Map<string, Promise<Done>>();

function fetchOne(d: Entry, frame: SkyFrame): Promise<Done> {
  const key = `${d.stamp}|${frame}`;
  let p = cache.get(key);
  if (!p) {
    p = getSkyFootprint(d.id, frame).then(
      (fp): Done => ({ fp }),
      (e: unknown): Done => ({ error: e instanceof ApiError ? e.message : "could not be read" }),
    );
    cache.set(key, p);
    // an error is not remembered: the next ask reads again
    void p.then((r) => "error" in r && cache.delete(key));
    if (cache.size > 600) cache.delete(cache.keys().next().value as string);
  }
  return p;
}

const fromFootprint = (d: Entry, fp: SkyFootprint): SkyItem => {
  const f = fp.footprint;
  return {
    id: d.id,
    name: d.name,
    state: f ? "ready" : "none",
    reason: f ? null : (fp.reason ?? "no footprint"),
    outline: f ? { polygon: f.polygon as [number, number][], center: f.center as { lon: number; lat: number }, extent: f.extent_deg } : null,
    fromFrame: fp.from_frame ?? null,
    fromStatus: fp.from_status ?? null,
  };
};

/**
 * The footprint of every registered image, in the panel's frame, read a few at a time. A point catalog or HEALPix map has no image
 * grid and is listed as having none. Nothing here depends on the network background: footprints come from the local API only.
 */
export function useFootprints(all: DatasetSummary[], frame: SkyFrame, enabled = true, subsetList: SubsetView[] = NO_SUBSETS) {
  const list = useMemo(() => all.filter((d) => !d.archived), [all]);
  const shown = useMemo(() => entriesOf(list.slice(0, MAX_FOOTPRINTS), subsetList), [list, subsetList]);
  const key = shown.map((e) => e.stamp).join("\n") + "|" + frame;
  const [done, setDone] = useState<{ key: string; byId: Map<number, SkyItem> }>({ key: "", byId: new Map() });
  const latest = useRef(shown);
  latest.current = shown;
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    const byId = new Map<number, SkyItem>();
    const queue = latest.current.filter((d) => d.none === null);
    for (const d of latest.current) {
      if (d.none !== null) byId.set(d.id, { id: d.id, name: d.name, state: "none", reason: d.none, outline: null, fromFrame: null, fromStatus: null });
    }
    const publish = () => live && setDone({ key, byId: new Map(byId) });
    publish();
    let next = 0;
    const worker = async () => {
      while (live && next < queue.length) {
        const d = queue[next++]!;
        const r = await fetchOne(d, frame);
        if (!live) return;
        byId.set(d.id, "fp" in r ? fromFootprint(d, r.fp) : { id: d.id, name: d.name, state: "error", reason: `could not be read now: ${r.error}`, outline: null, fromFrame: null, fromStatus: null });
        publish();
      }
    };
    void Promise.all(Array.from({ length: Math.min(CONCURRENCY, queue.length) }, worker));
    return () => {
      live = false;
    };
  }, [key, enabled]); // eslint-disable-line react-hooks/exhaustive-deps -- `key` names every input
  const items = useMemo<SkyItem[]>(
    () => shown.map((d) => (done.key === key ? done.byId.get(d.id) : undefined) ?? { id: d.id, name: d.name, state: "loading", reason: null, outline: null, fromFrame: null, fromStatus: null }),
    [shown, done, key],
  );
  return { items, omitted: Math.max(0, list.length - MAX_FOOTPRINTS) };
}

const NO_SUBSETS: SubsetView[] = [];

export type Cursor = { state: "off" | "loading" | "ready" | "none"; lon: number | null; lat: number | null; reason: string | null };
const OFF: Cursor = { state: "off", lon: null, lat: null, reason: null };

/**
 * The viewer's cursor pixel as a world position in the panel's frame, asked of the worker (the pixel is the viewer's; the position is
 * its WCS's). Debounced so that sweeping the mouse over a map asks for the rest position, not every pixel passed.
 */
export function useSkyCursor(datasetId: number | null, probe: { x: number; y: number } | null, frame: SkyFrame): Cursor {
  const [cur, setCur] = useState<{ key: string; c: Cursor }>({ key: "", c: OFF });
  const key = datasetId === null || !probe ? "" : `${datasetId}|${probe.x}|${probe.y}|${frame}`;
  useEffect(() => {
    if (!key || datasetId === null || !probe) return;
    const ac = new AbortController();
    const t = setTimeout(() => {
      getSkyPoint(datasetId, frame, probe.x, probe.y, ac.signal).then(
        (r) => setCur({ key, c: r.position ? { state: "ready", lon: r.position.lon, lat: r.position.lat, reason: null } : { state: "none", lon: null, lat: null, reason: r.reason ?? "no position" } }),
        (e: unknown) => {
          if (!ac.signal.aborted) setCur({ key, c: { state: "none", lon: null, lat: null, reason: e instanceof ApiError ? e.message : "could not be read" } });
        },
      );
    }, 60);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps -- `key` names every input
  if (!key) return OFF;
  return cur.key === key ? cur.c : { state: "loading", lon: null, lat: null, reason: null };
}
