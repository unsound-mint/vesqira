import { describe, expect, it } from "vitest";
import fixture from "../../../../packages/contracts/fixtures/scenes.json";
import { initialPane, newScene } from "../scene/scene";
import type { Scene, SceneAction } from "../scene/scene";
import { COALESCE_MS, MAX_UNDO, commandName, initialViewer, redoLabel, restore, undoLabel, viewerReducer } from "./sceneCommands";
import type { ViewerAction, ViewerState } from "./sceneCommands";

const fixtureScene = () => structuredClone(fixture.valid[0]!.scene) as unknown as Scene;
const start = (): ViewerState => ({ ...initialViewer(initialPane(null, false)), pane: { ...initialPane(null, false), scene: fixtureScene() } });
const scene = (v: ViewerState) => v.pane.scene!;

// a small seeded generator, so a failure names its seed
function rng(seed: number) {
  let x = seed >>> 0 || 1;
  return () => {
    x ^= x << 13; x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5; x >>>= 0;
    return x / 2 ** 32;
  };
}

function randomAction(r: () => number, s: Scene): SceneAction {
  const pick = <T,>(xs: T[]) => xs[Math.floor(r() * xs.length)]!;
  const ids = s.overlays.map((o) => o.id);
  switch (Math.floor(r() * 10)) {
    case 0: return { type: "setChannel", channel: Math.floor(r() * 40) };
    case 1: return { type: "setDisplay", display: { colormap: pick(["viridis", "inferno", "twilight", "gray"]) } };
    case 2: return { type: "setDisplay", display: { stretch: pick(["linear", "sqrt", "asinh", "linear"]) } };
    case 3: return { type: "setDisplay", display: { limits: r() < 0.3 ? null : [r(), 1 + r()] , range: "custom" } };
    case 4: return { type: "addOverlay", overlay: { id: `o${Math.floor(r() * 6)}`, kind: "beam", datasetId: 1, visible: true }, ...(r() < 0.5 ? { index: 0 } : {}) };
    case 5: return { type: "updateOverlay", id: pick(ids), patch: { visible: r() < 0.5 } };
    case 6: return { type: "removeOverlay", id: pick(ids) };
    case 7: return { type: "moveOverlay", id: pick(ids), to: Math.floor(r() * 5) };
    case 8: return { type: "setView", view: { zoom: 1 + Math.floor(r() * 8), centre: { x: Math.floor(r() * 50), y: Math.floor(r() * 50) } } };
    default: return { type: "updateOverlay", id: "regions", patch: { active: r() < 0.3 ? null : { shape: "rect", x0: 1, y0: 1, x1: 2 + Math.floor(r() * 9), y1: 4 } } };
  }
}

describe("scene commands", () => {
  it("undo restores the exact previous state and redo the exact next one, over random command sequences", () => {
    for (let seed = 1; seed <= 60; seed++) {
      const r = rng(seed);
      let v = start();
      const states: Scene[] = [scene(v)];
      let t = 1_000;
      for (let i = 0; i < 40; i++) {
        t += COALESCE_MS * 2; // far apart: every changing command is its own
        const a = randomAction(r, scene(v));
        const before = v;
        v = viewerReducer(v, { ...a, at: t } as ViewerAction);
        if (v.undo.past.length > before.undo.past.length) states.push(scene(v));
        else expect(scene(v), `seed ${seed}: an unrecorded or no-op action changed a recorded state`).toEqual(scene(before)); // nothing here is an unrecorded fact
      }
      const finalState = scene(v);
      for (let i = states.length - 1; i > 0; i--) {
        v = viewerReducer(v, { type: "undo" });
        expect(scene(v), `seed ${seed}, undo to state ${i - 1}`).toEqual(states[i - 1]);
      }
      expect(v.undo.past).toHaveLength(0);
      expect(viewerReducer(v, { type: "undo" })).toBe(v);
      for (let i = 1; i < states.length; i++) {
        v = viewerReducer(v, { type: "redo" });
        expect(scene(v), `seed ${seed}, redo to state ${i}`).toEqual(states[i]);
      }
      expect(scene(v)).toEqual(finalState);
      expect(viewerReducer(v, { type: "redo" })).toBe(v);
    }
  });

  it("names every command, and the table has no gap: each action type is either named or deliberately not a command", () => {
    const s = fixtureScene();
    const named: Record<string, SceneAction> = {
      "change channel": { type: "setChannel", channel: 9 },
      "change colour map": { type: "setDisplay", display: { colormap: "gray" } },
      "change stretch": { type: "setDisplay", display: { stretch: "asinh" } },
      "change limits": { type: "setDisplay", display: { limits: [0, 1] } },
      "change display": { type: "setDisplay", display: { colormap: "gray", stretch: "asinh" } },
      "add overlay": { type: "addOverlay", overlay: { id: "n", kind: "beam", datasetId: 1, visible: true } },
      "draw region": { type: "updateOverlay", id: "regions", patch: { active: { shape: "rect", x0: 0, y0: 0, x1: 1, y1: 1 } } },
      "clear region": { type: "updateOverlay", id: "regions", patch: { active: null } },
      "hide overlay": { type: "updateOverlay", id: "regions", patch: { visible: false } },
      "show overlay": { type: "updateOverlay", id: "regions", patch: { visible: true } },
      "remove overlay": { type: "removeOverlay", id: "regions" },
      "reorder overlays": { type: "moveOverlay", id: "regions", to: 0 },
      zoom: { type: "setView", view: { zoom: 3 } },
      pan: { type: "setView", view: { centre: { x: 1, y: 1 } } },
    };
    for (const [name, a] of Object.entries(named)) expect(commandName(a, s)?.name, name).toBe(name);
    // facts and derived changes are not commands
    expect(commandName({ type: "setSpectral", channel: 0, spectral: null }, s)).toBeNull();
    expect(commandName({ type: "setLink", link: null }, s)).toBeNull();
    expect(commandName({ type: "setDisplay", display: { scaleGroup: "compare" } }, s)).toBeNull();
    expect(commandName({ type: "updateOverlay", id: "regions", patch: { saved: [] } }, s)).toBeNull();
  });

  it("names the command in the labels, and a new command ends the redo branch", () => {
    let v = start();
    v = viewerReducer(v, { type: "setChannel", channel: 20, at: 1000 });
    v = viewerReducer(v, { type: "moveOverlay", id: "regions", to: 0, at: 5000 }); // a no-op for a single overlay
    expect(undoLabel(v.undo)).toBe("change channel");
    v = viewerReducer(v, { type: "undo" });
    expect(undoLabel(v.undo)).toBeNull();
    expect(redoLabel(v.undo)).toBe("change channel");
    v = viewerReducer(v, { type: "setView", view: { zoom: 2 }, at: 9000 });
    expect(redoLabel(v.undo)).toBeNull();
    expect(undoLabel(v.undo)).toBe("zoom");
  });

  it("joins a drag or a scrub into one command, and drops one that ends where it began", () => {
    let v = start();
    const ch0 = scene(v).base.channel;
    for (let i = 1; i <= 10; i++) v = viewerReducer(v, { type: "setChannel", channel: ch0 + i, at: 1000 + i * 100 });
    expect(v.undo.past).toHaveLength(1);
    v = viewerReducer(v, { type: "setChannel", channel: ch0 + 20, at: 1000 + 10 * 100 + COALESCE_MS + 1 });
    expect(v.undo.past).toHaveLength(2);
    v = viewerReducer(v, { type: "undo" });
    expect(scene(v).base.channel).toBe(ch0 + 10);
    // out and back within the window is no edit
    let w = start();
    const z0 = scene(w).view.zoom;
    w = viewerReducer(w, { type: "setView", view: { zoom: z0 + 1 }, at: 100 });
    w = viewerReducer(w, { type: "setView", view: { zoom: z0 }, at: 200 });
    expect(w.undo.past).toHaveLength(0);
  });

  it("is bounded", () => {
    let v = start();
    for (let i = 1; i <= MAX_UNDO + 20; i++) v = viewerReducer(v, { type: "setChannel", channel: 100 + i, at: i * COALESCE_MS * 2 });
    expect(v.undo.past).toHaveLength(MAX_UNDO);
  });

  it("keeps what is not the person's to undo: the saved regions and the link group", () => {
    let v = start();
    v = viewerReducer(v, { type: "setChannel", channel: 22, at: 1000 });
    const saved = [{ id: 77, shape: { shape: "circle", cx: 1, cy: 1, r: 1 } }] as never;
    v = viewerReducer(v, { type: "fact", action: { type: "updateOverlay", id: "regions", patch: { saved } } });
    v = viewerReducer(v, { type: "fact", action: { type: "setLink", link: { id: "g", name: "pair", basis: "wcs", facets: { cursor: true, channel: false, view: true }, datasets: [1, 2] } } });
    v = viewerReducer(v, { type: "undo" });
    expect(scene(v).base.channel).toBe(fixtureScene().base.channel);
    expect(scene(v).overlays.find((o) => o.id === "regions")).toMatchObject({ saved });
    expect(scene(v).link?.name).toBe("pair");
  });

  it("does not record facts, and a follower pane's undo stays its own", () => {
    let v = start();
    v = viewerReducer(v, { type: "fact", action: { type: "setChannel", channel: 31 } });
    expect(scene(v).base.channel).toBe(31);
    expect(v.undo.past).toHaveLength(0);
  });

  it("applyScene is one undoable command; a new dataset forgets the history", () => {
    let v = start();
    const target = newScene(scene(v).base.datasetId, scene(v).base.display, false, true);
    const first = scene(v);
    v = viewerReducer(v, { type: "applyScene", scene: target, name: "continue from history", at: 5 });
    expect(undoLabel(v.undo)).toBe("continue from history");
    expect(scene(v).base.channel).toBe(0);
    v = viewerReducer(v, { type: "undo" });
    expect(scene(v)).toEqual(restore(first, first));
    v = viewerReducer(v, { type: "redo" });
    expect(scene(v).base.channel).toBe(0);
    v = viewerReducer(v, { type: "resetForDataset", datasetId: 9, axial: false });
    expect(v.undo.past).toHaveLength(0);
    expect(v.undo.future).toHaveLength(0);
    expect(scene(v).base.datasetId).toBe(9);
  });

  describe("facts that arrive between commands are not undone with them", () => {
    const region = { shape: "rect", x0: 1, y0: 1, x1: 5, y1: 4 } as const;
    const draw = (v: ViewerState, at: number) => viewerReducer(v, { type: "updateOverlay", id: "regions", patch: { active: region }, at });
    const plain = (): ViewerState => ({ ...initialViewer(initialPane(null, false)), pane: { ...initialPane(null, false), scene: newScene(1, initialPane(1, false).keep.display, false, true) } });

    it("a declaration made after the command stays: undoing 'draw region' does not make an axial map scalar again", () => {
      let v = draw(plain(), 1000);
      v = viewerReducer(v, { type: "syncAxial", axial: true });
      expect(scene(v).base.display.mapping).toBe("axial");
      v = viewerReducer(v, { type: "undo" });
      expect(scene(v).base.display.mapping).toBe("axial");
      expect(scene(v).overlays.map((o) => o.id)).toEqual(["orientation", "regions"]);
      expect(scene(v).overlays.find((o) => o.id === "regions")).not.toHaveProperty("active");
      v = viewerReducer(v, { type: "redo" });
      expect(scene(v).overlays.find((o) => o.id === "regions")).toHaveProperty("active");
      expect(scene(v).overlays[0]!.id).toBe("orientation");
    });

    it("a linked pane that followed its partner keeps the channel it was moved to", () => {
      let v = draw(plain(), 1000);
      v = viewerReducer(v, { type: "fact", action: { type: "setChannel", channel: 17 } }); // B was scrubbed; this pane followed
      v = viewerReducer(v, { type: "undo" });
      expect(scene(v).base.channel).toBe(17);
      expect(scene(v).overlays.find((o) => o.id === "regions")).not.toHaveProperty("active");
    });

    it("the shared scale group and the worker's value for the unchanged channel stay", () => {
      let v = viewerReducer(plain(), { type: "setDisplay", display: { colormap: "gray" }, at: 1000 });
      v = viewerReducer(v, { type: "fact", action: { type: "setDisplay", display: { scaleGroup: "compare" } } });
      v = viewerReducer(v, { type: "fact", action: { type: "setSpectral", channel: 0, spectral: { value: 1.42e9, unit: "Hz" } } });
      v = viewerReducer(v, { type: "undo" });
      expect(scene(v).base.display).toMatchObject({ colormap: "viridis", scaleGroup: "compare" });
      expect(scene(v).base.spectral).toEqual({ value: 1.42e9, unit: "Hz" });
    });

    it("a command that changed the channel puts the old channel and its world value back", () => {
      let v = viewerReducer(plain(), { type: "fact", action: { type: "setSpectral", channel: 0, spectral: { value: 5, unit: "m/s" } } });
      v = viewerReducer(v, { type: "setChannel", channel: 9, at: 1000 });
      v = viewerReducer(v, { type: "fact", action: { type: "setSpectral", channel: 9, spectral: { value: 9, unit: "m/s" } } });
      v = viewerReducer(v, { type: "undo" });
      expect(scene(v).base).toMatchObject({ channel: 0, spectral: { value: 5, unit: "m/s" } });
      v = viewerReducer(v, { type: "redo" });
      expect(scene(v).base.channel).toBe(9);
    });

    it("a removed overlay comes back at its place and the mapping is never restored", () => {
      let v = viewerReducer(plain(), { type: "addOverlay", overlay: { id: "b", kind: "beam", datasetId: 1, visible: true }, at: 1000 });
      v = viewerReducer(v, { type: "removeOverlay", id: "b", at: 5000 });
      v = viewerReducer(v, { type: "undo" });
      expect(scene(v).overlays.map((o) => o.id)).toEqual(["regions", "b"]);
      v = viewerReducer(v, { type: "setDisplay", display: { stretch: "sqrt" }, at: 9000 });
      v = viewerReducer(v, { type: "syncAxial", axial: true });
      v = viewerReducer(v, { type: "undo" });
      expect(scene(v).base.display).toMatchObject({ mapping: "axial", stretch: "linear" });
    });
  });
});
