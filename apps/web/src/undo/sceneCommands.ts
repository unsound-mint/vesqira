import { REGIONS_ID, paneReducer, regionsOf } from "../scene/scene";
import type { PaneAction, PaneState, Scene, SceneAction } from "../scene/scene";

// The command registry for a viewer pane (roadmap Stage 6, ADR 0057). A command is a named, serialisable scene action; its
// inverse is computed from the state before it ran (the scene as it was), so undo and redo put back exactly what the command
// changed instead of trying to reverse each action by hand (and leave alone what changed since that is not the command's: see revert). The reducer in scene/scene.ts stays the only place a scene changes: undo and redo are
// themselves PaneActions of the wrapper below, applied through it.

/** How many commands a pane remembers. */
export const MAX_UNDO = 100;
/** A command of the same kind within this many ms of the last one joins it: dragging a region or scrubbing channels is one edit. */
export const COALESCE_MS = 800;

/** A command's name and kind. Commands of the same key coalesce. Null: not a command (a fact the server reported, or derived). */
export type Naming = { name: string; key: string } | null;

const DISPLAY_NAME: Record<string, string> = {
  colormap: "change colour map", stretch: "change stretch", range: "change range", limits: "change limits", limitsUnit: "change limits", scaleGroup: "",
};

/** The one table of command names: every SceneAction has a case, so a new action cannot be added without deciding its name. */
export function commandName(a: SceneAction, s: Scene): Naming {
  switch (a.type) {
    case "setChannel":
      return { name: "change channel", key: "channel" };
    case "setSpectral":
      return null; // the worker's report for the channel shown
    case "setDisplay": {
      const fields = Object.keys(a.display);
      const names = [...new Set(fields.map((f) => DISPLAY_NAME[f] ?? "change display").filter((n) => n !== ""))];
      return names.length === 0 ? null : { name: names.length === 1 ? names[0]! : "change display", key: `display:${fields.sort().join(",")}` };
    }
    case "addOverlay":
      return { name: "add overlay", key: `add:${a.overlay.id}` };
    case "updateOverlay": {
      const keys = Object.keys(a.patch);
      if (keys.every((k) => k === "saved")) return null; // the saved regions are server rows the scene mirrors
      if (a.id === REGIONS_ID && "active" in a.patch) return { name: a.patch.active ? "draw region" : "clear region", key: "region" };
      if (keys.length === 1 && keys[0] === "visible") return { name: a.patch.visible ? "show overlay" : "hide overlay", key: `visible:${a.id}` };
      return { name: "edit overlay", key: `overlay:${a.id}` };
    }
    case "removeOverlay":
      return { name: "remove overlay", key: `remove:${a.id}` };
    case "moveOverlay":
      return { name: "reorder overlays", key: "order" };
    case "setView": {
      const zoom = a.view.zoom !== undefined && a.view.zoom !== s.view.zoom;
      return { name: zoom ? "zoom" : "pan", key: "view" };
    }
    case "setLink":
      return null; // follows the link controls of the pair of panes, not a scene edit
  }
}

export type SceneEdit = { name: string; key: string; before: Scene; after: Scene; at: number };
export type UndoHistory = { past: SceneEdit[]; future: SceneEdit[] };
export type ViewerState = { pane: PaneState; undo: UndoHistory };

/** An action with the time it was made (the reducer is pure; the caller supplies the clock). */
export type Timed<A> = A & { at?: number };
export type ViewerAction =
  | Timed<PaneAction>
  | { type: "undo" }
  | { type: "redo" }
  /** Replace the whole scene (continue from a history step); a command like any other, so it can be undone. */
  | { type: "applyScene"; scene: Scene; name: string; at?: number }
  /** An action that is not the person's command (a linked pane following the other): applied, not recorded. */
  | { type: "fact"; action: PaneAction };

export const emptyUndo = (): UndoHistory => ({ past: [], future: [] });
export const initialViewer = (pane: PaneState): ViewerState => ({ pane, undo: emptyUndo() });

/**
 * Restoring a scene keeps what is not the person's to undo: the saved regions are server rows (the scene mirrors the list as it
 * is now) and the link group follows the controls of the pair of panes.
 */
export function restore(current: Scene, target: Scene): Scene {
  const saved = regionsOf(current)?.saved;
  const overlays = target.overlays.map((o) => (o.id === REGIONS_ID && o.kind === "regions" && saved ? { ...o, saved } : o));
  const { link: _l, ...rest } = target;
  return { ...rest, overlays, ...(current.link ? { link: current.link } : {}) };
}

const same = (a: unknown, b: unknown) => a === b || JSON.stringify(a) === JSON.stringify(b);

/** Sets or removes (when absent from the source) every key of o whose value differs between from and to; the rest keep o's value. */
function mergeKeys<T extends object>(o: T, from: T, to: T, locked: readonly string[] = []): T {
  const out: Record<string, unknown> = { ...(o as Record<string, unknown>) };
  for (const k of new Set([...Object.keys(from), ...Object.keys(to)])) {
    if (locked.includes(k)) continue;
    const a = (from as Record<string, unknown>)[k];
    const b = (to as Record<string, unknown>)[k];
    if (same(a, b)) continue;
    if (b === undefined) delete out[k];
    else out[k] = b;
  }
  return out as T;
}

/**
 * Takes a scene from the state `from` (where a command left it) to `to` (where it was, or where redo goes) in `current`, changing
 * only what differs between those two. What changed since in a way that is not the command's (the declaration made the map axial,
 * a linked pane followed its partner, the shared scale, the worker's spectral value for the same channel, the saved regions, the
 * link group) is kept: undoing "draw region" must not undo the rest. The mapping is the catalog's and is never restored. With no
 * such change since, the result equals `to` exactly.
 */
export function revert(current: Scene, from: Scene, to: Scene): Scene {
  const channelMoved = from.base.channel !== to.base.channel;
  const { spectral: _s, ...baseNoSpec } = current.base;
  const base = {
    ...baseNoSpec,
    channel: channelMoved ? to.base.channel : current.base.channel,
    display: mergeKeys(current.base.display, from.base.display, to.base.display, ["mapping"]),
    ...((channelMoved ? to.base.spectral : current.base.spectral) ? { spectral: (channelMoved ? to.base.spectral : current.base.spectral)! } : {}),
  };
  const fromIds = from.overlays.map((o) => o.id);
  const toIds = to.overlays.map((o) => o.id);
  const byId = (xs: Scene["overlays"]) => new Map(xs.map((o) => [o.id, o] as const));
  const cur = byId(current.overlays);
  const fr = byId(from.overlays);
  const tt = byId(to.overlays);
  const pick = (id: string) => {
    const t = tt.get(id)!;
    const c = cur.get(id);
    if (!c || !same(fr.get(id), t)) {
      return t.kind === "regions" && c?.kind === "regions" ? { ...t, saved: c.saved } : t; // changed by the command: the target's; the saved regions stay the server's
    }
    return c;
  };
  let overlays: Scene["overlays"];
  if (same(fromIds, toIds)) overlays = current.overlays.map((o) => (tt.has(o.id) ? pick(o.id) : o));
  else {
    overlays = toIds.map(pick);
    current.overlays.forEach((o, i) => { // an overlay that came from neither side (the orientation lines a declaration added) stays where it was
      if (!fr.has(o.id) && !tt.has(o.id)) overlays.splice(Math.min(i, overlays.length), 0, o);
    });
  }
  return { ...current, base, overlays, view: mergeKeys(current.view, from.view, to.view) };
}

function record(undo: UndoHistory, e: SceneEdit): UndoHistory {
  const last = undo.past[undo.past.length - 1];
  if (last && e.at > 0 && last.key === e.key && e.at - last.at < COALESCE_MS && undo.future.length === 0) {
    const merged = { ...last, after: e.after, at: e.at };
    // a coalesced edit that ends where it began is no edit at all
    return same(merged.before, merged.after) ? { past: undo.past.slice(0, -1), future: [] } : { past: [...undo.past.slice(0, -1), merged], future: [] };
  }
  return { past: [...undo.past, e].slice(-MAX_UNDO), future: [] };
}

/** paneReducer plus a bounded undo history. A new dataset starts a new scene and forgets the history. */
export function viewerReducer(v: ViewerState, a: ViewerAction): ViewerState {
  switch (a.type) {
    case "undo": {
      const e = v.undo.past[v.undo.past.length - 1];
      if (!e || !v.pane.scene) return v;
      return { pane: { ...v.pane, scene: revert(v.pane.scene, e.after, e.before) }, undo: { past: v.undo.past.slice(0, -1), future: [...v.undo.future, e] } };
    }
    case "redo": {
      const e = v.undo.future[v.undo.future.length - 1];
      if (!e || !v.pane.scene) return v;
      return { pane: { ...v.pane, scene: revert(v.pane.scene, e.before, e.after) }, undo: { past: [...v.undo.past, e], future: v.undo.future.slice(0, -1) } };
    }
    case "fact":
      return { ...v, pane: paneReducer(v.pane, a.action) };
    case "applyScene": {
      const cur = v.pane.scene;
      if (!cur) return v;
      const next = restore(cur, a.scene);
      if (same(cur, next)) return v;
      return { pane: { ...v.pane, scene: next }, undo: record(v.undo, { name: a.name, key: "apply", before: cur, after: next, at: 0 }) };
    }
    case "resetForDataset":
      return { pane: paneReducer(v.pane, a), undo: emptyUndo() };
    case "syncAxial":
      return { ...v, pane: paneReducer(v.pane, a) };
  }
  const { at, ...action } = a as Timed<SceneAction>;
  const before = v.pane.scene;
  const pane = paneReducer(v.pane, action as PaneAction);
  if (!before || pane.scene === before || !pane.scene) return pane === v.pane ? v : { ...v, pane };
  const n = commandName(action as SceneAction, before);
  if (!n) return { ...v, pane };
  return { pane, undo: record(v.undo, { ...n, before, after: pane.scene, at: at ?? 0 }) };
}

export const undoLabel = (u: UndoHistory) => u.past[u.past.length - 1]?.name ?? null;
export const redoLabel = (u: UndoHistory) => u.future[u.future.length - 1]?.name ?? null;
