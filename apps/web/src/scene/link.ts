import type { Scene, SceneAction } from "./scene";

/**
 * Panes that follow each other (ADR 0042, section 4). A group names its panes and which facets it links: the cursor, the
 * channel, and the view (pan and zoom). basis says how a facet is carried from one pane to another. Only "same-grid" exists
 * yet: the server has confirmed both maps share one pixel grid, so a pixel, a channel index and an image point mean the
 * same in each. Linking through WCS (sky position, spectral world value) is a later basis; callers ask the group, never
 * the grid check, so it can arrive without touching them.
 */
export type LinkFacet = "cursor" | "channel" | "view";
export type LinkGroup = { id: string; panes: string[]; basis: "same-grid"; facets: Record<LinkFacet, boolean> };

/** The two compare panes are one group when their grids are confirmed identical: cursor and channel linked, view not (as before). */
export function compareGroup(linked: boolean): LinkGroup | null {
  return linked ? { id: "compare", panes: ["a", "b"], basis: "same-grid", facets: { cursor: true, channel: true, view: false } } : null;
}

export const links = (g: LinkGroup | null, facet: LinkFacet): boolean => !!g && g.facets[facet];

/**
 * The actions that make `to` follow `from` for the group's linked facets. On the same grid a channel follows only when
 * both cubes have the same number of channels; the view is copied as is, because an image point is the same pixel in both.
 */
export function follow(g: LinkGroup | null, from: Scene | null, to: Scene | null, counts: { from: number; to: number }): SceneAction[] {
  if (!g || !from || !to) return [];
  const out: SceneAction[] = [];
  if (g.facets.channel && counts.from === counts.to && from.base.channel !== to.base.channel) out.push({ type: "setChannel", channel: from.base.channel });
  if (g.facets.view && JSON.stringify(from.view) !== JSON.stringify(to.view)) out.push({ type: "setView", view: { zoom: from.view.zoom, centre: from.view.centre ?? null } });
  return out;
}
