import { useEffect, useMemo, useRef, useState } from "react";
import { ApiError } from "../api/catalog";
import { getOverlayGeometry } from "../api/overlays";
import { geometryKey, needsGeometry, spacingFor } from "../scene/geometry";
import type { GeometryEntry, GeometryMap } from "../scene/geometry";
import type { Scene, SceneOverlay } from "../scene/scene";

type Want = { o: SceneOverlay; spacing: number | null; key: string };

/**
 * The geometry of every visible overlay that needs the worker (ADR 0042, section 3), by overlay id. An entry is replaced
 * as soon as what it depends on changes (its settings, the base, the spacing at this zoom), so the map never holds geometry
 * for settings the scene no longer has: renderers draw only "ready" entries, and the overlay list shows the rest as
 * computing or refused with the server's reason. Colour and visibility changes refetch nothing; the server caches repeats.
 */
export function useOverlayGeometry(scene: Scene | null, scale: number): GeometryMap {
  const [map, setMap] = useState<GeometryMap>({});
  const current = useRef<GeometryMap>({});
  const wanted: Want[] = useMemo(() => {
    if (!scene) return [];
    return scene.overlays
      .filter((o) => o.visible && needsGeometry(o, scene))
      .map((o) => {
        const spacing = spacingFor(o, scale);
        return { o, spacing, key: geometryKey(scene.base.datasetId, o, spacing) };
      });
  }, [scene, scale]);
  const signature = wanted.map((w) => `${w.o.id}=${w.key}`).join("\n");
  const baseId = scene?.base.datasetId ?? null;

  useEffect(() => {
    const prev = current.current;
    const next: Record<string, GeometryEntry> = {};
    for (const w of wanted) next[w.o.id] = prev[w.o.id]?.key === w.key ? prev[w.o.id]! : { key: w.key, status: "loading" };
    current.current = next;
    setMap(next);
    if (baseId === null) return;
    const ac = new AbortController();
    // every entry still being computed is asked for (again, after an abort): the server answers a repeat from its cache
    const pending = wanted.filter((w) => next[w.o.id]!.status === "loading");
    const t = setTimeout(() => {
      for (const w of pending) {
        getOverlayGeometry(baseId, w.o, w.spacing, ac.signal)
          .then((g) => settle(w, { key: w.key, status: "ready", g }))
          .catch((e: unknown) => {
            if (e instanceof DOMException && e.name === "AbortError") return;
            settle(w, { key: w.key, status: "refused", reason: e instanceof ApiError ? e.message : "The overlay could not be computed." });
          });
      }
    }, 120); // a zoom gesture or typing settles before the worker is asked
    function settle(w: Want, e: GeometryEntry) {
      if (current.current[w.o.id]?.key !== w.key) return; // the overlay changed meanwhile
      current.current = { ...current.current, [w.o.id]: e };
      setMap(current.current);
    }
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [signature, baseId]); // eslint-disable-line react-hooks/exhaustive-deps

  return map;
}
