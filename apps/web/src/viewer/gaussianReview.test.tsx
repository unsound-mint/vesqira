import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { RohsaRun, RohsaSigmav } from "../api/rohsa";
import type { Plane } from "../api/planes";
import { GaussianReview } from "./GaussianReview";
import { SigmavPlot } from "./SigmavPlot";
import { sigmavSvg } from "./sigmavExport";
import { diagram } from "./sigmavFixture";
import type { ComponentsState } from "./useComponents";
import type { Viewer } from "./useViewer";

const run: RohsaRun = { runId: 7, name: "rohsa_n3", nGauss: 3, nGaussBasis: "declared", cropDatasetId: 1, cropName: "region_a.fits", cropBunit: "K", isCrop: true, usable: true, refusal: null };

const api = vi.hoisted(() => ({ sigmav: vi.fn(), cell: vi.fn(), maps: vi.fn(), plane: vi.fn() }));
vi.mock("../api/rohsa", async (orig) => ({ ...(await orig<typeof import("../api/rohsa")>()), getSigmav: api.sigmav, getCellMask: api.cell, makeClassMaps: api.maps }));
vi.mock("../api/planes", async (orig) => ({ ...(await orig<typeof import("../api/planes")>()), getPlane: api.plane }));
vi.mock("../ui/QueueNote", () => ({ useQueueReason: () => null, QueueNote: () => null }));

const setPinned = vi.fn();
const setEnabled = vi.fn();
const viewer = { datasetId: 1, pinned: null, setPinned } as unknown as Viewer;
const st = { run, setEnabled } as unknown as ComponentsState;

const cls = (index: number, label: string, components: number) => ({ index, label, lo: index === 0 ? 0 : 2.03, hi: index === 0 ? 2.03 : null, components, weight_total: 1 });
const maps = {
  maps: [
    { dataset: { id: 11, name: "narrow.fits" }, class: cls(0, "narrow, σ < 2.03 km/s", 4) },
    { dataset: { id: 12, name: "rest.fits" }, class: cls(1, "σ ≥ 2.03 km/s", 2) },
  ],
  classes: [], weight: "integrated", weight_unit: "K km/s", policy: "NaN where no valid component.", runId: 7, pixels_without_valid_component: 1,
};
const plane = (): Plane => ({ head: { width: 3, height: 2 } as Plane["head"], data: Float32Array.from([1, 0, 5, NaN, 2, 3]) });

beforeEach(() => {
  api.sigmav.mockResolvedValue(diagram);
  api.cell.mockResolvedValue({ mask: { width: 3, height: 2, encoding: "packbits-msb-row-major+zlib+base64", data: "eJzzZQAAAJwATg==" }, pixels: 2, components: 3, cell: { s: 1, v: 1 }, runId: 7 });
  api.maps.mockResolvedValue(maps);
  api.plane.mockImplementation(() => Promise.resolve(plane()));
});
afterEach(() => vi.clearAllMocks());

describe("the sigma-v diagram", () => {
  it("states its axes with units, the scale and the weight, and marks a zero-weight cell with a cross", () => {
    const { container } = render(<SigmavPlot d={diagram} run="rohsa_n3" cut={2.03} cursor={null} selected={null} />);
    const text = container.textContent ?? "";
    expect(text).toContain("centroid velocity (km/s)");
    expect(text).toContain("σ (km/s)");
    expect(text).toContain("weighted by integrated intensity (K km/s)");
    expect(text).toContain("log scale");
    expect(text).toContain("cut σ = 2.03 km/s");
    const zero = container.querySelector('[data-cell="2,1"]')!;
    expect(zero.querySelector("path")).not.toBeNull();
    expect(container.querySelector('[data-cell="0,0"] path')).toBeNull();
    expect(zero.querySelector("title")!.textContent).toContain("1 contributor, weight 0 K km/s");
  });
  it("keeps widths in channels and offers no cut when there is no velocity axis", () => {
    const d: RohsaSigmav = { ...diagram, axes: { centre: { unit: "channels" }, sigma: { unit: "channels" } } };
    const { container } = render(<SigmavPlot d={d} run="r" cut={2.03} cursor={null} selected={null} />);
    expect(container.textContent).toContain("σ (channels)");
    expect(container.textContent).not.toContain("cut σ");
  });
  it("selects the cell under the cursor with the arrow keys and Enter", () => {
    const onSelect = vi.fn();
    const onCursor = vi.fn();
    const { container } = render(<SigmavPlot d={diagram} run="r" cut={null} cursor={{ s: 1, v: 0 }} selected={null} onCursor={onCursor} onSelect={onSelect} />);
    const svg = container.querySelector("svg")!;
    fireEvent.keyDown(svg, { key: "ArrowRight" });
    expect(onCursor).toHaveBeenCalledWith({ s: 1, v: 1 });
    fireEvent.keyDown(svg, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith({ s: 1, v: 0 });
  });
});

describe("the review panel", () => {
  it("draws the diagram with the chosen weight and bins, then asks for the cell's pixels with the same bins", async () => {
    const user = userEvent.setup();
    const { container } = render(<GaussianReview st={st} v={viewer} cut={2.03} />);
    await user.selectOptions(screen.getByLabelText("Weight"), "count");
    await user.type(screen.getByLabelText("Centre min"), "0");
    await user.type(screen.getByLabelText("Centre max"), "10");
    await user.type(screen.getByLabelText("Centre bins"), "2");
    await user.click(screen.getByRole("button", { name: "Draw the diagram" }));
    await waitFor(() => expect(api.sigmav).toHaveBeenCalled());
    expect(api.sigmav.mock.calls[0]![1]).toEqual({ runId: 7, weight: "count", velocityBins: { min: 0, max: 10, n: 2 } });
    await waitFor(() => expect(container.querySelector('[data-cell="1,1"]')).not.toBeNull());
    await user.click(container.querySelector('[data-cell="1,1"]')!);
    await waitFor(() => expect(api.cell).toHaveBeenCalled());
    expect(api.cell.mock.calls[0]![1]).toEqual({ runId: 7, weight: "count", velocityBins: { min: 0, max: 10, n: 2 }, cell: { s: 1, v: 1 } });
    expect(await screen.findByText(/3 components in 2 pixels/)).toBeInTheDocument();
  });
  it("refuses bins that do not parse before asking the API", async () => {
    const user = userEvent.setup();
    render(<GaussianReview st={st} v={viewer} cut={2.03} />);
    await user.type(screen.getByLabelText("Width min"), "0");
    expect(screen.getByRole("alert")).toHaveTextContent(/give min, max and the number of bins/);
    expect(screen.getByRole("button", { name: "Draw the diagram" })).toBeDisabled();
    expect(api.sigmav).not.toHaveBeenCalled();
  });
  it("shows the API's refusal as a sentence", async () => {
    api.sigmav.mockRejectedValue(new Error("The crop has no brightness unit, so the integrated weight cannot be formed."));
    const user = userEvent.setup();
    render(<GaussianReview st={st} v={viewer} cut={2.03} />);
    await user.click(screen.getByRole("button", { name: "Draw the diagram" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/no brightness unit/);
  });
  it("makes the class maps from the cut and pins a pixel of a map in the viewer", async () => {
    const user = userEvent.setup();
    render(<GaussianReview st={st} v={viewer} cut={2.03} />);
    await user.click(screen.getByRole("button", { name: "Make the class maps" }));
    await waitFor(() => expect(api.maps).toHaveBeenCalled());
    expect(api.maps.mock.calls[0]![1]).toEqual({ runId: 7, weight: "integrated", edges: [2.03] });
    expect(await screen.findByText(/narrow, σ < 2.03 km\/s/)).toBeInTheDocument();
    expect(screen.getByText(/narrow.fits, rest.fits/)).toBeInTheDocument();
    const canvases = screen.getAllByRole("application", { name: /3 by 2 pixels/ });
    expect(canvases).toHaveLength(2);
    // jsdom has no layout: the canvas is 0 x 0, so the keyboard picks the pixel (row 0 is the bottom row)
    fireEvent.keyDown(canvases[0]!, { key: "ArrowRight" });
    fireEvent.keyDown(canvases[0]!, { key: "ArrowUp" });
    fireEvent.keyDown(canvases[0]!, { key: "Enter" });
    expect(setPinned).toHaveBeenCalledWith({ x: 1, y: 1 });
    expect(setEnabled).toHaveBeenCalledWith(true);
  });
  it("marks the selected cell's pixels on the maps, and refuses to when the grids differ", async () => {
    const user = userEvent.setup();
    const { container } = render(<GaussianReview st={st} v={viewer} cut={2.03} />);
    await user.click(screen.getByRole("button", { name: "Draw the diagram" }));
    await user.click(await waitFor(() => container.querySelector('[data-cell="1,1"]')!));
    await screen.findByText(/3 components in 2 pixels/);
    await user.click(screen.getByRole("button", { name: "Make the class maps" }));
    expect((await screen.findAllByText(/Checkerboard: pixels of the selected cell/)).length).toBe(2);
    api.plane.mockImplementation(() => Promise.resolve({ head: { width: 4, height: 2 }, data: new Float32Array(8) }));
    await user.click(screen.getByRole("button", { name: "Make the maps again" }));
    expect(await screen.findByText(/not on the same grid/)).toBeInTheDocument();
    expect(screen.queryByText(/Checkerboard/)).toBeNull();
  });
  it("disables the weights the crop's unit cannot support", () => {
    render(<GaussianReview st={{ run: { ...run, cropBunit: "Jy/beam" }, setEnabled } as unknown as ComponentsState} v={viewer} cut={2.03} />);
    expect(screen.getByRole("option", { name: /Optically thin column density.*needs a brightness temperature in K/ })).toBeDisabled();
    expect(screen.getByRole("option", { name: /Integrated intensity \(Jy\/beam km\/s\)/ })).toBeEnabled();
  });
});

describe("the exported diagram", () => {
  it("is a self-contained SVG with its provenance and no unresolved tokens", async () => {
    const svg = await sigmavSvg(diagram, "rohsa_n3", 2.03, { s: 1, v: 1 }, "2026-11-13T00:00:00.000Z");
    expect(svg).not.toContain("var(--");
    expect(svg).toMatch(/^<svg[^>]*xmlns=/);
    const meta = /<metadata id="vesqira-provenance">(.*?)<\/metadata>/.exec(svg)![1]!.replace(/&quot;/g, '"').replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
    const p = JSON.parse(meta);
    expect(p).toMatchObject({ tool: "Vesqira sigma-v diagram", run: "rohsa_n3", runId: 7, weight: "integrated", weightUnit: "K km/s", cut: { sigma: 2.03, unit: "km/s" }, fit: { name: "x.dat", sizeBytes: 100, mtimeNs: 5 } });
    expect(p.centre.edges).toEqual([0, 5, 10]);
    expect(JSON.stringify(p)).not.toMatch(/\/home|\/tmp/);
    expect(svg).toContain("6 of 10 components in the diagram");
  });
});
