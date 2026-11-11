import { describe, expect, it } from "vitest";
import fixture from "../../../../packages/contracts/fixtures/scenes.json";
import { initialPane, newScene, sceneReducer } from "../scene/scene";
import type { Scene, SceneAction } from "../scene/scene";
import { CHANNEL_HOLD_MS, EDIT_HOLD_MS, StepRecorder, classify, signature } from "./recorder";
import type { Sender } from "./recorder";

const name = (id: number) => `ds${id}`;
const fresh = (id = 1): Scene => newScene(id, initialPane(id, false).keep.display, false, true);
const step = (s: Scene, a: SceneAction) => sceneReducer(s, a);

// a clock the test moves
function clock() {
  let now = 0;
  const timers: { at: number; f: () => void; id: number }[] = [];
  let n = 0;
  return {
    set: (f: () => void, ms: number) => { const t = { at: now + ms, f, id: ++n }; timers.push(t); return t.id; },
    clear: (h: unknown) => { const i = timers.findIndex((t) => t.id === h); if (i >= 0) timers.splice(i, 1); },
    advance(ms: number) {
      now += ms;
      for (const t of timers.filter((x) => x.at <= now).sort((a, b) => a.at - b.at)) { timers.splice(timers.indexOf(t), 1); t.f(); }
    },
  };
}

function setup(headId: number | null = null) {
  const sent: Parameters<Sender>[0][] = [];
  let next = 100;
  const send: Sender = async (c) => { sent.push(c); return ++next; };
  const clk = clock();
  const rec = new StepRecorder(send, name, headId, clk);
  return { rec, sent, clk };
}

describe("classify", () => {
  it("names the meaningful changes and nothing else", () => {
    const a = fresh();
    expect(classify(null, a, name)).toMatchObject({ kind: "opened", label: "Opened ds1", hold: 0 });
    expect(classify(a, fresh(2), name)?.kind).toBe("opened");
    expect(classify(a, step(a, { type: "setChannel", channel: 11 }), name)).toMatchObject({ kind: "channel", label: "Channel 12", hold: CHANNEL_HOLD_MS });
    expect(classify(a, step(a, { type: "updateOverlay", id: "regions", patch: { active: { shape: "rect", x0: 1, y0: 1, x1: 10, y1: 4 } } }), name)).toMatchObject({ kind: "region", label: "Drew a rectangle, 10 × 4 pixels", hold: EDIT_HOLD_MS });
    expect(classify(a, step(a, { type: "addOverlay", overlay: { id: "c", kind: "beam", datasetId: 2, visible: true } }), name)).toMatchObject({ kind: "overlay", label: "Added beam overlay from ds2" });
    expect(classify(a, step(a, { type: "setLink", link: { id: "g", name: "pair", basis: "wcs", facets: { cursor: true, channel: true, view: false }, datasets: [1, 2] } }), name)).toMatchObject({ kind: "link", label: "Linked views: pair (by sky position)" });
    // not steps: zoom, colour, stretch, clearing a region, saved regions mirroring the server
    expect(classify(a, step(a, { type: "setView", view: { zoom: 4 } }), name)).toBeNull();
    expect(classify(a, step(a, { type: "setDisplay", display: { colormap: "gray" } }), name)).toBeNull();
    expect(classify(a, step(a, { type: "updateOverlay", id: "regions", patch: { saved: [{ id: 1, shape: { shape: "circle", cx: 1, cy: 1, r: 1 } }] as never } }), name)).toBeNull();
  });

  it("does not let the mirrors of other state change a scene's identity", () => {
    const a = fresh();
    const b = step(step(a, { type: "setSpectral", channel: 0, spectral: { value: 1, unit: "Hz" } }), { type: "updateOverlay", id: "regions", patch: { saved: [{ id: 1, shape: { shape: "circle", cx: 1, cy: 1, r: 1 } }] as never } });
    expect(signature(b)).toBe(signature(a));
    expect(signature(step(a, { type: "setChannel", channel: 3 }))).not.toBe(signature(a));
  });

  it("reads the contract fixtures", () => {
    for (const v of fixture.valid) expect(() => signature(v.scene as unknown as Scene)).not.toThrow();
  });
});

describe("StepRecorder", () => {
  it("records a dataset opening at once, with no parent when there is no history", async () => {
    const { rec, sent } = setup();
    rec.observe(fresh());
    await rec.idle();
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ kind: "opened", label: "Opened ds1", parentId: null });
  });

  it("records a channel only when it was held for 1.5 s, once, as the person left it", async () => {
    const { rec, sent, clk } = setup();
    let s = fresh();
    rec.observe(s);
    for (let c = 1; c <= 20; c++) { // scrubbing: a change every 100 ms
      s = step(s, { type: "setChannel", channel: c });
      rec.observe(s);
      clk.advance(100);
    }
    await rec.idle();
    expect(sent.map((x) => x.kind)).toEqual(["opened"]);
    clk.advance(CHANNEL_HOLD_MS);
    await rec.idle();
    expect(sent.map((x) => x.label)).toEqual(["Opened ds1", "Channel 21"]);
    expect(sent[1]!.scene.base.channel).toBe(20);
    expect(sent[1]!.parentId).toBe(101); // the opening is its parent
  });

  it("a different kind of step flushes the waiting one first, each with the scene it had", async () => {
    const { rec, sent } = setup();
    let s = fresh();
    rec.observe(s);
    s = step(s, { type: "setChannel", channel: 5 });
    rec.observe(s);
    s = step(s, { type: "updateOverlay", id: "regions", patch: { active: { shape: "circle", cx: 5, cy: 5, r: 3 } } });
    rec.observe(s);
    await rec.idle();
    expect(sent.map((x) => x.kind)).toEqual(["opened", "channel"]);
    expect(sent[1]!.scene.base.channel).toBe(5);
    expect(sent[1]!.scene.overlays.find((o) => o.id === "regions")).not.toHaveProperty("active");
    rec.flush();
    await rec.idle();
    expect(sent.map((x) => x.kind)).toEqual(["opened", "channel", "region"]);
  });

  it("does not repeat a state the head already is, and adjusting a region is one step", async () => {
    const { rec, sent, clk } = setup();
    let s = fresh();
    rec.observe(s);
    for (const x1 of [4, 6, 9]) {
      s = step(s, { type: "updateOverlay", id: "regions", patch: { active: { shape: "rect", x0: 0, y0: 0, x1, y1: 3 } } });
      rec.observe(s);
      clk.advance(100);
    }
    clk.advance(EDIT_HOLD_MS);
    await rec.idle();
    expect(sent.map((x) => x.label)).toEqual(["Opened ds1", "Drew a rectangle, 10 × 4 pixels"]);
    // going back to the same channel state the head has: nothing new
    s = step(s, { type: "setView", view: { zoom: 2 } });
    rec.observe(s);
    clk.advance(5000);
    await rec.idle();
    expect(sent).toHaveLength(2);
  });

  it("continue from here: the head moves to that step, a 'continued' step is its child and later steps branch from it", async () => {
    const { rec, sent, clk } = setup(7);
    let s = fresh();
    rec.observe(s);
    await rec.idle(); // opened, child of 7 (101)
    const target = step(fresh(), { type: "setChannel", channel: 9 });
    rec.continueFrom({ id: 3, label: "Channel 10" }, target, false);
    rec.observe(target); // the restore arrives in the pane: not a second step
    clk.advance(CHANNEL_HOLD_MS);
    await rec.idle();
    expect(sent.map((x) => [x.kind, x.parentId])).toEqual([["opened", 7], ["continued", 3]]);
    expect(sent[1]!.label).toBe("Continued from “Channel 10”");
    const next = step(target, { type: "updateOverlay", id: "regions", patch: { active: { shape: "circle", cx: 2, cy: 2, r: 2 } } });
    rec.observe(next);
    clk.advance(EDIT_HOLD_MS);
    await rec.idle();
    expect(sent[2]).toMatchObject({ kind: "region", parentId: 102 }); // the continued step, so 3 now has two children: a branch
  });

  it("continue from here into another dataset does not record the opening that takes", async () => {
    const { rec, sent, clk } = setup(7);
    rec.observe(fresh(1));
    await rec.idle();
    const target = step(fresh(2), { type: "setChannel", channel: 4 });
    rec.continueFrom({ id: 3, label: "x" }, target, true);
    rec.observe(fresh(2)); // the pane starts over on dataset 2
    rec.observe(target);
    clk.advance(CHANNEL_HOLD_MS);
    await rec.idle();
    expect(sent.map((x) => x.kind)).toEqual(["opened", "continued"]);
  });

  it("a send that fails records nothing and the next step still goes out", async () => {
    const sent: string[] = [];
    let fail = true;
    const clk = clock();
    const rec = new StepRecorder(async (c) => { if (fail) { fail = false; return null; } sent.push(c.label); return 5; }, name, null, clk);
    rec.observe(fresh());
    await rec.idle();
    const s = step(fresh(), { type: "updateOverlay", id: "regions", patch: { active: { shape: "circle", cx: 1, cy: 1, r: 1 } } });
    rec.observe(s);
    clk.advance(EDIT_HOLD_MS);
    await rec.idle();
    expect(sent).toEqual(["Drew a circle, radius 1 pixels"]);
  });

  it("a link change is always a step, though the link is not part of a scene's identity", async () => {
    const { rec, sent, clk } = setup();
    const s = fresh();
    rec.observe(s);
    const linked = step(s, { type: "setLink", link: { id: "g", name: "pair", basis: "wcs", facets: { cursor: true, channel: true, view: false }, datasets: [1, 2] } });
    rec.observe(linked);
    clk.advance(EDIT_HOLD_MS);
    rec.observe(step(linked, { type: "setLink", link: null }));
    clk.advance(EDIT_HOLD_MS);
    await rec.idle();
    expect(sent.map((x) => x.label)).toEqual(["Opened ds1", "Linked views: pair (by sky position)", "Left the link group"]);
  });
});
