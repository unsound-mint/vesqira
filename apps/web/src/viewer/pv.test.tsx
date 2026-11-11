import { act, render, screen, waitFor, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { parsePv } from "../api/channelmaps";
import type { PvHeader } from "../api/channelmaps";
import { lineMarks } from "../scene/overlays";
import { axisFlipped, cellAt, cellText, parseVertices, PvPanel } from "./PvPanel";
import { lineFromDrag, linePath, lineValid, pathLength } from "./region";
import type { Viewer } from "./useViewer";

afterEach(() => vi.unstubAllGlobals());

/** A position-velocity frame as the API writes it: header, offsets, axis, values, counts. */
export function pvFrame(o: { offsets: number[]; axis: number[]; values: number[]; counts?: number[]; head?: Partial<PvHeader> }): ArrayBuffer {
  const no = o.offsets.length, nc = o.axis.length;
  const head = JSON.stringify({
    offsets: no, channels_returned: nc, first_channel: 0, last_channel: nc - 1, layout: "x",
    offset: { unit: "arcsec", origin: "the first vertex; positive along the path", method: "cumulative great-circle separation of the sky positions of the samples along the path (astropy)" },
    path: { vertices: [[1, 1], [9, 1]], length_pixels: 8, spacing_pixels: 1, samples: no },
    width: { requested: 3, unit: "pixel", cross_samples: 3, note: "3 pixels", method: "3 samples across at 1-pixel spacing, perpendicular to the segment, averaged over the finite ones (non-finite excluded, counted)" },
    interpolation: { mode: "linear", method: "bilinear interpolation of the four surrounding pixels; a sample with a non-finite neighbour is excluded" },
    outside_samples: 0, spectral: { axis: "radio velocity", unit: "km/s", unit_status: "converted", native_unit: "m/s", convention: "radio", convention_source: "file", specsys: "LSRK", rest: null, basis: "unit change only", refused: null, reference_frame_note: "x" },
    stats: { finite_count: 4, nonfinite_count: 0, min: 0, max: 3, p01: 0, p99: 3 }, nan_meaning: "no value", caps: {}, ...o.head,
  });
  const pad = (8 - ((4 + head.length) % 8)) % 8;
  const json = head + " ".repeat(pad);
  const buf = new ArrayBuffer(4 + json.length + 8 * no + 8 * nc + 5 * no * nc);
  const dv = new DataView(buf);
  dv.setUint32(0, json.length, true);
  new Uint8Array(buf, 4, json.length).set(new TextEncoder().encode(json));
  let at = 4 + json.length;
  for (const v of o.offsets) { dv.setFloat64(at, v, true); at += 8; }
  for (const v of o.axis) { dv.setFloat64(at, v, true); at += 8; }
  for (const v of o.values) { dv.setFloat32(at, v, true); at += 4; }
  (o.counts ?? o.values.map(() => 3)).forEach((c, i) => dv.setUint8(at + i, c));
  return buf;
}

describe("the position-velocity frame", () => {
  it("reads offsets in arcsec, the axis, values with no-value as NaN, and the samples averaged into each", () => {
    const d = parsePv(pvFrame({ offsets: [0, 60.5, 121], axis: [-1, 0], values: [1, NaN, 3, 4, 5, 6], counts: [3, 0, 2, 3, 3, 3] }));
    expect(Array.from(d.offsets)).toEqual([0, 60.5, 121]);
    expect(Array.from(d.axis)).toEqual([-1, 0]);
    expect(d.values[0]).toBe(1);
    expect(Number.isNaN(d.values[1])).toBe(true);
    expect(Array.from(d.counts)).toEqual([3, 0, 2, 3, 3, 3]);
    expect(d.head.offset.unit).toBe("arcsec");
  });

  it("refuses a frame that is truncated or whose size disagrees with its header", () => {
    const f = pvFrame({ offsets: [0, 1], axis: [0, 1], values: [1, 2, 3, 4] });
    expect(() => parsePv(f.slice(0, f.byteLength - 1))).toThrow(/wrong size/);
    expect(() => parsePv(new ArrayBuffer(2))).toThrow();
  });
});

describe("reading the diagram", () => {
  const d = parsePv(pvFrame({ offsets: [0, 60, 120, 180], axis: [10, 20, 30], values: [1, 2, 3, 4, NaN, 6, 7, 8, 9, 10, 11, 12], counts: [3, 3, 3, 3, 0, 3, 2, 3, 3, 3, 3, 1] }));

  it("maps a pointer to the sample and the channel row, the highest spectral value at the top", () => {
    expect(cellAt(0.01, 0.99, 4, 3, false)).toEqual({ o: 0, c: 0 });   // bottom-left is the lowest value
    expect(cellAt(0.99, 0.01, 4, 3, false)).toEqual({ o: 3, c: 2 });   // top-right is the highest
    expect(cellAt(0.5, 0.5, 4, 3, false)).toEqual({ o: 2, c: 1 });
    expect(cellAt(0.99, 0.01, 4, 3, true)).toEqual({ o: 3, c: 0 });    // an axis that falls with the channel flips the rows
    expect(cellAt(2, -1, 4, 3, false)).toEqual({ o: 3, c: 2 });        // clamped to the plot
  });

  it("flips only when the axis falls with the channel", () => {
    expect(axisFlipped([1, 2, 3])).toBe(false);
    expect(axisFlipped([3, 2, 1])).toBe(true);
    expect(axisFlipped([NaN, 1])).toBe(false);
  });

  it("states offset, spectral value, value and samples averaged, and says no value where there is none", () => {
    expect(cellText(d, 1, 0, "K")).toBe("offset 60 arcsec · radio velocity 10 km/s (channel 1) · 2 K · 3 of 3 samples across");
    expect(cellText(d, 0, 1, "K")).toBe("offset 0 arcsec · radio velocity 20 km/s (channel 2) · no value · 0 of 3 samples across");
    expect(cellText(d, 3, 2, null)).toBe("offset 180 arcsec · radio velocity 30 km/s (channel 3) · 12 · 1 of 3 samples across");
  });
});

describe("the path", () => {
  it("is a drag from the first point to the second, keeping the width, and Shift adds a vertex", () => {
    const a = lineFromDrag({ x: 2, y: 3 }, { x: 10, y: 3 });
    expect(a).toEqual({ shape: "line", points: [[2, 3], [10, 3]], width: 1, widthUnit: "pixel" });
    const kept = { ...a, width: 4, widthUnit: "arcsec" as const };
    expect(lineFromDrag({ x: 0, y: 0 }, { x: 5, y: 5 }, kept)).toEqual({ shape: "line", points: [[0, 0], [5, 5]], width: 4, widthUnit: "arcsec" });
    expect(lineFromDrag({ x: 10, y: 3 }, { x: 10, y: 9 }, kept, true).points).toEqual([[2, 3], [10, 3], [10, 9]]);
  });

  it("has a length along its legs and is valid only with two different vertices", () => {
    expect(pathLength([[0, 0], [3, 4], [3, 10]])).toBe(11);
    expect(lineValid({ shape: "line", points: [[1, 1], [1, 1]], width: 1, widthUnit: "pixel" })).toBe(false);
    expect(lineValid({ shape: "line", points: [[1, 1], [1, 2]], width: 1, widthUnit: "pixel" })).toBe(true);
    expect(lineValid(null)).toBe(false);
  });

  it("is drawn as a polyline with a chevron at the end, with y measured from the top of the picture", () => {
    const { path, head } = linePath([[2, 3], [10, 3]], 24);
    expect(path).toEqual([2.5, 20.5, 10.5, 20.5]);
    expect(head).toHaveLength(6);
    expect(head[2]).toBe(10.5); // the tip is the last vertex
    expect(head[3]).toBe(20.5);
    expect(head[0]).toBeLessThan(10.5); // the arms trail behind the tip
    expect(lineMarks([[2, 3], [10, 3]], 24, "saved")).toHaveLength(2);
    expect(lineMarks([[2, 3], [10, 3]], 24, "draft")[0]).toMatchObject({ type: "path", role: "draft", stroke: { dashed: true } });
  });

  it("is typed as vertices, one per line, pixel numbers from 1; anything else is refused", () => {
    expect(parseVertices("3, 4\n 11 ,4\n")).toEqual([[2, 3], [10, 3]]);
    expect(parseVertices("1 1\n5;5")).toEqual([[0, 0], [4, 4]]);
    expect(parseVertices("3, 4")).toBeNull();
    expect(parseVertices("3, x\n4, 5")).toBeNull();
    expect(parseVertices("0, 1\n2, 2")).toBeNull();
  });
});

const viewer = (over: Partial<Record<string, unknown>> = {}) => ({
  datasetId: 5, view: { channels: 30, bunit: "K", spectral: { unit: "m/s" } }, interp: null, colormap: "viridis", stretch: "linear",
  path: { shape: "line", points: [[2, 3], [10, 3]], width: 2, widthUnit: "pixel" }, setPath: vi.fn(),
  offers: { spectral: { native_unit: "m/s", targets: [{ unit: "km/s", enabled: true, reason: null, needs_convention: false }, { unit: "GHz", enabled: false, reason: "needs a rest frequency", needs_convention: false }] } },
  ...over,
}) as unknown as Viewer;

describe("PvPanel", () => {
  it("computes along the drawn path, states how, and reads a cell with the keyboard", async () => {
    const calls: { url: string; body: Record<string, unknown> }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init?.body)) });
      return new Response(pvFrame({ offsets: [0, 60, 120], axis: [-1, 0, 1], values: [1, 2, 3, 4, 5, 6, 7, 8, 9] }), { status: 200, headers: { "Content-Type": "application/vnd.vesqira.pv" } });
    }));
    render(<PvPanel v={viewer()} />);
    await userEvent.click(screen.getByRole("button", { name: "Compute the diagram" }));
    await screen.findByTestId("pv-plot");
    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toBe("/api/datasets/5/pv");
    expect(calls[0]!.body).toEqual({ points: [[2, 3], [10, 3]], width: 2, widthUnit: "pixel", interpolation: "linear", first: 0, last: 29 });
    expect(screen.getByText(/cumulative great-circle separation/)).toBeInTheDocument();
    expect(screen.getByText(/Interpolated: bilinear interpolation/)).toBeInTheDocument();
    expect(screen.getByText(/3 samples across at 1-pixel spacing/)).toBeInTheDocument();
    const plot = screen.getByTestId("pv-plot");
    plot.focus();
    fireEvent.keyDown(plot, { key: "ArrowRight" });
    expect(await screen.findByText(/offset 120 arcsec/)).toBeInTheDocument();
    fireEvent.keyDown(plot, { key: "ArrowUp" });
    expect(await screen.findByText(/radio velocity 1 km\/s \(channel 3\)/)).toBeInTheDocument();
  });

  it("sends the nearest-pixel choice, a converted spectral axis with its convention and a channel range", async () => {
    const calls: Record<string, unknown>[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_u: string, init?: RequestInit) => {
      calls.push(JSON.parse(String(init?.body)));
      return new Response(pvFrame({ offsets: [0, 60], axis: [0, 1], values: [1, 2, 3, 4] }), { status: 200 });
    }));
    const v = viewer({ offers: { spectral: { native_unit: "Hz", targets: [{ unit: "km/s", enabled: true, reason: null, needs_convention: true }] } } });
    render(<PvPanel v={v} />);
    await userEvent.selectOptions(screen.getByLabelText("Sampling"), "nearest");
    await userEvent.selectOptions(screen.getByLabelText("Spectral axis"), "km/s");
    await userEvent.selectOptions(screen.getByLabelText("Velocity convention"), "optical");
    await userEvent.selectOptions(screen.getByLabelText("Width unit"), "arcsec");
    const first = screen.getByLabelText("First channel");
    await userEvent.clear(first);
    await userEvent.type(first, "5");
    await userEvent.click(screen.getByRole("button", { name: "Compute the diagram" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({ interpolation: "nearest", unit: "km/s", convention: "optical", widthUnit: "arcsec", first: 4, last: 29 });
  });

  it("shows the server's refusal and offers nothing to compute without a line", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "a width in arcsec needs a celestial WCS; this file has none, give the width in pixels" }), { status: 422, headers: { "Content-Type": "application/json" } })));
    const { rerender } = render(<PvPanel v={viewer({ path: null })} />);
    expect(screen.getByRole("button", { name: "Compute the diagram" })).toBeDisabled();
    rerender(<PvPanel v={viewer()} />);
    await userEvent.click(screen.getByRole("button", { name: "Compute the diagram" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("needs a celestial WCS");
  });

  it("can be cancelled while it runs", async () => {
    let signal: AbortSignal | undefined;
    vi.stubGlobal("fetch", vi.fn((_u: string, init?: RequestInit) => new Promise((_res, rej) => {
      signal = init?.signal ?? undefined;
      signal?.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError")));
    })));
    render(<PvPanel v={viewer()} />);
    await userEvent.click(screen.getByRole("button", { name: "Compute the diagram" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(signal?.aborted).toBe(true);
    await waitFor(() => expect(screen.getByRole("button", { name: "Compute the diagram" })).toBeEnabled());
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("refuses an orientation map and a file with one channel, with the reason", () => {
    const { unmount } = render(<PvPanel v={viewer({ interp: { kind: "axial_orientation" } })} />);
    expect(screen.getByText(/not defined for angles of an axial map/)).toBeInTheDocument();
    unmount();
    render(<PvPanel v={viewer({ view: { channels: 1, bunit: "K", spectral: null } })} />);
    expect(screen.getByText(/no channel axis of more than one channel/)).toBeInTheDocument();
  });

  it("applies typed vertices as the path, keeping the width", async () => {
    const setPath = vi.fn();
    render(<PvPanel v={viewer({ setPath })} />);
    const box = screen.getByLabelText(/Vertices/);
    await userEvent.clear(box);
    await userEvent.type(box, "4, 5{enter}20, 5{enter}20, 12");
    await userEvent.click(screen.getByRole("button", { name: "Use these vertices" }));
    expect(setPath).toHaveBeenLastCalledWith({ shape: "line", points: [[3, 4], [19, 4], [19, 11]], width: 2, widthUnit: "pixel" });
    await userEvent.clear(box);
    await userEvent.type(box, "4, 5");
    await userEvent.click(screen.getByRole("button", { name: "Use these vertices" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at least two vertices");
    await act(async () => {});
  });
});
