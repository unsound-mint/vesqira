import { useCallback, useEffect, useRef, useState } from "react";
import { applyDs9, createSavedRegion, deleteSavedRegion, listSavedRegions, toShape, updateSavedRegion } from "../api/regions";
import type { SavedRegion } from "../api/regions";
import type { RegionShape } from "../api/planes";

/** The saved, named regions of one dataset: list, select, save, rename, replace the shape, duplicate, delete and undo. */
export function useSavedRegions(datasetId: number | null, setRegion: (r: RegionShape | null) => void) {
  const [saved, setSaved] = useState<SavedRegion[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [hidden, setHidden] = useState<Set<number>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [undo, setUndo] = useState<SavedRegion | null>(null);
  const live = useRef(datasetId);
  live.current = datasetId;

  useEffect(() => {
    setSaved([]);
    setSelectedId(null);
    setHidden(new Set());
    setError(null);
    setUndo(null);
    if (datasetId === null) return;
    let ok = true;
    listSavedRegions(datasetId).then((r) => ok && setSaved(Array.isArray(r) ? r : [])).catch(() => undefined); // the list is a convenience; the viewer works without it
    return () => {
      ok = false;
    };
  }, [datasetId]);

  const run = useCallback(async (f: () => Promise<void>) => {
    try {
      await f();
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "That did not work.");
    }
  }, []);

  const select = useCallback((r: SavedRegion | null) => {
    setSelectedId(r ? r.id : null);
    setRegion(r ? toShape(r) : null);
  }, [setRegion]);

  const save = (name: string, shape: RegionShape) => run(async () => {
    const r = await createSavedRegion(datasetId!, name, shape);
    setSaved((l) => [...l, r]);
    setSelectedId(r.id);
  });
  const rename = (id: number, name: string) => run(async () => {
    const r = await updateSavedRegion(id, { name });
    setSaved((l) => l.map((x) => (x.id === id ? r : x)));
  });
  const reshape = (id: number, shape: RegionShape) => run(async () => {
    const r = await updateSavedRegion(id, { shape });
    setSaved((l) => l.map((x) => (x.id === id ? r : x)));
  });
  const duplicate = (r: SavedRegion) => run(async () => {
    const c = await createSavedRegion(datasetId!, `${r.name} copy`.slice(0, 80), toShape(r));
    setSaved((l) => [...l, c]);
    select(c);
  });
  const remove = (r: SavedRegion) => run(async () => {
    await deleteSavedRegion(r.id);
    setSaved((l) => l.filter((x) => x.id !== r.id));
    if (selectedId === r.id) setSelectedId(null);
    setUndo(r);
  });
  const undoDelete = () => run(async () => {
    if (!undo) return;
    const r = await createSavedRegion(datasetId!, undo.name, toShape(undo));
    setSaved((l) => [...l, r]);
    setUndo(null);
  });
  const importText = (text: string, includeApproximated: boolean) => run(async () => {
    const r = await applyDs9(datasetId!, text, includeApproximated);
    setSaved((l) => [...l, ...r.created]);
    if (r.failed.length > 0) throw new Error(`Not saved: ${r.failed.join("; ")}`);
  });
  const toggle = (id: number) => setHidden((h) => {
    const n = new Set(h);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    return n;
  });

  return { saved, selectedId, hidden, error, undo, select, save, rename, reshape, duplicate, remove, undoDelete, importText, toggle, dismissUndo: () => setUndo(null) };
}
