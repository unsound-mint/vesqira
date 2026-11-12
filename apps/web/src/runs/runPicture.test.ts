import { describe, expect, it } from "vitest";
import { buildPlot } from "../catalog/runPlot";
import type { Conversion } from "../catalog/runPlot";
import { metric, run } from "../catalog/runFixtures";
import { plotSvg, viewBoxOf } from "./runPicture";

const D = "Axial agreement";
const f = Math.PI / 180;
const runs = [
  run(1, { b: "8" }, { metrics: [metric("am", 0.5, { unit: "rad", definition: D })] }),
  run(2, { b: "16" }, { metrics: [metric("am", 30, { unit: "deg", definition: D })] }),
];
const conv: Conversion = { y: "am", to: "rad", method: "m", entries: { 2: { definition: D, unit: "deg", value: 30, uncertainty: null, factor: f, out: { value: 30 * f, uncertainty: null } } } };

describe("the picture a cross-run figure saves", () => {
  const model = buildPlot(runs, "am", { kind: "param", key: "b" }, "", conv);

  it("is a self-contained SVG with no selection, no keyboard focus and no page-dependent attribute", async () => {
    const svg = await plotSvg(model, "all runs · 2 runs");
    expect(svg.startsWith("<svg")).toBe(true);
    expect(svg).toContain('xmlns="http://www.w3.org/2000/svg"');
    expect(svg).not.toMatch(/tabindex|data-testid|data-selected|data-run=/);
    expect(svg).not.toContain("var(--"); // tokens are resolved or fall back to currentColor
    expect(svg).toContain("all runs · 2 runs");
    expect(svg).toContain("dotted ring: converted from deg"); // the conversion is on the picture, not only on the page
    expect(svg).toMatch(/width="\d+" height="\d+"/);
    expect(new DOMParser().parseFromString(svg, "image/svg+xml").querySelector("parsererror")).toBeNull();
  });

  it("is the same picture every time for the same runs, which is what lets Draw again say the pixels are the same", async () => {
    expect(await plotSvg(model, "x")).toBe(await plotSvg(buildPlot(runs, "am", { kind: "param", key: "b" }, "", conv), "x"));
  });

  it("reads its size from its viewBox", () => {
    expect(viewBoxOf('<svg viewBox="0 0 720 311.5">')).toEqual({ width: 720, height: 311.5 });
  });
});
