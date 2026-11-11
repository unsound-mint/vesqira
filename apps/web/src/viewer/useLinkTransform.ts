import { useEffect, useMemo, useState } from "react";
import { ApiError } from "../api/catalog";
import { postLink } from "../api/link";
import type { LinkReply } from "../api/link";
import { regionToAsk, visibleRegion } from "../scene/link";
import type { ChannelMap, ChannelState, Region, Size, SkyMap, SkyState } from "../scene/link";
import type { Scene } from "../scene/scene";

const IDLE_SKY: SkyState = { status: "idle", refused: null, map: null };
const IDLE_CHANNEL: ChannelState = { status: "idle", refused: null, map: null };
const SETTLE_MS = 120; // a zoom or pan gesture settles before the worker is asked

const reason = (e: unknown) => (e instanceof ApiError ? e.message : "The link could not be computed.");
const aborted = (e: unknown) => e instanceof DOMException && e.name === "AbortError";

function skyOf(r: LinkReply): SkyState {
  const s = r.sky;
  if (!s) return { status: "refused", refused: "the worker sent no sky transform", map: null };
  if (s.refused || !s.affine || !s.region || s.max_error_px === undefined || s.scale === undefined || !s.b_shape || !s.frames) return { status: "refused", refused: s.refused ?? "the worker sent an incomplete transform", map: null };
  const map: SkyMap = {
    affine: s.affine, region: { x0: s.region.x0, y0: s.region.y0, x1: s.region.x1, y1: s.region.y1 }, maxErrorPx: s.max_error_px, scale: s.scale,
    bShape: [s.b_shape[0]!, s.b_shape[1]!], frames: { from: s.frames.from ?? "", to: s.frames.to ?? "", sameFrame: !!s.frames.same_frame },
  };
  return { status: "ready", refused: null, map };
}

function channelsOf(r: LinkReply): ChannelState {
  const s = r.spectral;
  if (!s) return { status: "refused", refused: "the worker sent no channel link", map: null };
  if (s.refused || !s.index || !s.residual || !s.outside || !s.unit) return { status: "refused", refused: s.refused ?? "the worker sent an incomplete channel link", map: null };
  const map: ChannelMap = { index: s.index, residual: s.residual, outside: s.outside, unit: s.unit, basis: s.basis ?? "" };
  return { status: "ready", refused: null, map };
}

/**
 * How pixels of the map `from` land in the map `to` on the sky (ADR 0042, section 4), from the worker, over the region of
 * `from` its pane shows. The region is asked once with a margin and kept while the view stays inside it; a much smaller view
 * is asked again, because the approximation's measured error is per region. The last answer stays until the next arrives
 * (consumers check that a point is inside its region), and is dropped when the pair changes.
 */
export function useSkyLink(enabled: boolean, from: number | null, to: number | null, scene: Scene | null, image: Size | null, pane: Size | null, stamp: string): SkyState {
  const [state, setState] = useState<{ key: string; s: SkyState }>({ key: "", s: IDLE_SKY });
  const [kept, setKept] = useState<Region | null>(null);
  const pair = `${from}>${to}|${stamp}`;
  const view = scene?.view;
  const win = useMemo(() => (view && image ? visibleRegion(view, image, pane) : null), [view?.zoom, view?.centre?.x, view?.centre?.y, image?.w, image?.h, pane?.w, pane?.h]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setKept(null);
  }, [pair]);
  useEffect(() => {
    if (!enabled || !win) return;
    setKept((k) => regionToAsk(k, win));
  }, [enabled, win?.x0, win?.x1, win?.y0, win?.y1, pair]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!enabled || from === null || to === null || !kept) return;
    const ac = new AbortController();
    const t = setTimeout(() => {
      postLink(from, { otherId: to, region: kept }, ac.signal)
        .then((r) => setState({ key: pair, s: skyOf(r) }))
        .catch((e: unknown) => {
          if (!aborted(e)) setState({ key: pair, s: { status: "refused", refused: reason(e), map: null } });
        });
    }, SETTLE_MS);
    return () => {
      clearTimeout(t);
      ac.abort();
    };
  }, [enabled, from, to, kept?.x0, kept?.x1, kept?.y0, kept?.y1, pair]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!enabled) return IDLE_SKY;
  if (state.key !== pair) return { status: "loading", refused: null, map: null };
  return state.s;
}

/** The channel correspondence from map `from` to map `to`, once per pair (the whole table in one reply), refused with the worker's reason. */
export function useChannelLink(enabled: boolean, from: number | null, to: number | null, stamp: string): ChannelState {
  const pair = `${from}>${to}|${stamp}`;
  const [state, setState] = useState<{ key: string; s: ChannelState }>({ key: "", s: IDLE_CHANNEL });
  useEffect(() => {
    if (!enabled || from === null || to === null) return;
    const ac = new AbortController();
    postLink(from, { otherId: to, channels: true }, ac.signal)
      .then((r) => setState({ key: pair, s: channelsOf(r) }))
      .catch((e: unknown) => {
        if (!aborted(e)) setState({ key: pair, s: { status: "refused", refused: reason(e), map: null } });
      });
    return () => ac.abort();
  }, [enabled, from, to, pair]);
  if (!enabled) return IDLE_CHANNEL;
  if (state.key !== pair) return { status: "loading", refused: null, map: null };
  return state.s;
}
