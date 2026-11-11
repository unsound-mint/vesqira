import type { Scene, SceneAction, SceneLink } from "./scene";

/**
 * Panes that follow each other (ADR 0042, section 4). A group names its panes and which facets it links: the cursor, the
 * channel, and the view (pan and zoom). `basis` says how a facet is carried from one pane to another:
 * - "same-grid": the server has confirmed both maps share one pixel grid, so a pixel, a channel index and an image point mean
 *   the same in each. This is the fast path and needs no worker call.
 * - "wcs": different grids. The worker (astropy) states how a pixel of one map lands in the other and which channel of the
 *   other has the same spectral world value; this file only applies what it said. The browser never does WCS math.
 * Callers ask the group, never the grid check.
 */
export type LinkFacet = "cursor" | "channel" | "view";
export type LinkBasis = "same-grid" | "wcs";
export type PaneId = "a" | "b";
export type LinkGroup = { id: string; name: string; panes: PaneId[]; basis: LinkBasis; facets: Record<LinkFacet, boolean> };

export const DEFAULT_LINK_NAME = "Compare";
export const FACETS: { id: LinkFacet; label: string }[] = [
  { id: "cursor", label: "Cursor" },
  { id: "channel", label: "Channel" },
  { id: "view", label: "View" },
];

/** What the person chose for the two compare panes: the group's name and facets, who has left it, and whether linking different grids by WCS is on. */
export type LinkPrefs = { name: string; facets: Record<LinkFacet, boolean>; members: Record<PaneId, boolean>; wcs: boolean };
export const defaultPrefs = (): LinkPrefs => ({ name: DEFAULT_LINK_NAME, facets: { cursor: true, channel: true, view: false }, members: { a: true, b: true }, wcs: false });

/**
 * The compare panes' group, or null when they are not linked: fewer than two panes still in it, or different grids the person
 * has not chosen to link by WCS. Same grid links on its own (as before), and the person can still leave it.
 */
export function linkGroup(prefs: LinkPrefs, sameGrid: boolean): LinkGroup | null {
  const panes = (["a", "b"] as const).filter((p) => prefs.members[p]);
  if (panes.length < 2) return null;
  const basis: LinkBasis | null = sameGrid ? "same-grid" : prefs.wcs ? "wcs" : null;
  if (!basis) return null;
  return { id: "compare", name: prefs.name.trim() || DEFAULT_LINK_NAME, panes, basis, facets: prefs.facets };
}

/** The two compare panes as one group when their grids are confirmed identical: cursor and channel linked, view not (as before). */
export function compareGroup(linked: boolean): LinkGroup | null {
  return linked ? linkGroup(defaultPrefs(), true) : null;
}

export const links = (g: LinkGroup | null, facet: LinkFacet): boolean => !!g && g.facets[facet];

/** The group as a scene records it, from the point of view of one pane: how this pane was linked when the scene was made. */
export function sceneLink(g: LinkGroup | null, datasets: Record<PaneId, number | null>): SceneLink | null {
  if (!g) return null;
  const ids = g.panes.map((p) => datasets[p]).filter((d): d is number => d !== null);
  if (ids.length < 2 || new Set(ids).size !== ids.length) return null;
  return { id: g.id, name: g.name, basis: g.basis, facets: { ...g.facets }, datasets: ids };
}

/**
 * The actions that make `to` follow `from` for the group's linked facets, on the same grid. A channel follows only when
 * both cubes have the same number of channels; the view is copied as is, because an image point is the same pixel in both.
 */
export function follow(g: LinkGroup | null, from: Scene | null, to: Scene | null, counts: { from: number; to: number }): SceneAction[] {
  if (!g || g.basis !== "same-grid" || !from || !to) return [];
  const out: SceneAction[] = [];
  if (g.facets.channel && counts.from === counts.to && from.base.channel !== to.base.channel) out.push({ type: "setChannel", channel: from.base.channel });
  if (g.facets.view && JSON.stringify(from.view) !== JSON.stringify(to.view)) out.push({ type: "setView", view: { zoom: from.view.zoom, centre: from.view.centre ?? null } });
  return out;
}

// ---- linking by WCS: applying what the worker stated ----

export type Pt = { x: number; y: number };
export type Size = { w: number; h: number };
export type Region = { x0: number; y0: number; x1: number; y1: number };

/** The worker's answer for one direction on the sky (LinkSky): an affine map over a region with its measured error. Pixels are the worker's: 0-based, centres at integers, y up. */
export type SkyMap = {
  affine: readonly number[];
  region: Region;
  maxErrorPx: number;
  scale: number;
  bShape: readonly [number, number];
  frames: { from: string; to: string; sameFrame: boolean };
};

/** Image pixels the pane shows, in the worker's pixel coordinates (the view is in edge coordinates, y up: pixel i spans [i, i + 1)). */
export function visibleRegion(view: Scene["view"], image: Size): Region {
  const c = view.centre ?? { x: image.w / 2, y: image.h / 2 };
  const hw = image.w / view.zoom / 2;
  const hh = image.h / view.zoom / 2;
  return { x0: c.x - hw - 0.5, x1: c.x + hw - 0.5, y0: c.y - hh - 0.5, y1: c.y + hh - 0.5 };
}

export const padRegion = (r: Region, by: number): Region => {
  const px = (r.x1 - r.x0) * by;
  const py = (r.y1 - r.y0) * by;
  return { x0: r.x0 - px, x1: r.x1 + px, y0: r.y0 - py, y1: r.y1 + py };
};
export const contains = (outer: Region, inner: Region, slack = 0): boolean =>
  inner.x0 >= outer.x0 - slack && inner.x1 <= outer.x1 + slack && inner.y0 >= outer.y0 - slack && inner.y1 <= outer.y1 + slack;
const area = (r: Region) => (r.x1 - r.x0) * (r.y1 - r.y0);
/** Keep the region already asked for while the view stays inside it and is not much smaller (a smaller view has a tighter error bound, so it is asked again). */
export function regionToAsk(kept: Region | null, win: Region): Region {
  return kept && contains(kept, win) && area(kept) <= 6 * area(win) ? kept : padRegion(win, 0.25);
}

export const applyAffine = (m: readonly number[], p: Pt): Pt => ({ x: m[0]! * p.x + m[1]! * p.y + m[2]!, y: m[3]! * p.x + m[4]! * p.y + m[5]! });

export type Mapped =
  | { kind: "inside"; at: Pt; exactAt: Pt }
  | { kind: "outside"; exactAt: Pt }
  | { kind: "stale" };

/**
 * Where a pixel of the first map is in the second: the nearest pixel (the marker is drawn on a pixel) and whether it falls
 * inside the second map's footprint. A pixel outside the region the affine was fitted on has no answer yet ("stale": the
 * worker is asked again for the new view), never an extrapolation.
 */
export function mapPixel(m: SkyMap | null, p: Pt): Mapped {
  if (!m || !contains(m.region, { x0: p.x, x1: p.x, y0: p.y, y1: p.y }, 1e-9)) return { kind: "stale" };
  const e = applyAffine(m.affine, p);
  const at = { x: Math.round(e.x), y: Math.round(e.y) };
  const [w, h] = m.bShape;
  return at.x >= 0 && at.y >= 0 && at.x < w && at.y < h ? { kind: "inside", at, exactAt: e } : { kind: "outside", exactAt: e };
}

/** The worker's channel answer for one direction (LinkSpectral): for every channel of the first map, the second's nearest. */
export type ChannelMap = { index: (number | null)[]; residual: (number | null)[]; outside: boolean[]; unit: string; basis: string };

export type ChannelTarget = { channel: number; residual: number; unit: string } | { outside: true; residual: number; unit: string } | null;
export function channelTarget(m: ChannelMap | null, channel: number): ChannelTarget {
  if (!m) return null;
  const j = m.index[channel] ?? null;
  const r = m.residual[channel] ?? null;
  if (j === null || r === null) return null;
  return m.outside[channel] ? { outside: true, residual: r, unit: m.unit } : { channel: j, residual: r, unit: m.unit };
}

/**
 * The view of the second pane that shows the sky the first pane shows: its centre carried through the affine, and its zoom
 * scaled by the pixel-size ratio. Approximate by construction: a view is a centre and a zoom, so a rotated grid, or a pane
 * of another shape, shows a similar and not the same region (`rotated`), and zoom is limited to 1 to 16 (`clamped`).
 */
export function mapView(m: SkyMap, from: Scene["view"], fromImage: Size, toImage: Size): { zoom: number; centre: Pt | null; clamped: boolean; rotated: boolean } {
  const [a, b, , c, d] = m.affine as number[];
  const rotated = Math.abs(Math.atan2(c! - b!, a! + d!)) > (1 * Math.PI) / 180;
  const raw = (from.zoom * Math.max(toImage.w, toImage.h)) / (Math.max(fromImage.w, fromImage.h) * m.scale);
  const zoom = Math.min(16, Math.max(1, raw));
  const centreA = from.centre ?? { x: fromImage.w / 2, y: fromImage.h / 2 };
  const e = applyAffine(m.affine, { x: centreA.x - 0.5, y: centreA.y - 0.5 });
  const centre = zoom === 1 ? null : { x: Math.min(toImage.w, Math.max(0, e.x + 0.5)), y: Math.min(toImage.h, Math.max(0, e.y + 0.5)) };
  return { zoom, centre, clamped: zoom !== raw, rotated };
}

/** The actions that make `to` show what `from` shows, through the sky: its view (approximate, see mapView) and its channel (by spectral value). */
export function followWcs(
  g: LinkGroup | null,
  from: Scene | null,
  to: Scene | null,
  ctx: { sky: SkyMap | null; channels: ChannelMap | null; fromImage: Size | null; toImage: Size | null },
  only: { channel?: boolean; view?: boolean } = {},
): SceneAction[] {
  if (!g || g.basis !== "wcs" || !from || !to) return [];
  const out: SceneAction[] = [];
  if (g.facets.channel && only.channel !== false) {
    const t = channelTarget(ctx.channels, from.base.channel);
    if (t && "channel" in t && t.channel !== to.base.channel) out.push({ type: "setChannel", channel: t.channel });
  }
  if (g.facets.view && only.view !== false && ctx.sky && ctx.fromImage && ctx.toImage) {
    const v = mapView(ctx.sky, from.view, ctx.fromImage, ctx.toImage);
    const next = { zoom: v.zoom, centre: v.centre };
    if (JSON.stringify({ zoom: to.view.zoom, centre: to.view.centre ?? null }) !== JSON.stringify(next)) out.push({ type: "setView", view: next });
  }
  return out;
}

// ---- what the link pill says ----

export type Pill = { text: string; tone: "ok" | "warn" | "neutral" };
export type SkyState = { status: "idle" | "loading" | "ready" | "refused"; refused: string | null; map: SkyMap | null };
export type ChannelState = { status: "idle" | "loading" | "ready" | "refused"; refused: string | null; map: ChannelMap | null };

const px = (v: number) => (v < 0.005 ? "< 0.01" : v.toFixed(2));
const joined = (names: string[]) => (names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`);

/** The facets a group links, in words: "cursor and channel". */
export const facetWords = (g: LinkGroup) => joined(FACETS.filter((f) => g.facets[f.id]).map((f) => f.id));

/**
 * What the link pill states: the basis (same grid, or WCS with the frames and the error bound) and, for a facet that cannot
 * be linked, the reason. `sky` is the state of the active direction.
 */
export function linkPills(g: LinkGroup, sky: SkyState, channel: ChannelState): Pill[] {
  const words = facetWords(g);
  if (g.basis === "same-grid") {
    const n = FACETS.filter((f) => g.facets[f.id]).length;
    return [{ text: n === 0 ? "Same grid: nothing is linked" : `Same grid: ${words} ${n > 1 ? "are" : "is"} linked`, tone: n === 0 ? "neutral" : "ok" }];
  }
  const out: Pill[] = [];
  if (g.facets.cursor || g.facets.view) {
    if (sky.status === "refused") out.push({ text: `${g.facets.cursor ? "Cursor" : "View"} link refused: ${sky.refused}`, tone: "warn" });
    else if (sky.status !== "ready" || !sky.map) out.push({ text: "Linked by WCS: computing the transform…", tone: "neutral" });
    else {
      const f = sky.map.frames;
      const frames = f.sameFrame ? `frame ${f.from}` : `frames ${f.from}↔${f.to}`;
      const bound = sky.map.maxErrorPx >= 0.5 ? `approximate: up to ${px(sky.map.maxErrorPx)} px in this view, zoom in to tighten` : `cursor within ${px(sky.map.maxErrorPx)} px in this view`;
      out.push({ text: `Linked by WCS: ${frames} (astropy), ${bound}`, tone: sky.map.maxErrorPx >= 0.5 ? "warn" : "ok" });
    }
  }
  if (g.facets.channel) {
    if (channel.status === "refused") out.push({ text: `Channel link refused: ${channel.refused}`, tone: "warn" });
    else if (channel.status !== "ready") out.push({ text: "Channel link: computing…", tone: "neutral" });
    else out.push({ text: "Channel linked by spectral value", tone: "ok" });
  }
  return out;
}
