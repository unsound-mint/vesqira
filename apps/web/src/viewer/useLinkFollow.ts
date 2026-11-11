import { useEffect, useRef } from "react";
import { followWcs } from "../scene/link";
import type { ChannelState, LinkGroup, PaneId, SkyMap, SkyState } from "../scene/link";
import type { Viewer } from "./useViewer";

type Panes = Record<PaneId, Viewer>;
type Facet = "ch" | "view";
const PANES = ["a", "b"] as const;
const other = (p: PaneId): PaneId => (p === "a" ? "b" : "a");
const flags = () => ({ ch: { a: false, b: false }, view: { a: false, b: false } });

type Seen = { key: string; ch: Record<PaneId, number | null>; view: Record<PaneId, string>; sky: Record<PaneId, SkyMap | null> };

/**
 * Makes a pane follow the one the person moves, for a group linked by WCS (ADR 0042, section 4). It reacts to a pane's own
 * change, never to which pane is active: nearest-channel and clamped-zoom mappings are not invertible, so following the
 * active pane would make the other jump the moment the inspector switched. A change this hook made itself is not taken for
 * the person's (it would bounce back). When a facet is switched on or its transform arrives, the active pane leads once; when
 * a view transform is replaced (the view left its region, or zoomed in for a tighter bound), the pane that last led is followed again.
 */
export function useLinkFollow(group: LinkGroup | null, v: Panes, active: PaneId, sky: Record<"ab" | "ba", SkyState>, chan: Record<"ab" | "ba", ChannelState>, ids: Record<PaneId, number | null>): void {
  const seen = useRef<Seen | null>(null);
  const mine = useRef(flags()); // a change this hook dispatched, not yet seen come back
  const led = useRef<PaneId>("a"); // the pane whose view was last followed
  const sa = v.a.scene;
  const sb = v.b.scene;
  const key = group && group.basis === "wcs"
    ? [ids.a, ids.b, group.facets.channel, group.facets.view, chan.ab.map !== null, chan.ba.map !== null, sky.ab.map !== null, sky.ba.map !== null].join("|")
    : null;
  const cha = sa?.base.channel ?? null;
  const chb = sb?.base.channel ?? null;
  const viewA = sa ? JSON.stringify(sa.view) : "";
  const viewB = sb ? JSON.stringify(sb.view) : "";
  const imageA = v.a.view ? { w: v.a.view.width, h: v.a.view.height } : null;
  const imageB = v.b.view ? { w: v.b.view.width, h: v.b.view.height } : null;
  useEffect(() => {
    if (!group || key === null || !sa || !sb) {
      seen.current = null;
      mine.current = flags();
      return;
    }
    const prev = seen.current;
    const cur: Seen = { key, ch: { a: cha, b: chb }, view: { a: viewA, b: viewB }, sky: { a: sky.ab.map, b: sky.ba.map } };
    seen.current = cur;
    const scenes = { a: sa, b: sb };
    const images = { a: imageA, b: imageB };
    const drive = (from: PaneId, facet: Facet) => {
      const to = other(from);
      const dir = from === "a" ? "ab" : "ba";
      const only = facet === "ch" ? { channel: true, view: false } : { channel: false, view: true };
      const actions = followWcs(group, scenes[from], scenes[to], { sky: sky[dir].map, channels: chan[dir].map, fromImage: images[from], toImage: images[to] }, only);
      for (const a of actions) {
        mine.current[facet][to] = true;
        v[to].dispatch(a);
      }
      if (facet === "view") led.current = from;
    };
    const fresh = !prev || prev.key !== key;
    for (const facet of ["ch", "view"] as const) {
      if (fresh) {
        drive(active, facet);
        continue;
      }
      const changed = (p: PaneId) => cur[facet][p] !== prev[facet][p];
      for (const p of PANES) if (mine.current[facet][p] && !changed(p)) mine.current[facet][p] = false; // our dispatch changed nothing
      const driver = PANES.find((p) => {
        if (!changed(p)) return false;
        if (mine.current[facet][p]) {
          mine.current[facet][p] = false; // our own change arriving
          return false;
        }
        return true;
      });
      if (driver) drive(driver, facet);
      else if (facet === "view" && cur.sky[led.current] !== prev.sky[led.current]) drive(led.current, facet);
    }
  }, [key, cha, chb, viewA, viewB, active, sky.ab.map, sky.ba.map, chan.ab.map, chan.ba.map]); // eslint-disable-line react-hooks/exhaustive-deps
}
