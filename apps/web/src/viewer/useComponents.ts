import { useEffect, useMemo, useState } from "react";
import { getSpectrum } from "../api/planes";
import type { Spectrum } from "../api/planes";
import { getComponents, indexRohsa, listRohsaRuns } from "../api/rohsa";
import type { Components, RohsaIndexState, RohsaRun } from "../api/rohsa";
import type { Pixel, Viewer } from "./useViewer";

const isAbort = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const message = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/**
 * The Gaussian components of a ROHSA run at one pixel of the open dataset (ADR 0046). The pixel is the pinned one, else the
 * one under the pointer, and it stays at the last one chosen when the pointer leaves. The data drawn with the components is
 * that same pixel's own spectrum (a one-pixel region), never the region mean the plot above shows.
 */
export function useComponents(v: Viewer) {
  const { datasetId, view, probe } = v;
  const [runs, setRuns] = useState<RohsaRun[]>([]);
  const [enabled, setEnabled] = useState(false);
  const [chosen, setChosen] = useState<number | null>(null);
  const [pixel, setPixel] = useState<Pixel | null>(null);
  const [index, setIndex] = useState<RohsaIndexState | null>(null);
  const [indexError, setIndexError] = useState<string | null>(null);
  const [comps, setComps] = useState<Components | null>(null);
  const [data, setData] = useState<Spectrum | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    setRuns([]);
    setChosen(null);
    setPixel(null);
    setComps(null);
    setData(null);
    if (datasetId === null || !view) return;
    const ac = new AbortController();
    listRohsaRuns(datasetId, ac.signal)
      .then(setRuns)
      .catch((e: unknown) => !isAbort(e) && setRuns([]));
    return () => ac.abort();
  }, [datasetId, view]);

  const run = useMemo(() => runs.find((r) => r.runId === chosen) ?? runs.find((r) => r.usable) ?? runs[0] ?? null, [runs, chosen]);
  const runId = run?.usable ? run.runId : null;

  useEffect(() => {
    if (probe) setPixel((p) => (p && p.x === probe.x && p.y === probe.y ? p : { x: probe.x, y: probe.y }));
  }, [probe]);

  // Index the .dat once (each call is a bounded slice of work, so a very large file shows progress instead of hanging).
  useEffect(() => {
    setIndex(null);
    setIndexError(null);
    if (!enabled || datasetId === null || runId === null) return;
    const ac = new AbortController();
    (async () => {
      for (;;) {
        const st = await indexRohsa(datasetId, runId, ac.signal);
        setIndex(st);
        if (st.state !== "building") return;
      }
    })().catch((e: unknown) => !isAbort(e) && setIndexError(message(e, "Could not index the .dat.")));
    return () => ac.abort();
  }, [enabled, datasetId, runId]);

  const ready = index?.state === "ready";
  useEffect(() => {
    if (!enabled || !ready || datasetId === null || runId === null || !pixel) {
      setComps(null);
      setData(null);
      setError(null);
      setLoading(false);
      return;
    }
    const ac = new AbortController();
    setLoading(true);
    const t = setTimeout(() => {
      Promise.all([
        getComponents(datasetId, runId, pixel.x, pixel.y, ac.signal),
        getSpectrum(datasetId, { shape: "rect", x0: pixel.x, y0: pixel.y, x1: pixel.x, y1: pixel.y }, ac.signal),
      ])
        .then(([c, sp]) => {
          setComps(c);
          setData(sp);
          setError(null);
          setLoading(false);
        })
        .catch((e: unknown) => {
          if (isAbort(e)) return;
          setComps(null);
          setData(null);
          setError(message(e, "Could not read the components."));
          setLoading(false);
        });
    }, 120);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [enabled, ready, datasetId, runId, pixel]);

  return { runs, run, chosen: run?.runId ?? null, setChosen, usable: runId !== null, enabled, setEnabled, index, indexError, pixel, comps, data, error, loading };
}

export type ComponentsState = ReturnType<typeof useComponents>;
