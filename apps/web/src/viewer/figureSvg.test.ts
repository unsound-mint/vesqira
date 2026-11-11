import { describe, expect, it } from "vitest";
import { figureSvg, withoutOwnerKeys, xmlEscape } from "./figureSvg";
import type { ImageFigure } from "./figure";
import type { Mark } from "../scene/overlays";

const lut = new Uint8ClampedArray(256 * 3).map((_, i) => Math.floor(i / 3));
const measure = (t: string) => t.length * 6;
const PNG = "data:image/png;base64,iVBORw0KGgo=";

const stroke = { color: "cyan", width: 1.25, dashed: false } as const;
const marks: Mark[][] = [
  // contours: three paths, one dashed (a negative level)
  [
    { type: "path", role: "contour", points: [1, 1, 5, 1, 5, 5], closed: false, stroke },
    { type: "path", role: "contour", points: [2, 2, 6, 2, 6, 6], closed: false, stroke },
    { type: "path", role: "contour", points: [3, 3, 7, 3, 7, 7], closed: false, stroke: { ...stroke, dashed: true } },
  ],
  // orientation segments
  [
    { type: "line", role: "orientation", x1: 1, y1: 1, x2: 2, y2: 2, opacity: 0.5 },
    { type: "line", role: "orientation", x1: 3, y1: 1, x2: 4, y2: 2, opacity: 1, stroke: { color: "amber", width: 2, dashed: false } },
  ],
  // a catalog: two markers (one hollow) and a vector
  [
    { type: "line", role: "orientation", x1: 5, y1: 5, x2: 6, y2: 6, opacity: 1, stroke: { color: "lime", width: 1.5, dashed: false } },
    { type: "marker", role: "catalog", layer: "cat-secret-id", row: 41, x: 5.5, y: 5.5, r: 4, ring: "lime", ringWidth: 1.5, fill: "#440154" },
    { type: "marker", role: "catalog", layer: "cat-secret-id", row: 42, x: 7.5, y: 2.5, r: 4, ring: "lime", ringWidth: 1.5, fill: "none" },
  ],
  // regions, the beam (a filled polygon and its size) and the grid with its labels
  [
    { type: "rect", role: "region", x: 1, y: 1, w: 3, h: 3 },
    { type: "circle", role: "saved", cx: 6, cy: 6, r: 2 },
    { type: "path", role: "beam", points: [1, 7, 2, 7, 2, 8], closed: true, stroke: { color: "white", width: 1.5, dashed: false }, fill: 0.35 },
    { type: "text", role: "beam", x: 1.5, y: 8, text: "12″ × 12″ <b>", color: "white", anchor: "middle", baseline: "bottom" },
    { type: "path", role: "grid", points: [0, 4, 9, 4], closed: false, stroke: { color: "white", width: 0.75, dashed: false } },
    { type: "text", role: "grid", x: 0, y: 4, text: "Dec -30.5°", color: "white", anchor: "start", baseline: "middle" },
  ],
];

const figure: ImageFigure = {
  planeW: 10,
  planeH: 10,
  layers: marks,
  legend: { kind: "scalar", lut, limits: [0.5, 8.25], unit: "K" },
  title: 'Peak & "co" <script>alert(1)</script>',
  captions: ["cube.fits", "Channel 4 of 8, velocity 8 km/s", "viridis, linear stretch, 1st to 99th percentile of finite pixels in K", "Header values are declared by the file, not verified."],
};

const provenance = { source: { name: "cube.fits", identity: "size and time" }, statements: ["Brightness unit (K) declared by the header."], view: { kind: "plane", channel: 3 }, datasetId: 7, path: "/home/me/data/cube.fits" };
const meta = { title: figure.title, caption: "The ridge.", provenance, software: "Vesqira test" };

function parse(svg: string): Document {
  const doc = new DOMParser().parseFromString(svg, "image/svg+xml");
  expect(doc.querySelector("parsererror"), svg.slice(0, 200)).toBeNull();
  return doc;
}
const ink = (doc: Document, role: string) => doc.querySelectorAll(`[data-role="${role}"]`);

describe("SVG export of a figure", () => {
  const svg = figureSvg({ ...figure, raster: PNG, meta }, measure);
  const doc = parse(svg);

  it("is well-formed XML with a size, a viewBox, a title and a description", () => {
    const root = doc.documentElement;
    expect(root.localName).toBe("svg");
    expect(root.getAttribute("viewBox")).toBe(`0 0 ${root.getAttribute("width")} ${root.getAttribute("height")}`);
    expect(doc.querySelector("title")?.textContent).toBe(figure.title);
    const desc = doc.querySelector("desc")?.textContent ?? "";
    expect(desc).toContain("The ridge.");
    expect(desc).toContain("Channel 4 of 8, velocity 8 km/s");
  });

  it("holds one element per mark, told apart by role, each with its halo kept apart", () => {
    expect(ink(doc, "contour")).toHaveLength(3);
    expect(ink(doc, "orientation")).toHaveLength(3); // two segments and one catalog vector
    expect(ink(doc, "catalog")).toHaveLength(2);
    expect(ink(doc, "region")).toHaveLength(1);
    expect(ink(doc, "saved")).toHaveLength(1);
    expect(ink(doc, "beam")).toHaveLength(2); // the ellipse and its size text
    expect(ink(doc, "grid")).toHaveLength(2); // a line and its label
    // a halo is not an ink element: three contours + one grid line + beam outline + 3 segments + 2 markers
    expect(doc.querySelectorAll("[data-halo]")).toHaveLength(3 + 1 + 1 + 3 + 2);
    expect(doc.querySelectorAll("[data-fill]")).toHaveLength(1);
    // the dashed (negative) level keeps its dashes, so its sign does not rest on colour
    expect(Array.from(ink(doc, "contour")).filter((e) => e.hasAttribute("stroke-dasharray"))).toHaveLength(1);
    // a hollow marker (no value) stays hollow, a valued one is filled with the colour of its value
    const fills = Array.from(ink(doc, "catalog")).map((e) => e.getAttribute("fill"));
    expect(fills).toEqual(["#440154", "none"]);
  });

  it("embeds the base raster as a PNG data URL, under the vector layers, and keeps text as text", () => {
    const img = doc.querySelector("image")!;
    expect(img.getAttributeNS("http://www.w3.org/1999/xlink", "href")).toBe(PNG);
    expect(img.getAttribute("style")).toContain("image-rendering:pixelated");
    const all = Array.from(doc.documentElement.querySelectorAll("image, g[data-layer]"));
    expect(all[0]).toBe(img);
    expect(ink(doc, "beam")[1]!.textContent).toBe("12″ × 12″ <b>");
    expect(Array.from(doc.querySelectorAll("text")).map((t) => t.textContent)).toEqual(expect.arrayContaining(["0.5 K", "8.25 K", "Header values are declared by the file, not verified."]));
  });

  it("carries the colour bar as a gradient with its limits and unit", () => {
    expect(doc.querySelectorAll("linearGradient stop").length).toBeGreaterThan(8);
    const labels = Array.from(doc.querySelectorAll("text")).map((t) => t.textContent);
    expect(labels).toContain("0.5 K");
    expect(labels).toContain("8.25 K");
  });

  it("writes the provenance the PNG carries in its metadata, without a dataset id or a path", () => {
    const m = doc.querySelector("metadata")!;
    const body = JSON.parse(m.textContent ?? "");
    expect(body.title).toBe(figure.title);
    expect(body.provenance.source.name).toBe("cube.fits");
    expect(body.provenance.statements).toEqual(["Brightness unit (K) declared by the header."]);
    expect(svg).not.toContain("datasetId");
    expect(svg).not.toContain("/home/me");
    expect(svg).not.toContain("cat-secret-id"); // no overlay id
    expect(svg).not.toMatch(/\brow="/);
  });

  it("cannot carry script, handlers or an external reference", () => {
    expect(doc.querySelector("script, foreignObject, a")).toBeNull();
    expect(svg).not.toMatch(/\son[a-z]+=/i);
    expect(svg).not.toMatch(/(?:href|src)="(?!data:image\/png;base64,)/);
    expect(svg).not.toContain("<script>");
  });

  it("refuses a raster that is not a PNG data URL", () => {
    expect(() => figureSvg({ ...figure, raster: 'javascript:alert(1)"', meta }, measure)).toThrow();
    expect(() => figureSvg({ ...figure, raster: "https://example.org/a.png", meta }, measure)).toThrow();
  });

  it("lays out the caption exactly as the PNG does (one layout function)", async () => {
    const { layoutImageFigure } = await import("./figure");
    const lay = layoutImageFigure(figure, measure);
    const lines = Array.from(doc.querySelectorAll("text")).map((t) => t.textContent);
    for (const l of lay.capLines) expect(lines).toContain(l);
    expect(Number(doc.documentElement.getAttribute("height"))).toBe(lay.H);
  });
});

describe("what goes into a file that may be passed on", () => {
  it("escapes markup and drops characters XML cannot hold", () => {
    expect(xmlEscape('a<b>&"c"\u0001\u0008')).toBe("a&lt;b&gt;&amp;&quot;c&quot;");
  });
  it("removes dataset ids and paths wherever they sit", () => {
    expect(withoutOwnerKeys({ a: [{ datasetId: 1, keep: 2, nested: { path: "/x", ok: true } }], dataset_id: 3 })).toEqual({ a: [{ keep: 2, nested: { ok: true } }] });
  });
});
