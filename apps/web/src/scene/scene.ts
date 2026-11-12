import type { components } from "../api/schema";

// The scene/1 contract (ADR 0042, section 3), generated from packages/contracts/bodies.json. The server reads the same
// shape strictly (services/api/internal/scene); never widen these types here.
type S = components["schemas"];
export type Scene = S["Scene"];
export type SceneBase = S["SceneBase"];
export type SceneSpectral = S["SceneSpectral"];
export type SceneDisplay = S["SceneDisplay"];
export type SceneView = S["SceneView"];
export type SceneLink = S["SceneLink"];
export type SceneMask = S["SceneMask"];
export type ScenePoint = S["ScenePoint"];
export type SceneRegion = S["SceneRegion"];
export type SceneOverlay = S["SceneOverlay"];
export type OverlayKind = SceneOverlay["kind"];
export type OverlayOf<K extends OverlayKind> = Extract<SceneOverlay, { kind: K }>;
export type OrientationOverlay = S["OrientationOverlay"];
export type RegionsOverlay = S["RegionsOverlay"];

export const SCENE_VERSION = "scene/1" as const;
export const MAX_ZOOM = 16;
export const MAX_OVERLAYS = 16;
/** The largest canvas a figure records (`render.canvas`): what a redrawing allocates for the base raster (256 MiB of RGBA). The server holds the same bound. */
export const MAX_RENDER_PIXELS = 2 ** 26;
export const MAX_RENDER_SIDE = 65536;
/** The overlays every pane has today, by id: its own orientation lines (axial maps only) and its regions. */
export const ORIENTATION_ID = "orientation";
export const REGIONS_ID = "regions";

export const DEFAULT_DISPLAY: SceneDisplay = { mapping: "scalar", colormap: "viridis", stretch: "linear", range: "percentile" };
/** Lines about 16 screen pixels apart, 0.8 of the step long, opacity from the declared reliability map (as since ADR 0016). */
export const orientationOverlay = (datasetId: number, visible: boolean): OrientationOverlay => ({
  id: ORIENTATION_ID, kind: "orientation", datasetId, visible, sampling: { mode: "auto", gapPx: 16 }, length: "fixed", opacity: "reliability",
});
export const regionsOverlay = (datasetId: number): RegionsOverlay => ({ id: REGIONS_ID, kind: "regions", datasetId, visible: true, saved: [] });

export function newScene(datasetId: number, display: SceneDisplay, axial: boolean, linesVisible: boolean): Scene {
  return {
    schema: SCENE_VERSION,
    base: { datasetId, channel: 0, display: { ...display, mapping: axial ? "axial" : "scalar" } },
    overlays: axial ? [orientationOverlay(datasetId, linesVisible), regionsOverlay(datasetId)] : [regionsOverlay(datasetId)],
    view: { zoom: 1 },
  };
}

export const overlayById = (s: Scene | null, id: string): SceneOverlay | undefined => s?.overlays.find((o) => o.id === id);
export const regionsOf = (s: Scene | null): RegionsOverlay | undefined => s?.overlays.find((o): o is RegionsOverlay => o.kind === "regions" && o.id === REGIONS_ID);
export const orientationOf = (s: Scene | null): OrientationOverlay | undefined => s?.overlays.find((o): o is OrientationOverlay => o.kind === "orientation" && o.id === ORIENTATION_ID);

// ---- actions ----

/**
 * Every change to a pane's scene is one of these plain, serialisable actions, so a command registry with undo (roadmap
 * Stage 6) can record and replay them. In a patch, null removes an optional field (JSON cannot carry undefined).
 * setSpectral and syncAxial record facts the server reported, not a person's command; an undo stack skips them.
 */
type Nullable<T> = { [K in keyof T]?: T[K] | null };
type OverlayPatch = Nullable<Omit<SceneOverlay, "id" | "kind">> & Record<string, unknown>;
export type SceneAction =
  | { type: "setChannel"; channel: number }
  | { type: "setSpectral"; channel: number; spectral: SceneSpectral | null }
  | { type: "setDisplay"; display: Nullable<Omit<SceneDisplay, "mapping">> }
  | { type: "addOverlay"; overlay: SceneOverlay; index?: number }
  | { type: "updateOverlay"; id: string; patch: OverlayPatch }
  | { type: "removeOverlay"; id: string }
  | { type: "moveOverlay"; id: string; to: number }
  | { type: "setView"; view: { zoom?: number; centre?: ScenePoint | null } }
  | { type: "setLink"; link: SceneLink | null };

/** Pane-level actions: they start a scene over or change what the dataset is, so they need more than the scene. */
export type PaneAction =
  | SceneAction
  | { type: "resetForDataset"; datasetId: number | null; axial: boolean }
  | { type: "syncAxial"; axial: boolean };

const same = (a: unknown, b: unknown) => a === b || JSON.stringify(a) === JSON.stringify(b);

/** Copy of o with patch applied; null deletes a key. Returns o itself when nothing changes. */
function patched<T extends object>(o: T, patch: Record<string, unknown>, locked: readonly string[] = []): T {
  let out: Record<string, unknown> | null = null;
  for (const [k, v] of Object.entries(patch)) {
    if (locked.includes(k) || v === undefined) continue;
    const cur = (o as Record<string, unknown>)[k];
    if (v === null ? !(k in o) : same(cur, v)) continue;
    const next: Record<string, unknown> = out ?? { ...(o as Record<string, unknown>) };
    if (v === null) delete next[k];
    else next[k] = v;
    out = next;
  }
  return (out as T | null) ?? o;
}

const clampZoom = (z: number) => (Number.isFinite(z) ? Math.min(MAX_ZOOM, Math.max(1, z)) : 1);

/** The pure scene reducer. It never mutates its input and returns the same object for an action that changes nothing. */
export function sceneReducer(s: Scene, a: SceneAction): Scene {
  switch (a.type) {
    case "setChannel": {
      const channel = Math.max(0, Math.trunc(a.channel));
      if (!Number.isFinite(channel) || channel === s.base.channel) return s;
      const { spectral: _old, ...base } = s.base; // the world value belonged to the old channel; the worker reports the new one
      return { ...s, base: { ...base, channel } };
    }
    case "setSpectral": {
      if (a.channel !== s.base.channel || same(s.base.spectral ?? null, a.spectral)) return s;
      const base = patched(s.base, { spectral: a.spectral });
      return base === s.base ? s : { ...s, base };
    }
    case "setDisplay": {
      const display = patched(s.base.display, a.display, ["mapping"]);
      return display === s.base.display ? s : { ...s, base: { ...s.base, display } };
    }
    case "addOverlay": {
      if (s.overlays.length >= MAX_OVERLAYS || s.overlays.some((o) => o.id === a.overlay.id)) return s;
      const overlays = [...s.overlays];
      overlays.splice(a.index ?? overlays.length, 0, a.overlay);
      return { ...s, overlays };
    }
    case "updateOverlay": {
      const i = s.overlays.findIndex((o) => o.id === a.id);
      if (i < 0) return s;
      const next = patched(s.overlays[i]!, a.patch, ["id", "kind"]);
      if (next === s.overlays[i]) return s;
      const overlays = [...s.overlays];
      overlays[i] = next;
      return { ...s, overlays };
    }
    case "removeOverlay": {
      const overlays = s.overlays.filter((o) => o.id !== a.id);
      return overlays.length === s.overlays.length ? s : { ...s, overlays };
    }
    case "moveOverlay": {
      const from = s.overlays.findIndex((o) => o.id === a.id);
      const to = Math.min(s.overlays.length - 1, Math.max(0, Math.trunc(a.to)));
      if (from < 0 || from === to || !Number.isFinite(to)) return s;
      const overlays = [...s.overlays];
      const [o] = overlays.splice(from, 1);
      overlays.splice(to, 0, o!);
      return { ...s, overlays };
    }
    case "setLink": {
      if (same(s.link ?? null, a.link)) return s;
      const { link: _old, ...rest } = s; // null leaves the group: the scene then says nothing about links
      return a.link ? { ...rest, link: a.link } : rest;
    }
    case "setView": {
      const zoom = a.view.zoom === undefined ? s.view.zoom : clampZoom(a.view.zoom);
      // the whole picture fitted has no centre to keep: fitting again always shows all of it, centred
      const view = patched(s.view, { zoom, centre: zoom === 1 ? null : a.view.centre });
      return view === s.view ? s : { ...s, view };
    }
  }
}

/** What a pane holds: its scene (none without a dataset), and what carries over to the next dataset opened in it. */
export type PaneState = { scene: Scene | null; keep: { display: SceneDisplay; linesVisible: boolean } };
export const initialPane = (datasetId: number | null, axial: boolean): PaneState => {
  const keep = { display: DEFAULT_DISPLAY, linesVisible: true };
  return { scene: datasetId === null ? null : newScene(datasetId, keep.display, axial, keep.linesVisible), keep };
};

/**
 * Opening a dataset (or reading it again) starts a new scene on channel 0, fitted, with no region. The colour settings and
 * whether orientation lines are shown carry over from the previous scene, as they always have.
 */
export function paneReducer(p: PaneState, a: PaneAction): PaneState {
  if (a.type === "resetForDataset") {
    const keep = p.scene ? { display: p.scene.base.display, linesVisible: orientationOf(p.scene)?.visible ?? p.keep.linesVisible } : p.keep;
    const { scaleGroup: _g, ...display } = keep.display; // a shared scale belongs to the pair of maps it was set for
    return { scene: a.datasetId === null ? null : newScene(a.datasetId, display, a.axial, keep.linesVisible), keep: { ...keep, display } };
  }
  if (!p.scene) return p;
  if (a.type === "syncAxial") {
    const s = p.scene;
    const mapping = a.axial ? "axial" : "scalar";
    const lines = orientationOf(s);
    if (s.base.display.mapping === mapping && !!lines === a.axial) return p;
    let overlays = s.overlays.filter((o) => o.id !== ORIENTATION_ID);
    if (a.axial) overlays = [orientationOverlay(s.base.datasetId, lines?.visible ?? p.keep.linesVisible), ...overlays];
    const keep = lines ? { ...p.keep, linesVisible: lines.visible } : p.keep;
    return { keep, scene: { ...s, base: { ...s.base, display: { ...s.base.display, mapping } }, overlays } };
  }
  const scene = sceneReducer(p.scene, a);
  return scene === p.scene ? p : { ...p, scene };
}
