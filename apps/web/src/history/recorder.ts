import { REGIONS_ID } from "../scene/scene";
import type { Scene } from "../scene/scene";
import type { StepKind } from "../api/history";

// Which scene changes are exploration steps, and when to write one (ADR 0057). A step is a meaningful state, not every change:
// a dataset opened, a channel the person stayed on, a region drawn, an overlay added, a link changed. Zooming, colour and
// stretch settings are not steps (they stay in the scene a step records, and in the undo stack).

/** A channel must be held this long before it is a step: scrubbing through a cube is one look, not fifty. */
export const CHANNEL_HOLD_MS = 1500;
/** Discrete edits wait a moment so a region being adjusted is recorded once, as it ended. */
export const EDIT_HOLD_MS = 400;

export type Candidate = { kind: StepKind; label: string; hold: number };

/** What identifies a scene as a step: the scene without what only mirrors other state (saved regions, link group, the worker's spectral value). */
export function signature(s: Scene): string {
  const { link: _l, ...rest } = s;
  const overlays = rest.overlays.map((o) => (o.kind === "regions" ? { ...o, saved: undefined } : o));
  const { spectral: _sp, ...base } = rest.base;
  return JSON.stringify({ ...rest, base, overlays });
}

const regionOf = (s: Scene) => s.overlays.find((o) => o.id === REGIONS_ID && o.kind === "regions");
const activeOf = (s: Scene) => {
  const r = regionOf(s);
  return r && r.kind === "regions" ? (r.active ?? null) : null;
};

function describeRegion(a: NonNullable<ReturnType<typeof activeOf>>): string {
  switch (a.shape) {
    case "rect": return `Drew a rectangle, ${a.x1 - a.x0 + 1} × ${a.y1 - a.y0 + 1} pixels`;
    case "circle": return `Drew a circle, radius ${a.r} pixels`;
    default: return "Drew a line";
  }
}

/**
 * The step a change from prev to next is, or null. `name` gives a dataset's name. Several changes at once (a restored scene)
 * count as the first of: opened, link, overlay, region, channel.
 */
export function classify(prev: Scene | null, next: Scene, name: (datasetId: number) => string): Candidate | null {
  if (!prev || prev.base.datasetId !== next.base.datasetId) return { kind: "opened", label: `Opened ${name(next.base.datasetId)}`, hold: 0 };
  const pl = prev.link ?? null;
  const nl = next.link ?? null;
  if (JSON.stringify(pl) !== JSON.stringify(nl)) {
    return { kind: "link", label: nl ? `Linked views: ${nl.name || "group"} (${nl.basis === "wcs" ? "by sky position" : "same grid"})` : "Left the link group", hold: EDIT_HOLD_MS };
  }
  const had = new Set(prev.overlays.map((o) => o.id));
  const added = next.overlays.filter((o) => !had.has(o.id));
  if (added.length > 0) {
    const o = added[0]!;
    const from = "datasetId" in o && o.datasetId !== next.base.datasetId ? ` from ${name(o.datasetId)}` : "";
    return { kind: "overlay", label: `Added ${o.kind} overlay${from}`, hold: EDIT_HOLD_MS };
  }
  const a = activeOf(next);
  if (a && JSON.stringify(a) !== JSON.stringify(activeOf(prev))) return { kind: "region", label: describeRegion(a), hold: EDIT_HOLD_MS };
  if (prev.base.channel !== next.base.channel) return { kind: "channel", label: `Channel ${next.base.channel + 1}`, hold: CHANNEL_HOLD_MS };
  return null;
}

export type Sender = (c: { kind: StepKind; label: string; scene: Scene; parentId: number | null }) => Promise<number | null>;

type Timers = { set: (f: () => void, ms: number) => unknown; clear: (h: unknown) => void };
const realTimers: Timers = { set: (f, ms) => setTimeout(f, ms), clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };

/**
 * Turns the scene of one pane into steps. observe() is called with every scene; the recorder debounces by kind (a later change of
 * the same kind replaces a pending one, a different kind flushes it first), writes the scene of the last change when the wait ends, skips a
 * state equal to the head step's, and chains the writes so each step's parent is the head when it is sent.
 */
export class StepRecorder {
  private prev: Scene | null = null;
  private pending: { c: Candidate; scene: Scene; timer: unknown } | null = null;
  private head: { id: number | null; sig: string } | null = null;
  private quietFor: number | null = null;
  private queue: Promise<unknown> = Promise.resolve();

  constructor(private readonly send: Sender, private readonly name: (id: number) => string, headId: number | null = null, private readonly timers: Timers = realTimers) {
    this.head = headId === null ? null : { id: headId, sig: "" };
  }

  /** The newest step of the project is where the next step branches from. Its signature is unknown until the list is loaded. */
  setHead(id: number | null, scene?: Scene) {
    this.head = id === null ? null : { id, sig: scene ? signature(scene) : "" };
  }

  observe(scene: Scene | null) {
    if (!scene) {
      this.cancel();
      this.prev = null;
      return;
    }
    const prev = this.prev;
    this.prev = scene;
    if (prev === scene) return;
    let c = classify(prev, scene, this.name);
    if (c?.kind === "opened" && this.quietFor === scene.base.datasetId) {
      this.quietFor = null; // the dataset a "continue from here" had to open first
      c = null;
    }
    if (!c) return;
    if (this.pending && this.pending.c.kind !== c.kind) this.flush();
    if (this.pending) this.timers.clear(this.pending.timer);
    if (c.hold === 0) {
      this.pending = null;
      this.write(c, scene);
      return;
    }
    this.pending = { c, scene, timer: this.timers.set(() => this.flush(), c.hold) };
  }

  /** Writes a waiting step now (the pane is going away, or a different kind of step arrived). */
  flush() {
    const p = this.pending;
    if (!p) return;
    this.timers.clear(p.timer);
    this.pending = null;
    this.write(p.c, p.scene);
  }

  cancel() {
    if (this.pending) this.timers.clear(this.pending.timer);
    this.pending = null;
  }

  /**
   * "Continue from here": the restored scene becomes the head (so the observed restore is not a second step) and a "continued"
   * step is written as a child of the step it continues; steps after it branch from it. If the scene opens another dataset first,
   * that opening is not recorded either.
   */
  continueFrom(step: { id: number; label: string }, scene: Scene, willOpen: boolean) {
    this.cancel();
    this.quietFor = willOpen ? scene.base.datasetId : null;
    this.head = { id: step.id, sig: signature(scene) };
    this.queue = this.queue.then(async () => {
      const id = await this.send({ kind: "continued", label: `Continued from “${step.label}”`, scene, parentId: step.id });
      if (id !== null && this.head && this.head.id === step.id) this.head = { id, sig: signature(scene) };
    });
  }

  private write(c: Candidate, scene: Scene) {
    this.queue = this.queue.then(async () => {
      const sig = signature(scene);
      if (c.kind !== "link" && this.head && this.head.sig === sig) return; // the head already is this state (a link change is not in the signature: it is always a step)
      const id = await this.send({ kind: c.kind, label: c.label, scene, parentId: this.head?.id ?? null });
      if (id !== null) this.head = { id, sig };
    });
  }

  /** Resolves when every write started so far has finished (tests, and the unmount flush). */
  idle() {
    return this.queue;
  }
}
