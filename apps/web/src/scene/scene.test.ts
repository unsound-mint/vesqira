import { describe, expect, it } from "vitest";
import fixture from "../../../../packages/contracts/fixtures/scenes.json";
import { DEFAULT_DISPLAY, MAX_OVERLAYS, initialPane, newScene, orientationOf, paneReducer, regionsOf, sceneReducer } from "./scene";
import type { PaneAction, Scene, SceneAction, SceneOverlay } from "./scene";
import { compareGroup, follow, links } from "./link";

const valid = fixture.valid.map((v) => v.scene as unknown as Scene);
const base = () => valid[0]!; // scalar plane, channel 3, a working region and a saved one, zoomed

function deepFreeze<T>(o: T): T {
  if (o && typeof o === "object" && !Object.isFrozen(o)) {
    Object.freeze(o);
    for (const v of Object.values(o)) deepFreeze(v);
  }
  return o;
}

const beam = (id: string): SceneOverlay => ({ id, kind: "beam", datasetId: 1, visible: true });

// one of every action, each of which changes the fixture scene
const everyAction: SceneAction[] = [
  { type: "setChannel", channel: 5 },
  { type: "setSpectral", channel: 3, spectral: { value: 1.4204e9, unit: "Hz" } },
  { type: "setDisplay", display: { colormap: "inferno", limits: null } },
  { type: "addOverlay", overlay: beam("c"), index: 0 },
  { type: "updateOverlay", id: "regions", patch: { active: null } },
  { type: "removeOverlay", id: "regions" },
  { type: "moveOverlay", id: "b", to: 0 },
  { type: "setView", view: { zoom: 4, centre: { x: 1, y: 2 } } },
];

describe("the scene reducer", () => {
  it("changes the channel and forgets the old channel's world value; the worker's value is recorded only for the channel shown", () => {
    const s = sceneReducer(base(), { type: "setChannel", channel: 7 });
    expect(s.base.channel).toBe(7);
    expect(s.base.spectral).toBeUndefined();
    expect(sceneReducer(s, { type: "setSpectral", channel: 6, spectral: { value: 1, unit: "m/s" } })).toBe(s); // a late reply for another channel
    expect(sceneReducer(s, { type: "setSpectral", channel: 7, spectral: { value: 2, unit: "m/s" } }).base.spectral).toEqual({ value: 2, unit: "m/s" });
    expect(sceneReducer(base(), { type: "setChannel", channel: -3 }).base.channel).toBe(0);
    expect(sceneReducer(base(), { type: "setChannel", channel: 3 })).toBe(base());
  });

  it("sets display fields, removes optional ones with null, and never changes the mapping (the catalog declares it)", () => {
    const s = sceneReducer(base(), { type: "setDisplay", display: { stretch: "sqrt", limits: null, scaleGroup: null } });
    expect(s.base.display).toEqual({ mapping: "scalar", colormap: "viridis", stretch: "sqrt", range: "custom", limitsUnit: "K" });
    expect(sceneReducer(base(), { type: "setDisplay", display: { mapping: "axial" } as never }).base.display.mapping).toBe("scalar");
    expect(sceneReducer(base(), { type: "setDisplay", display: { colormap: "viridis", limits: [0.5, 2] } })).toBe(base());
  });

  it("adds, updates, removes and reorders overlays by id", () => {
    let s = sceneReducer(base(), { type: "addOverlay", overlay: beam("b") });
    expect(s.overlays.map((o) => o.id)).toEqual(["regions", "b"]);
    expect(sceneReducer(s, { type: "addOverlay", overlay: beam("b") })).toBe(s); // ids are unique
    s = sceneReducer(s, { type: "moveOverlay", id: "b", to: 0 });
    expect(s.overlays.map((o) => o.id)).toEqual(["b", "regions"]);
    expect(sceneReducer(s, { type: "moveOverlay", id: "b", to: 99 }).overlays.map((o) => o.id)).toEqual(["regions", "b"]);
    s = sceneReducer(s, { type: "updateOverlay", id: "b", patch: { visible: false, id: "x", kind: "grid" } });
    expect(s.overlays[0]).toEqual({ id: "b", kind: "beam", datasetId: 1, visible: false }); // id and kind cannot be patched
    s = sceneReducer(s, { type: "removeOverlay", id: "b" });
    expect(s.overlays.map((o) => o.id)).toEqual(["regions"]);
    expect(sceneReducer(s, { type: "removeOverlay", id: "b" })).toBe(s);
    let full = base();
    for (let i = 0; i < MAX_OVERLAYS + 3; i++) full = sceneReducer(full, { type: "addOverlay", overlay: beam(`b${i}`) });
    expect(full.overlays).toHaveLength(MAX_OVERLAYS);
  });

  it("clears the working region with null and keeps an untouched region's identity (effects depend on it)", () => {
    const b = base();
    const active = regionsOf(b)!.active;
    const s = sceneReducer(b, { type: "updateOverlay", id: "regions", patch: { saved: [] } });
    expect(regionsOf(s)!.active).toBe(active);
    expect(sceneReducer(s, { type: "setChannel", channel: 9 }).overlays).toBe(s.overlays);
    const cleared = sceneReducer(b, { type: "updateOverlay", id: "regions", patch: { active: null } });
    expect("active" in regionsOf(cleared)!).toBe(false);
  });

  it("keeps zoom within 1 to 16 and forgets the centre when fitted again", () => {
    expect(sceneReducer(base(), { type: "setView", view: { zoom: 99 } }).view).toEqual({ zoom: 16, centre: { x: 10.5, y: 4 } });
    expect(sceneReducer(base(), { type: "setView", view: { zoom: 0.2 } }).view).toEqual({ zoom: 1 });
    expect(sceneReducer(base(), { type: "setView", view: { centre: null } }).view).toEqual({ zoom: 2.5 });
    expect(sceneReducer(base(), { type: "setView", view: { zoom: Number.NaN } }).view).toEqual({ zoom: 1 });
  });

  it("never mutates the scene it is given", () => {
    const frozen = deepFreeze(sceneReducer(structuredClone(base()), { type: "addOverlay", overlay: beam("b") }));
    const before = JSON.stringify(frozen);
    for (const a of everyAction) {
      const out = sceneReducer(frozen, a);
      expect(out, a.type).not.toBe(frozen);
    }
    expect(JSON.stringify(frozen)).toBe(before);
  });

  it("round-trips every shared fixture scene and every action through JSON unchanged", () => {
    for (const s of valid) expect(JSON.parse(JSON.stringify(s))).toEqual(s);
    // replaying actions read back from JSON gives the same scene: actions are plain data
    const withBeam = sceneReducer(base(), { type: "addOverlay", overlay: beam("b") });
    const direct = everyAction.reduce(sceneReducer, withBeam);
    const replayed = (JSON.parse(JSON.stringify(everyAction)) as SceneAction[]).reduce(sceneReducer, JSON.parse(JSON.stringify(withBeam)) as Scene);
    expect(replayed).toEqual(direct);
    expect(JSON.parse(JSON.stringify(direct))).toEqual(direct); // no undefined, NaN or functions inside
  });
});

describe("a pane", () => {
  it("starts a new dataset on channel 0, fitted, with no region, and keeps the colours and the line setting", () => {
    let p = initialPane(1, true);
    const steps: PaneAction[] = [
      { type: "setDisplay", display: { colormap: "gray", range: "custom", limits: [1, 2], scaleGroup: "compare" } },
      { type: "setChannel", channel: 4 },
      { type: "updateOverlay", id: "regions", patch: { active: { shape: "circle", cx: 1, cy: 1, r: 2 } } },
      { type: "updateOverlay", id: "orientation", patch: { visible: false } },
      { type: "setView", view: { zoom: 3 } },
    ];
    p = steps.reduce(paneReducer, p);
    const q = paneReducer(p, { type: "resetForDataset", datasetId: 2, axial: true });
    expect(q.scene!.base).toEqual({ datasetId: 2, channel: 0, display: { mapping: "axial", colormap: "gray", stretch: "linear", range: "custom", limits: [1, 2] } });
    expect(regionsOf(q.scene)!.active).toBeUndefined();
    expect(orientationOf(q.scene)!.visible).toBe(false);
    expect(q.scene!.view).toEqual({ zoom: 1 });
    // no dataset: no scene, but what carries over is kept for the next one
    const none = paneReducer(q, { type: "resetForDataset", datasetId: null, axial: false });
    expect(none.scene).toBeNull();
    expect(paneReducer(none, { type: "setChannel", channel: 3 })).toBe(none);
    expect(paneReducer(none, { type: "resetForDataset", datasetId: 3, axial: false }).scene!.base.display.colormap).toBe("gray");
  });

  it("follows the declaration: an axial map gets its own orientation lines under the regions, and loses them when withdrawn", () => {
    const p = initialPane(5, false);
    expect(p.scene).toEqual(newScene(5, DEFAULT_DISPLAY, false, true));
    const axial = paneReducer(p, { type: "syncAxial", axial: true });
    expect(axial.scene!.overlays.map((o) => o.kind)).toEqual(["orientation", "regions"]);
    expect(axial.scene!.base.display.mapping).toBe("axial");
    expect(paneReducer(axial, { type: "syncAxial", axial: true })).toBe(axial);
    const back = paneReducer(axial, { type: "syncAxial", axial: false });
    expect(back.scene!.overlays.map((o) => o.kind)).toEqual(["regions"]);
    expect(back.scene!.base.display.mapping).toBe("scalar");
  });
});

describe("link groups", () => {
  const a = { ...base(), base: { ...base().base, channel: 9 } };
  const b = base();
  it("same grid: cursor and channel are linked, the view is not; nothing is linked without a group", () => {
    const g = compareGroup(true);
    expect(links(g, "cursor")).toBe(true);
    expect(links(g, "view")).toBe(false);
    expect(follow(g, a, b, { from: 12, to: 12 })).toEqual([{ type: "setChannel", channel: 9 }]);
    expect(follow(g, a, b, { from: 12, to: 20 })).toEqual([]); // a different number of channels: not the same axis
    expect(follow(compareGroup(false), a, b, { from: 12, to: 12 })).toEqual([]);
    expect(links(null, "cursor")).toBe(false);
  });
  it("a group that links the view copies it", () => {
    const g = { ...compareGroup(true)!, facets: { cursor: true, channel: false, view: true } };
    const moved = sceneReducer(a, { type: "setView", view: { zoom: 3, centre: { x: 2, y: 2 } } });
    expect(follow(g, moved, b, { from: 1, to: 1 })).toEqual([{ type: "setView", view: { zoom: 3, centre: { x: 2, y: 2 } } }]);
  });
});
