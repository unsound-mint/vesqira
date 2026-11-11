import { describe, expect, it } from "vitest";
import fixture from "../../../../packages/contracts/fixtures/scenes.json";
import { channelTarget, compareGroup, contains, defaultPrefs, followWcs, linkGroup, linkPills, mapPixel, mapView, regionToAsk, sceneLink, visibleRegion } from "./link";
import type { ChannelMap, ChannelState, LinkGroup, SkyMap, SkyState } from "./link";
import { initialPane, paneReducer, sceneReducer } from "./scene";
import type { Scene } from "./scene";

const scene = (channel = 0, view: Scene["view"] = { zoom: 1 }): Scene => ({
  schema: "scene/1", base: { datasetId: 1, channel, display: { mapping: "scalar", colormap: "viridis", stretch: "linear", range: "percentile" } }, overlays: [], view,
});

// B is A shifted by (+10, -3) and twice as fine: x' = 2x + 10, y' = 2y - 3 (a pure scale and shift)
const sky = (over: Partial<SkyMap> = {}): SkyMap => ({
  affine: [2, 0, 10, 0, 2, -3], region: { x0: -0.5, y0: -0.5, x1: 39.5, y1: 39.5 }, maxErrorPx: 0.03, scale: 2, bShape: [100, 80],
  frames: { from: "ICRS", to: "Galactic", sameFrame: false }, ...over,
});
const channels = (): ChannelMap => ({ index: [0, 0, 1, 2, null], residual: [0.4, -0.12, 0, 0.5, null], outside: [false, false, false, true, false], unit: "m/s", basis: "x" });
const wcsGroup = (facets = { cursor: true, channel: true, view: true }): LinkGroup => ({ ...linkGroup({ ...defaultPrefs(), wcs: true }, false)!, facets });

describe("the compare panes' link group", () => {
  it("same grid links on its own with cursor and channel (as before); different grids only once chosen", () => {
    expect(linkGroup(defaultPrefs(), true)).toMatchObject({ basis: "same-grid", panes: ["a", "b"], facets: { cursor: true, channel: true, view: false } });
    expect(compareGroup(true)).toEqual(linkGroup(defaultPrefs(), true));
    expect(linkGroup(defaultPrefs(), false)).toBeNull();
    expect(linkGroup({ ...defaultPrefs(), wcs: true }, false)).toMatchObject({ basis: "wcs" });
  });
  it("a group needs two panes: leaving dissolves it, and the person's name is kept (trimmed, never empty)", () => {
    expect(linkGroup({ ...defaultPrefs(), members: { a: true, b: false } }, true)).toBeNull();
    expect(linkGroup({ ...defaultPrefs(), name: "  H I and CO " }, true)!.name).toBe("H I and CO");
    expect(linkGroup({ ...defaultPrefs(), name: "  " }, true)!.name).toBe("Compare");
  });
  it("is recorded in the scene with the datasets of its panes, and only when there are two distinct ones", () => {
    const g = wcsGroup();
    expect(sceneLink(g, { a: 1, b: 2 })).toEqual({ id: "compare", name: "Compare", basis: "wcs", facets: { cursor: true, channel: true, view: true }, datasets: [1, 2] });
    expect(sceneLink(g, { a: 1, b: null })).toBeNull();
    expect(sceneLink(g, { a: 1, b: 1 })).toBeNull();
    expect(sceneLink(null, { a: 1, b: 2 })).toBeNull();
  });
  it("setLink records the group, null removes it, and opening another dataset starts without one", () => {
    const l = sceneLink(wcsGroup(), { a: 1, b: 2 })!;
    const s = sceneReducer(scene(), { type: "setLink", link: l });
    expect(s.link).toEqual(l);
    expect(sceneReducer(s, { type: "setLink", link: l })).toBe(s);
    expect("link" in sceneReducer(s, { type: "setLink", link: null })).toBe(false);
    const p = paneReducer({ ...initialPane(1, false), scene: s }, { type: "resetForDataset", datasetId: 3, axial: false });
    expect(p.scene!.link).toBeUndefined();
  });
  it("the link fixtures are valid scenes the reducer keeps whole", () => {
    const linked = fixture.valid.map((v) => v.scene as unknown as Scene).filter((v) => v.link);
    expect(linked.length).toBeGreaterThanOrEqual(2);
    for (const l of linked) expect(sceneReducer(l, { type: "setView", view: { zoom: l.view.zoom } })).toBe(l);
  });
});

describe("a pixel in the other map, from the worker's transform", () => {
  it("rounds to the nearest pixel and says when it falls outside the footprint", () => {
    expect(mapPixel(sky(), { x: 5, y: 7 })).toMatchObject({ kind: "inside", at: { x: 20, y: 11 } });
    expect(mapPixel(sky({ affine: [2, 0, 10.4, 0, 2, -3.4] }), { x: 5, y: 7 })).toMatchObject({ kind: "inside", at: { x: 20, y: 11 } }); // 20.4 -> 20, 10.6 -> 11
    expect(mapPixel(sky(), { x: 0, y: 0 })).toMatchObject({ kind: "outside", exactAt: { x: 10, y: -3 } }); // y' = -3: below the footprint
    expect(mapPixel(sky(), { x: 39, y: 39 })).toMatchObject({ kind: "inside", at: { x: 88, y: 75 } });
    expect(mapPixel(sky({ bShape: [88, 76] }), { x: 39, y: 39 })).toMatchObject({ kind: "outside" });
  });
  it("gives no answer outside the region the transform was fitted on (never an extrapolation), nor before there is one", () => {
    expect(mapPixel(sky(), { x: 60, y: 5 })).toEqual({ kind: "stale" });
    expect(mapPixel(null, { x: 5, y: 5 })).toEqual({ kind: "stale" });
  });
});

describe("the view in the other map", () => {
  const img = { w: 40, h: 40 };
  it("carries the centre through the transform and scales the zoom by the pixel ratio", () => {
    // A zoomed 4x on (20, 20) in edge coordinates: worker pixel (19.5, 19.5) -> (49, 36) -> edge (49.5, 36.5)
    const v = mapView(sky({ scale: 2 }), { zoom: 4, centre: { x: 20, y: 20 } }, img, { w: 100, h: 80 });
    expect(v.centre).toEqual({ x: 49.5, y: 36.5 });
    expect(v.zoom).toBeCloseTo((4 * 100) / (40 * 2));
    expect(v).toMatchObject({ clamped: false, rotated: false });
  });
  it("is clamped to 1..16 and says so; a rotated grid is approximate and says so", () => {
    expect(mapView(sky({ scale: 0.1 }), { zoom: 8, centre: { x: 5, y: 5 } }, img, img)).toMatchObject({ zoom: 16, clamped: true });
    expect(mapView(sky({ scale: 4 }), { zoom: 2, centre: { x: 5, y: 5 } }, img, img)).toMatchObject({ zoom: 1, centre: null, clamped: true });
    const c = Math.cos(Math.PI / 6);
    const s = Math.sin(Math.PI / 6);
    expect(mapView(sky({ affine: [c, -s, 0, s, c, 0], scale: 1 }), { zoom: 2, centre: { x: 5, y: 5 } }, img, img).rotated).toBe(true);
  });
  it("the region asked of the worker is the pane's window with a margin, kept while the view stays inside it and is not much smaller", () => {
    const win = visibleRegion({ zoom: 4, centre: { x: 20, y: 20 } }, { w: 40, h: 40 });
    expect(win).toEqual({ x0: 14.5, x1: 24.5, y0: 14.5, y1: 24.5 }); // 10 px wide, centred on edge coordinate 20 = pixel 19.5
    const asked = regionToAsk(null, win);
    expect(contains(asked, win)).toBe(true);
    expect(regionToAsk(asked, { ...win, x0: win.x0 + 1, x1: win.x1 + 1 })).toBe(asked); // a small pan asks nothing new
    expect(regionToAsk(asked, { x0: win.x0 + 20, x1: win.x1 + 20, y0: win.y0, y1: win.y1 })).not.toBe(asked); // left the region
    const zoomedIn = visibleRegion({ zoom: 16, centre: { x: 20, y: 20 } }, { w: 40, h: 40 });
    expect(regionToAsk(asked, zoomedIn)).not.toBe(asked); // a much smaller view has a tighter error bound: asked again
  });
});

describe("the channel in the other map", () => {
  it("is the worker's nearest channel with its residual; beyond the other map's range it does not follow", () => {
    expect(channelTarget(channels(), 1)).toEqual({ channel: 0, residual: -0.12, unit: "m/s" });
    expect(channelTarget(channels(), 3)).toEqual({ outside: true, residual: 0.5, unit: "m/s" });
    expect(channelTarget(channels(), 4)).toBeNull(); // no spectral value here
    expect(channelTarget(null, 0)).toBeNull();
  });
  const ctx = { sky: sky(), channels: channels(), fromImage: { w: 40, h: 40 }, toImage: { w: 100, h: 80 } };
  it("followWcs moves the channel and the view of the other pane, and nothing when it is already there or the facet is off", () => {
    const g = wcsGroup();
    expect(followWcs(g, scene(2), scene(0), ctx, { view: false })).toEqual([{ type: "setChannel", channel: 1 }]);
    expect(followWcs(g, scene(2), scene(1), ctx, { view: false })).toEqual([]);
    expect(followWcs(g, scene(3), scene(0), ctx, { view: false })).toEqual([]); // outside B's range
    expect(followWcs(wcsGroup({ cursor: true, channel: false, view: false }), scene(2), scene(0), ctx)).toEqual([]);
    const acts = followWcs(g, scene(0, { zoom: 4, centre: { x: 20, y: 20 } }), scene(0), ctx, { channel: false });
    expect(acts).toEqual([{ type: "setView", view: { zoom: 5, centre: { x: 49.5, y: 36.5 } } }]);
    expect(followWcs(g, scene(0, { zoom: 4, centre: { x: 20, y: 20 } }), sceneReducer(scene(0), { type: "setView", view: { zoom: 5, centre: { x: 49.5, y: 36.5 } } }), ctx, { channel: false })).toEqual([]);
  });
  it("is not what the same-grid fast path does: that basis never uses the worker's tables", () => {
    expect(followWcs(compareGroup(true), scene(2), scene(0), ctx)).toEqual([]);
  });
});

describe("what the link pill states", () => {
  const ready = (m: SkyMap): SkyState => ({ status: "ready", refused: null, map: m });
  const chReady: ChannelState = { status: "ready", refused: null, map: channels() };
  it("same grid keeps its words", () => {
    expect(linkPills(compareGroup(true)!, { status: "idle", refused: null, map: null }, { status: "idle", refused: null, map: null })).toEqual([{ text: "Same grid: cursor and channel are linked", tone: "ok" }]);
    const view = { ...compareGroup(true)!, facets: { cursor: false, channel: false, view: true } };
    expect(linkPills(view, ready(sky()), chReady)[0]!.text).toBe("Same grid: view is linked");
  });
  it("WCS names the frames, the program and the error bound; a larger error is called approximate", () => {
    const [p] = linkPills(wcsGroup({ cursor: true, channel: false, view: false }), ready(sky()), chReady);
    expect(p).toEqual({ text: "Linked by WCS: frames ICRS↔Galactic (astropy), cursor within 0.03 px in this view", tone: "ok" });
    const same = linkPills(wcsGroup({ cursor: true, channel: false, view: false }), ready(sky({ frames: { from: "ICRS", to: "ICRS", sameFrame: true } })), chReady)[0]!;
    expect(same.text).toContain("frame ICRS (astropy)");
    const rough = linkPills(wcsGroup({ cursor: true, channel: false, view: false }), ready(sky({ maxErrorPx: 1.234 })), chReady)[0]!;
    expect(rough).toMatchObject({ tone: "warn" });
    expect(rough.text).toContain("approximate: up to 1.23 px");
  });
  it("a refused facet says why, and does not hide the other", () => {
    const refused: ChannelState = { status: "refused", refused: "spectral frames differ (LSRK vs TOPOCENT); changing frame is not offered", map: null };
    const pills = linkPills(wcsGroup({ cursor: true, channel: true, view: false }), ready(sky()), refused);
    expect(pills.map((p) => p.text)).toEqual([
      "Linked by WCS: frames ICRS↔Galactic (astropy), cursor within 0.03 px in this view",
      "Channel link refused: spectral frames differ (LSRK vs TOPOCENT); changing frame is not offered",
    ]);
    expect(pills[1]!.tone).toBe("warn");
    const skyRefused: SkyState = { status: "refused", refused: "the second map has no celestial WCS", map: null };
    expect(linkPills(wcsGroup({ cursor: true, channel: false, view: false }), skyRefused, chReady)[0]!.text).toBe("Cursor link refused: the second map has no celestial WCS");
  });
});
