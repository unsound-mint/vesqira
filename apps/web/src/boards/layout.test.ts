import { describe, expect, it } from "vitest";
import { arrange, compact, COLS, dragTarget, moveStep, readingOrder, resizeStep, resizeTarget, resolve, sameLayout, sentence, settle } from "./layout";
import type { Rect } from "./layout";

const R = (x: number, y: number, w: number, h: number): Rect => ({ x, y, w, h });
const noOverlap = (rs: Rect[]) => rs.every((a, i) => rs.every((b, j) => i === j || !(a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h)));
const inside = (rs: Rect[]) => rs.every((r) => r.x >= 0 && r.y >= 0 && r.x + r.w <= COLS && r.w >= 1 && r.h >= 1);

describe("settling a layout", () => {
  it("compacts upward and leaves a compact layout alone", () => {
    expect(compact([R(0, 5, 6, 3), R(6, 9, 6, 3)])).toEqual([R(0, 0, 6, 3), R(6, 0, 6, 3)]);
    const tight = [R(0, 0, 6, 4), R(6, 0, 6, 4), R(0, 4, 12, 2)];
    expect(compact(tight)).toEqual(tight);
  });

  it("pushes the panels under a placed one down, never overlapping, and is idempotent", () => {
    const rs = [R(0, 0, 6, 4), R(6, 0, 6, 4), R(0, 4, 6, 4)];
    const out = settle(rs, 1, R(0, 0, 6, 4)); // the second panel is dropped onto the first
    expect(noOverlap(out) && inside(out)).toBe(true);
    expect(out[1]).toEqual(R(0, 0, 6, 4)); // the moved panel keeps its place
    expect(out[0]!.y).toBeGreaterThanOrEqual(4); // the one it covered went below
    expect(settle(out, null)).toEqual(out);
  });

  it("closes the hole a panel leaves, and lets a panel dragged down past a neighbour take its place", () => {
    const rs = [R(0, 0, 12, 4), R(0, 4, 12, 4), R(0, 8, 12, 4)];
    const out = settle(rs, 0, R(0, 4, 12, 4)); // the top panel is dragged onto the second one
    expect(out.map((r) => r.y)).toEqual([4, 0, 8]);
    expect(noOverlap(out)).toBe(true);
    const gone = settle([R(0, 0, 12, 4), R(0, 4, 12, 4)], 1, R(0, 40, 12, 4)); // dropped far below: it cannot float
    expect(gone[1]).toEqual(R(0, 4, 12, 4));
  });

  it("keeps whatever is laid out inside the grid however it is asked", () => {
    const out = settle([R(0, 0, 6, 4), R(6, 0, 6, 4)], 0, R(40, -3, 30, 1));
    expect(inside(out) && noOverlap(out)).toBe(true);
    expect(out[0]!.w).toBeLessThanOrEqual(COLS);
  });

  it("gives the same result for any order of the same rectangles (a total reading order)", () => {
    const rs = [R(6, 0, 6, 3), R(0, 0, 6, 3), R(0, 3, 12, 2)];
    expect(readingOrder(rs)).toEqual([1, 0, 2]);
    expect(readingOrder([R(0, 0, 2, 2), R(0, 0, 2, 2)])).toEqual([0, 1]); // ties by index
  });
});

describe("deriving a layout for a board that has none", () => {
  it("stacks the panels full width in their stored order, a run shorter than a figure", () => {
    const out = resolve([null, null, null], ["figure", "job", "figure"]);
    expect(out).toEqual([R(0, 0, 12, 8), R(0, 8, 12, 5), R(0, 13, 12, 8)]);
    expect(resolve([null, null, null], ["figure", "job", "figure"])).toEqual(out); // deterministic
  });

  it("depends only on what is stored: the same input gives the same output, whatever else changed", () => {
    expect(resolve([null, null], ["figure", "figure"])).toEqual(resolve([null, null], ["figure", "figure"]));
  });

  it("returns a stored layout unchanged and puts a panel without a place below the others", () => {
    const stored = [R(0, 0, 6, 4), R(6, 0, 6, 4)];
    expect(resolve(stored, ["figure", "figure"])).toEqual(stored);
    const mixed = resolve([...stored, null], ["figure", "figure", "figure"]);
    expect(mixed.slice(0, 2)).toEqual(stored);
    expect(mixed[2]).toEqual(R(0, 4, 12, 8));
  });

  it("treats a stored place that is outside the grid or overlaps an earlier one as missing, and closes a hole left by a removal", () => {
    const out = resolve([R(0, 0, 6, 4), R(3, 0, 6, 4), R(11, 0, 4, 2)], ["figure", "figure", "figure"]);
    expect(noOverlap(out) && inside(out)).toBe(true);
    expect(out[0]).toEqual(R(0, 0, 6, 4));
    expect(resolve([R(0, 9, 12, 4)], ["figure"])).toEqual([R(0, 0, 12, 4)]);
  });
});

describe("keyboard moves and resizes", () => {
  const two = [R(0, 0, 6, 4), R(6, 0, 6, 4)];

  it("moves one column at a time and refuses to leave the grid", () => {
    const right = moveStep(two, 0, 1, 0);
    expect(right.changed).toBe(true);
    expect(right.rects[0]!.x).toBe(1);
    expect(noOverlap(right.rects)).toBe(true); // the panel it ran into went below
    expect(moveStep(two, 0, -1, 0)).toMatchObject({ changed: false, reason: "edge" });
    expect(moveStep(two, 1, 1, 0)).toMatchObject({ changed: false, reason: "edge" });
  });

  it("moves down past the neighbour in the way, because a compact layout closes a single empty row", () => {
    const col = [R(0, 0, 12, 4), R(0, 4, 12, 4)];
    const down = moveStep(col, 0, 0, 1);
    expect(down.changed).toBe(true);
    expect(down.rects.map((r) => r.y)).toEqual([4, 0]);
    expect(moveStep(down.rects, 0, 0, 1)).toMatchObject({ changed: false, reason: "edge" }); // already last
    expect(moveStep(col, 0, 0, -1)).toMatchObject({ changed: false }); // already first
  });

  it("moves up by one row-step when something is above, and every step ends with a valid compact layout", () => {
    const col = [R(0, 0, 12, 4), R(0, 4, 12, 4), R(0, 8, 12, 4)];
    const up = moveStep(col, 2, 0, -1);
    expect(up.changed).toBe(true);
    expect(noOverlap(up.rects) && inside(up.rects)).toBe(true);
    expect(compact(up.rects)).toEqual(up.rects);
  });

  it("resizes by whole columns and rows within the limits", () => {
    expect(resizeStep(two, 0, 1, 0).rects[0]).toMatchObject({ w: 7 });
    expect(noOverlap(resizeStep(two, 0, 1, 0).rects)).toBe(true);
    expect(resizeStep(two, 0, 0, 1).rects[0]).toMatchObject({ h: 5 });
    expect(resizeStep(two, 0, -1, -1).rects[0]).toMatchObject({ w: 5, h: 3 });
    expect(resizeStep([R(0, 0, 2, 2)], 0, -1, 0)).toMatchObject({ changed: false, reason: "limit" });
    expect(resizeStep([R(10, 0, 2, 2)], 0, 1, 0)).toMatchObject({ changed: false, reason: "limit" }); // would pass column 12
  });

  it("makes room by pushing the panels below when one grows, and lets the rest rise when one shrinks", () => {
    const col = [R(0, 0, 12, 4), R(0, 4, 12, 4)];
    expect(resizeStep(col, 0, 0, 2).rects.map((r) => r.y)).toEqual([0, 6]);
    expect(resizeStep(col, 0, 0, -2).rects.map((r) => r.y)).toEqual([0, 2]);
  });

  it("describes the result with 1-based places", () => {
    expect(sentence("Peak", R(2, 0, 6, 1), "moved")).toBe("“Peak” moved to column 3, row 1; 6 columns wide, 1 row tall.");
    expect(sentence("Peak", R(0, 3, 12, 8), "resized")).toContain("resized to column 1, row 4; 12 columns wide, 8 rows tall");
  });
});

describe("pointer geometry", () => {
  it("snaps a drag to whole cells and keeps the panel in the grid", () => {
    expect(dragTarget(R(2, 3, 4, 4), 130, 70, 60, 60)).toEqual(R(4, 4, 4, 4));
    expect(dragTarget(R(2, 3, 4, 4), -1000, -1000, 60, 60)).toEqual(R(0, 0, 4, 4));
    expect(dragTarget(R(2, 3, 4, 4), 1000, 0, 60, 60).x).toBe(COLS - 4);
  });
  it("snaps a corner resize to whole cells within the limits", () => {
    expect(resizeTarget(R(0, 0, 4, 4), 125, 61, 60, 60)).toEqual(R(0, 0, 6, 5));
    expect(resizeTarget(R(0, 0, 4, 4), -1000, -1000, 60, 60)).toMatchObject({ w: 2, h: 2 });
  });
});

describe("arrange automatically", () => {
  const fig = (channel: number | undefined, method: string | undefined) => ({ kind: "figure" as const, channel, method });

  it("puts channels in columns and methods in rows when every figure records both", () => {
    const out = arrange([fig(2, "vgt"), fig(0, "vgt"), fig(2, "rohsa"), fig(0, "rohsa")]);
    expect(out.by).toBe("channel-method");
    expect(out.rects[1]).toMatchObject({ x: 0, y: 0, w: 6 }); // channel 0, first method
    expect(out.rects[0]).toMatchObject({ x: 6, y: 0 }); // channel 2 is the second column
    expect(out.rects[2]).toMatchObject({ x: 6, y: 8 }); // second method is the second row
    expect(noOverlap(out.rects)).toBe(true);
    expect(compact(out.rects)).toEqual(out.rects);
    expect(out.sentence).toMatch(/2 channels as columns and 2 methods/);
  });

  it.each([
    ["a run without a result", [{ kind: "job" as const }, fig(0, "vgt")], /follows a run that has no picture yet/],
    ["a figure without a channel", [fig(undefined, "vgt"), fig(1, "vgt")], /does not record a channel/],
    ["a figure without a run", [fig(0, undefined), fig(1, "vgt")], /not drawn from a recorded run/],
    ["two figures in one cell", [fig(0, "vgt"), fig(0, "vgt")], /two figures show channel 1 of the same method/],
    ["a grid with a hole", [fig(0, "vgt"), fig(1, "vgt"), fig(1, "rohsa")], /not every method has a figure for every channel/],
    ["more channels than columns", [0, 1, 2, 3, 4, 5, 6].map((c) => fig(c, "vgt")), /7 different channels/],
  ])("falls back to reading order and says why: %s", (_n, items, why) => {
    const out = arrange(items);
    expect(out.by).toBe("reading-order");
    expect(out.sentence).toMatch(why);
    expect(out.sentence).toMatch(/placed in reading order instead/);
    expect(out.rects.every((r) => r.x === 0 && r.w === COLS)).toBe(true);
    expect(noOverlap(out.rects)).toBe(true);
  });

  it("does not take channel 0 for a missing channel", () => {
    expect(arrange([fig(0, "vgt"), fig(1, "vgt")]).by).toBe("channel-method");
  });
});

it("compares layouts by value", () => {
  expect(sameLayout([R(0, 0, 1, 1)], [R(0, 0, 1, 1)])).toBe(true);
  expect(sameLayout([R(0, 0, 1, 1)], [R(0, 1, 1, 1)])).toBe(false);
  expect(sameLayout([], [R(0, 0, 1, 1)])).toBe(false);
});
