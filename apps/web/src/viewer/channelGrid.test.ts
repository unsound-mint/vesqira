import { describe, expect, it } from "vitest";
import type { ChannelGridPlan } from "../api/channelmaps";
import { lut } from "./colormaps";
import { defaultColumns, gridCaption, gridFigure, panelLabel, paintPixels, rampCss } from "./channelGrid";

const NODATA: [[number, number, number], [number, number, number]] = [[10, 10, 10], [200, 200, 200]];

const plan = (over: Partial<ChannelGridPlan> = {}): ChannelGridPlan => ({
  channels: 64, width: 32, height: 24, panel_width: 32, panel_height: 24, step: 1, panel_count: 3, pixels_per_panel: 768, pixels_total: 2304,
  caps: { panels: 64, pixels_per_panel: 1048576, pixels_total: 16777216 },
  panels: [
    { index: 0, first: 0, last: 0, channels: 1, mean: false, spectral: { first: -32.5, last: -32.5 }, flagged: null },
    { index: 1, first: 10, last: 12, channels: 3, mean: true, spectral: { first: -22, last: -20 }, flagged: 1 },
    { index: 2, first: 20, last: 20, channels: 1, mean: false, spectral: null, flagged: null },
  ],
  preview: null, averaging: { channels: 3, order: "per-pixel mean of the finite values of the panel's channels at full resolution, then the block mean" },
  scale: { lo: 0.5, hi: 8.25, source: "1st to 99th percentile of the finite pixels pooled over panels 1, 3 of 3 (evenly spaced, at the preview resolution); one scale for every panel", sampled_panels: [0, 2] },
  spectral: { axis: "radio velocity", ctype: "VRAD", unit: "km/s", unit_status: "converted", native_unit: "m/s", convention: "radio", convention_source: "file", specsys: "LSRK", rest: null, basis: "unit change only (astropy.units); the velocity is the file's radio velocity (VRAD)", refused: null, reference_frame_note: "x" },
  nan_meaning: "no value", ...over,
}) as ChannelGridPlan;

describe("panel labels", () => {
  it("name the channel and the spectral coordinate in the unit the plan states, counting channels from 1", () => {
    const p = plan();
    expect(panelLabel(p.panels[0]!, p)).toBe("Channel 1 · -32.5 km/s");
    expect(panelLabel(p.panels[1]!, p)).toBe("Mean of channels 11–13 · -22 to -20 km/s");
  });

  it("do not invent a coordinate: no spectral value or no unit leaves the channel alone", () => {
    const p = plan();
    expect(panelLabel(p.panels[2]!, p)).toBe("Channel 21");
    const noUnit = plan({ spectral: { ...p.spectral, unit: null, refused: "the file has no spectral axis" } });
    expect(panelLabel(noUnit.panels[0]!, noUnit)).toBe("Channel 1");
  });

  it("show the file's unit as given, never silently rescaled to km/s or MHz", () => {
    const p = plan({ spectral: { ...plan().spectral, unit: "m/s", unit_status: "declared" } });
    expect(panelLabel(p.panels[0]!, p)).toBe("Channel 1 · -32.5 m/s");
  });
});

describe("the shared scale", () => {
  const scale = { lo: 0, hi: 10 };
  const table = lut("viridis");

  it("gives the same value the same colour in every panel, whatever else the panel holds", () => {
    const a = new Float32Array([5, 0, 10, 2]);
    const b = new Float32Array([5, 9, 1, 5]);
    const pa = paintPixels(a, 2, 2, scale, table, "linear", NODATA);
    const pb = paintPixels(b, 2, 2, scale, table, "linear", NODATA);
    // data row 0 is the bottom row of the picture: the value 5 is at picture (0, 1) in panel a, and at (0, 1) and (1, 0) in panel b
    const at = (px: Uint8ClampedArray, x: number, y: number) => Array.from(px.slice((y * 2 + x) * 4, (y * 2 + x) * 4 + 3));
    expect(at(pa, 0, 1)).toEqual(at(pb, 0, 1));
    expect(at(pb, 0, 1)).toEqual(at(pb, 1, 0));
    expect(at(pa, 0, 1)).not.toEqual(at(pa, 1, 1)); // 5 and 0 differ
  });

  it("clips beyond the limits to the ends of the scale instead of rescaling the panel", () => {
    const px = paintPixels(new Float32Array([-5, 50]), 2, 1, scale, table, "linear", NODATA);
    expect(Array.from(px.slice(0, 3))).toEqual([table[0], table[1], table[2]]);
    expect(Array.from(px.slice(4, 7))).toEqual([table[765], table[766], table[767]]);
  });

  it("hatches a pixel with no value, which is not the bottom of the scale", () => {
    const px = paintPixels(new Float32Array([NaN, 0, Infinity, 0]), 4, 1, scale, table, "linear", NODATA);
    const rgb = (i: number) => Array.from(px.slice(i * 4, i * 4 + 3));
    expect(rgb(0)).toEqual(NODATA[0]);
    expect(rgb(2)).toEqual(NODATA[0]);
    expect(rgb(1)).toEqual([table[0], table[1], table[2]]);
    expect(rgb(0)).not.toEqual(rgb(1));
  });

  it("applies one stretch to all panels", () => {
    const lin = paintPixels(new Float32Array([2.5]), 1, 1, scale, table, "linear", NODATA);
    const sq = paintPixels(new Float32Array([2.5]), 1, 1, scale, table, "sqrt", NODATA);
    expect(Array.from(lin.slice(0, 3))).not.toEqual(Array.from(sq.slice(0, 3)));
  });

  it("draws the legend from the same colour table", () => {
    expect(rampCss("viridis")).toMatch(/^linear-gradient\(to right, rgb\(\d+,\d+,\d+\) 0\.0%/);
  });
});

describe("what the grid states", () => {
  it("says the scale is shared, with its limits, stretch, source and what is not drawn", () => {
    const lines = gridCaption(plan(), { stretch: "square root", bunit: "K", overlaysOnBase: 2 });
    const all = lines.join("\n");
    expect(all).toContain("All 3 panels share one colour scale and the square root stretch: 0.5 to 8.25 K");
    expect(all).toContain("one scale for every panel");
    expect(all).toContain("mean of 3 channels");
    expect(all).toContain("Non-finite values are excluded");
    expect(all).toContain("radio convention, the file's");
    expect(all).toContain("Reference frame LSRK, unchanged");
    expect(all).toContain("The map's 2 overlays are not drawn on the panels");
    expect(all).toContain("never drawn as zero");
  });

  it("says a preview is a preview, and that a refused conversion kept the native labels", () => {
    const p = plan({ preview: { step: 4, method: "each panel is the mean of the finite pixels in 4 x 4 blocks (NaN where a block has none); a display preview, not for measurement" }, spectral: { ...plan().spectral, unit: "Hz", unit_status: "declared", refused: "velocity needs a rest frequency" } });
    const all = gridCaption(p, { stretch: "linear", bunit: null, overlaysOnBase: 0 }).join("\n");
    expect(all).toContain("a display preview, not for measurement");
    expect(all).toContain("Spectral labels: velocity needs a rest frequency");
    expect(all).not.toContain("overlay");
  });

  it("has nothing to claim about a scale when no sampled pixel is finite", () => {
    const p = plan({ scale: { lo: null, hi: null, source: "x", sampled_panels: [0] } });
    expect(gridCaption(p, { stretch: "linear", bunit: "K", overlaysOnBase: 0 })[0]).toContain("no shared scale");
    expect(gridFigure({ datasetId: 1, plan: p, first: 0, last: 20, stride: 10, average: 1, columns: 2, unit: "", convention: "radio", colormap: "viridis", stretch: "linear", bunit: "K", overlaysNotDrawn: 0 })).toBeNull();
  });
});

describe("the stored description", () => {
  it("is built from the plan: numeric shared limits, labels' unit and convention, preview, and the matching reviewer-safe view", () => {
    const p = plan({ step: 2, panel_width: 16, panel_height: 12 });
    const g = gridFigure({ datasetId: 7, plan: p, first: 0, last: 20, stride: 10, average: 3, columns: 2, unit: "km/s", convention: "radio", colormap: "inferno", stretch: "asinh", bunit: "K", overlaysNotDrawn: 2 })!;
    expect(g.spec).toMatchObject({
      schema: "channel-grid/1", datasetId: 7, first: 0, last: 20, stride: 10, average: 3, columns: 2, panels: 3,
      spectral: { unit: "km/s", convention: "radio" },
      display: { colormap: "inferno", stretch: "asinh", limits: [0.5, 8.25], limitsUnit: "K" },
      preview: { step: 2, panelWidth: 16, panelHeight: 12 },
      render: { figureWidth: 720, overlaysNotDrawn: 2 },
    });
    expect(g.view).toMatchObject({ kind: "channelgrid", channel: 0, range: "custom", limits: [0.5, 8.25], limitsUnit: "K", colormap: "inferno", stretch: "asinh" });
    expect(g.view.grid).toMatchObject({ first: 0, last: 20, stride: 10, average: 3, panels: 3, previewStep: 2, unit: "km/s", convention: "radio" });
    expect(g.view.grid?.scaleSource).toBe(g.spec.display.source);
  });

  it("has no spectral part when the labels are in the file's own unit", () => {
    const p = plan({ spectral: { ...plan().spectral, unit_status: "declared", convention: null, basis: null } });
    const g = gridFigure({ datasetId: 1, plan: p, first: 0, last: 20, stride: 10, average: 1, columns: 3, unit: "", convention: "radio", colormap: "viridis", stretch: "linear", bunit: null, overlaysNotDrawn: 0 })!;
    expect(g.spec.spectral).toBeUndefined();
    expect(g.view.grid?.unit).toBeUndefined();
    expect(g.spec.display.limitsUnit).toBeUndefined();
  });
});

describe("columns", () => {
  it("make a near-square grid of at most 8 columns", () => {
    expect([1, 2, 4, 9, 12, 64, 200].map(defaultColumns)).toEqual([1, 2, 2, 3, 4, 8, 8]);
  });
});
