import { useEffect, useRef, useState } from "react";
import { loadAladin } from "./aladinLoader";
import type { AladinInstance, AladinNs, AladinOverlay } from "./aladinLoader";
import { contains, wrap360 } from "./projection";
import type { View } from "./projection";
import type { SkyFrame } from "../api/sky";
import type { SkyItem } from "./useSky";
import type { Survey } from "./surveys";
import s from "./SkyPanel.module.css";

export type BackgroundStatus = { state: "connecting" } | { state: "ready" } | { state: "unavailable"; reason: string };

/** What the panel can ask of the picture it does not draw itself. */
export type BackgroundApi = {
  capture(): Promise<string | null>;
  /** The view Aladin is showing now, in the panel's frame and as wide as the picture is; null if the library will not say. */
  view(): View | null;
};

const REACH_TIMEOUT_MS = 8000;

/** A token colour as the browser resolves it now, for the places a library wants a literal colour. */
const token = (name: string, fallback: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;

/**
 * The survey image behind the footprints, drawn by Aladin Lite (ADR 0072). Everything handed to Aladin is ICRS (its shapes and
 * gotoRaDec are ICRS whatever frame it displays; `cooFrame` only chooses the grid and the orientation), so the outlines and the
 * cursor come from the worker in ICRS and the panel's frame is Aladin's `cooFrame`. The picture is context: nothing here reads it
 * back as numbers, and a click is turned into a position on the sky, never a pixel of the survey.
 *
 * Order matters for privacy and for being offline: the survey's own `properties` file is asked for first, with a timeout. If it cannot
 * be reached the library is not even loaded and the panel falls back to the plain grid. Aladin Lite's own usage log (`log`) is off.
 */
export function AladinView({ survey, frame, fit, items, selectedId, cursor, onPick, onStatus, onApi }: {
  survey: Survey;
  frame: SkyFrame;
  /** The view to go to, in ICRS, and a key that changes when it should be applied again (another selection, "fit"). */
  fit: { view: View | null; key: string };
  items: SkyItem[];
  selectedId: number | null;
  cursor: { lon: number; lat: number } | null;
  onPick: (id: number) => void;
  onStatus: (s: BackgroundStatus) => void;
  onApi: (a: BackgroundApi | null) => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const live = useRef<{ A: AladinNs; al: AladinInstance; foot: AladinOverlay; cur: AladinOverlay } | null>(null);
  const [built, setBuilt] = useState(0);
  const latest = useRef({ items, onPick, onStatus, fit });
  latest.current = { items, onPick, onStatus, fit };

  // one instance per survey and frame: the frame is a construction option of the library
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let dead = false;
    const ac = new AbortController();
    const timer = setTimeout(() => ac.abort(), REACH_TIMEOUT_MS);
    const say = (st: BackgroundStatus) => !dead && latest.current.onStatus(st);
    say({ state: "connecting" });
    void (async () => {
      try {
        const r = await fetch(`${survey.url}/properties`, { signal: ac.signal, cache: "no-store" });
        clearTimeout(timer);
        if (!r.ok) throw new Error(`the server answered ${r.status}`);
      } catch (e) {
        clearTimeout(timer);
        say({ state: "unavailable", reason: ac.signal.aborted ? `${survey.host} did not answer in ${REACH_TIMEOUT_MS / 1000} seconds` : `${survey.host} could not be reached${e instanceof Error && e.message.startsWith("the server") ? ` (${e.message})` : ""}` });
        return;
      }
      if (dead) return;
      let A: AladinNs;
      try {
        A = await loadAladin();
      } catch (e) {
        say({ state: "unavailable", reason: typeof e === "string" && /webgl/i.test(e) ? "this browser has no WebGL2, which the survey viewer needs" : "the survey viewer could not be loaded" });
        return;
      }
      if (dead) return;
      const f = latest.current.fit.view;
      const al = A.aladin(el, {
        survey: survey.url,
        cooFrame: frame,
        projection: "TAN",
        fov: f?.fov ?? 4,
        log: false, // Aladin Lite's own usage log sends the survey, target, field of view and page address to CDS
        showCooGrid: true,
        showReticle: false,
        showContextMenu: false,
        showFullscreenControl: false,
        showLayersControl: false,
        showGotoControl: false,
        showSimbadPointerControl: false,
        showProjectionControl: false,
        showShareControl: false,
        showSettingsControl: false,
        showCooGridControl: false,
        showStatusBar: false,
        showCooLocation: false,
        showFov: false,
        showFrame: false,
        showCatalog: false,
        showZoomControl: true,
        gridColor: token("--text-3", "#8794a3"),
        backgroundColor: "rgb(20, 24, 30)", // a string: 3.8.2 parses it with a colour class that throws on an array, which left the panel on "Contacting"
      });
      if (f) al.gotoRaDec(f.lon, f.lat);
      const foot = A.graphicOverlay({ name: "footprints", color: token("--text-2", "#aab4c0"), lineWidth: 1 });
      const cur = A.graphicOverlay({ name: "cursor", color: token("--accent", "#27c3e6"), lineWidth: 2 });
      al.addOverlay(foot);
      al.addOverlay(cur);
      al.on("click", (e) => {
        if (e.isDragging || e.ra === undefined || e.dec === undefined) return;
        const at: [number, number] = [e.ra, e.dec];
        // the footprint under the click, from the outlines the worker computed: the later in the list is drawn on top
        const hit = [...latest.current.items].reverse().find((it) => it.outline && contains(it.outline.polygon, at));
        if (hit) latest.current.onPick(hit.id);
      });
      live.current = { A, al, foot, cur };
      setBuilt((n) => n + 1); // the footprints and the cursor are drawn now that there is a picture to draw on
      onApi({
        capture: async () => {
          const r = await al.getViewDataURL?.({ format: "image/png", logo: false });
          return typeof r === "string" ? r : null;
        },
        // Aladin Lite 3.8.2 (pinned): getRaDec() converts the view's centre to ICRS (`viewToICRSCooSys`) whatever the grid frame, so it
        // cannot give the panel's frame for a Galactic view. The centre it converts is `wasm.getCenter()`, which is in the view's own
        // frame (the panel's: `cooFrame` above), and getFoV()[0] is the width of the view in degrees. A different library version
        // must be checked again, which is why this says null rather than guess when the shape is not as found.
        view: () => {
          const w = (al as unknown as { wasm?: { getCenter?: () => ArrayLike<number> | null } }).wasm;
          const c = typeof w?.getCenter === "function" ? w.getCenter() : null; // a Float64Array in 3.8.2
          const [fov] = al.getFoV();
          if (!c || c.length < 2 || !Number.isFinite(c[0]) || !Number.isFinite(c[1]) || !(fov > 0)) return null;
          return { lon: wrap360(c[0]!), lat: c[1]!, fov };
        },
      });
      say({ state: "ready" });
    })().catch(() => say({ state: "unavailable", reason: "the survey viewer failed to start" })); // never leave the panel on "Contacting"
    return () => {
      dead = true;
      clearTimeout(timer);
      ac.abort();
      live.current = null;
      onApi(null);
      el.replaceChildren(); // Aladin Lite has no destroy: its canvas goes with its element
    };
  }, [survey.url, survey.host, frame]); // eslint-disable-line react-hooks/exhaustive-deps -- callbacks are read through `latest`

  // go where the panel says when the selection or "fit" changes
  useEffect(() => {
    const f = fit.view;
    if (f && live.current) {
      live.current.al.gotoRaDec(f.lon, f.lat);
      live.current.al.setFoV(f.fov);
    }
  }, [fit.key]); // eslint-disable-line react-hooks/exhaustive-deps -- the key names the view

  // the footprints, the selected one heavier (and the list beside the picture names it, so colour is not the only cue)
  useEffect(() => {
    const l = live.current;
    if (!l) return;
    l.foot.removeAll();
    const accent = token("--accent", "#27c3e6");
    for (const it of items) {
      if (!it.outline) continue;
      const sel = it.id === selectedId;
      l.foot.add(l.A.polygon(it.outline.polygon.map(([lon, lat]) => [wrap360(lon), lat] as [number, number]), sel ? { color: accent, lineWidth: 3 } : { color: token("--text-2", "#aab4c0"), lineWidth: 1 }));
    }
  }, [items, selectedId, fit.key, built]);

  // the viewer's cursor
  useEffect(() => {
    const l = live.current;
    if (!l) return;
    l.cur.removeAll();
    if (cursor) l.cur.add(l.A.circle(cursor.lon, cursor.lat, Math.max(l.al.getFoV()[0] / 60, 1e-4), { color: token("--accent", "#27c3e6"), lineWidth: 2 }));
  }, [cursor, items, fit.key, built]);

  return <div ref={host} className={s.aladin} data-testid="sky-background" />;
}
