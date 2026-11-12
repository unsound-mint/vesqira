import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Components, RohsaRun } from "../api/rohsa";
import { parseComponents } from "../api/rohsa";
import type { Spectrum } from "../api/planes";
import { GaussianComponents } from "./GaussianComponents";
import { CNM_SIGMA_DEFAULT_KMS, CNM_TD_MAX_K, DOPPLER_K_PER_KMS2, classify, cutSigma, cutText, parseCut, residual, sigmaOfTd, tdOfSigma } from "./rohsa";
import type { Viewer } from "./useViewer";

const run: RohsaRun = { runId: 7, name: "rohsa_n3", nGauss: 3, nGaussBasis: "declared by the run trace", cropDatasetId: 1, cropName: "region_a.fits", cropBunit: "K", isCrop: true, usable: true, refusal: null };

const NC = 5;
const axis = Float64Array.from([-2, -1, 0, 1, 2]);
const curves = [Float64Array.from([0, 1, 2, 1, 0]), Float64Array.from([1, 1, 1, 1, 1]), Float64Array.from([0, 0, 0, 0, 0])];
const model = Float64Array.from([1, 2, 3, 2, 1]);
const comp = (k: number, amp: number, sigma: number | null, valid = true) => ({
  k, amp, mean_channel_file: 3, mean_channel: 2, sigma_channel: 1, valid, centre: valid ? 0 : null, sigma, fwhm: sigma === null ? null : 2.3548 * sigma, t_doppler_k: sigma === null ? null : 121.1 * sigma * sigma,
});
const components: Components = {
  axis, model, curves,
  head: {
    ok: true, x: 2, y: 1, channels: NC, n_gauss: 3, bunit: "K", layout: "", conventions: {},
    axis: { kind: "velocity", name: "radio velocity", unit: "km/s", source_unit: "m/s", unit_status: "inferred", note: null },
    components: [comp(1, 2, 1.5), comp(2, 1, 2.03), comp(3, 0, 3)],
  },
};
// the data is the model plus 0.5 and a channel with no value
const spectrum = { head: { bunit: "K", bunit_status: "declared", channels: NC }, axis, mean: Float64Array.from([1.5, 2.5, NaN, 2.5, 1.5]), count: new Uint32Array(NC) } as unknown as Spectrum;

const api = vi.hoisted(() => ({ list: vi.fn(), index: vi.fn(), comps: vi.fn(), spectrum: vi.fn() }));
vi.mock("../api/rohsa", async (orig) => ({ ...(await orig<typeof import("../api/rohsa")>()), listRohsaRuns: api.list, indexRohsa: api.index, getComponents: api.comps }));
vi.mock("../api/planes", async (orig) => ({ ...(await orig<typeof import("../api/planes")>()), getSpectrum: api.spectrum }));

const viewer = { datasetId: 1, view: {}, probe: { x: 2, y: 1 } } as unknown as Viewer;

beforeEach(() => {
  api.list.mockResolvedValue([run]);
  api.index.mockResolvedValue({ state: "ready", done_bytes: 10, size_bytes: 10, rows: 72, malformed: 0, reason: null, examples: [], index_bytes: 192 });
  api.comps.mockResolvedValue(components);
  api.spectrum.mockResolvedValue(spectrum);
});
afterEach(() => vi.clearAllMocks());

describe("cut and classification", () => {
  it("is CNM only strictly below the cut, so a width equal to the cut is in the others", () => {
    expect(classify(2.029, 2.03)).toBe("cnm");
    expect(classify(2.03, 2.03)).toBe("other");
    expect(classify(2.031, 2.03)).toBe("other");
    expect(CNM_SIGMA_DEFAULT_KMS).toBe(2.03);        // the cut earlier results and class maps used, unchanged
  });

  it("derives the starting cut from a 500 K Doppler temperature and converts both ways with the worker's constant", () => {
    expect(DOPPLER_K_PER_KMS2).toBe(121.1);
    expect(CNM_TD_MAX_K).toBe(500);
    expect(sigmaOfTd(500)).toBeCloseTo(2.03195, 5);
    expect(tdOfSigma(2.03)).toBeCloseTo(499.041, 3);
    expect(cutSigma("2.03", "sigma")).toBe(2.03);
    expect(cutSigma("500", "td")).toBeCloseTo(2.03195, 5);
    expect(cutSigma("0", "td")).toBeNull();
    expect(cutSigma("x", "sigma")).toBeNull();
    expect(cutSigma(cutText(2.03, "td"), "td")).toBeCloseTo(2.03, 6);      // switching the basis back and forth does not drift
  });

  it("accepts a positive number as typed and nothing else", () => {
    expect(parseCut(" 1.5 ")).toBe(1.5);
    expect(parseCut(".5")).toBe(0.5);
    for (const bad of ["", "0", "-1", "abc", "1,5", "NaN", "Infinity", "1e999"]) expect(parseCut(bad)).toBeNull();
  });

  it("subtracts model from data for display and leaves a missing channel missing, never zero", () => {
    const r = residual([1.5, NaN, 0, 2], [1, 2, 0, 3]);
    expect(r[0]).toBe(0.5);
    expect(Number.isNaN(r[1])).toBe(true);
    expect(r[2]).toBe(0);
    expect(r[3]).toBe(-1);
  });
});

describe("frame", () => {
  it("reads axis, model and one curve per Gaussian, and refuses a frame of the wrong size", () => {
    const head = JSON.stringify({ channels: 2, n_gauss: 1 });
    const json = head + " ".repeat((8 - ((4 + head.length) % 8)) % 8);
    const buf = new ArrayBuffer(4 + json.length + 3 * 2 * 8);
    const dv = new DataView(buf);
    dv.setUint32(0, json.length, true);
    new Uint8Array(buf, 4, json.length).set(new TextEncoder().encode(json));
    [-1, 1, 5, 6, 0.5, 0].forEach((x, i) => dv.setFloat64(4 + json.length + i * 8, x, true));
    const c = parseComponents(buf);
    expect(Array.from(c.axis)).toEqual([-1, 1]);
    expect(Array.from(c.model)).toEqual([5, 6]);
    expect(Array.from(c.curves[0]!)).toEqual([0.5, 0]);
    expect(() => parseComponents(buf.slice(0, buf.byteLength - 8))).toThrow();
  });
});

describe("Gaussian components over the spectrum", () => {
  it("shows nothing when no run applies to the dataset", async () => {
    api.list.mockResolvedValue([]);
    const { container } = render(<GaussianComponents v={viewer} />);
    await waitFor(() => expect(api.list).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("explains why a run cannot be drawn and keeps the toggle off", async () => {
    api.list.mockResolvedValue([{ ...run, isCrop: false, usable: false, refusal: "this cube is not known to be on the run's grid: axis 3 unit differs or is unresolved" }]);
    render(<GaussianComponents v={viewer} />);
    expect(await screen.findByText(/not known to be on the run's grid/)).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: /Gaussian components/ })).toBeDisabled();
    expect(api.index).not.toHaveBeenCalled();
  });

  it("draws nothing until switched on, then indexes, fetches the pixel and lists the components with the cut marking CNM", async () => {
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    const toggle = await screen.findByRole("switch", { name: /Gaussian components/ });
    expect(api.index).not.toHaveBeenCalled();
    await user.click(toggle);
    await waitFor(() => expect(api.comps).toHaveBeenCalledWith(1, 7, 2, 1, expect.anything()));
    // the data is that pixel's own spectrum (a one-pixel region), not the region mean
    expect(api.spectrum).toHaveBeenCalledWith(1, { shape: "rect", x0: 2, y0: 1, x1: 2, y1: 1 }, expect.anything());
    const plot = await screen.findByRole("img", { name: /Gaussian components at pixel 3, 2/ });
    expect(plot.querySelectorAll("polyline").length).toBeGreaterThanOrEqual(1 + 1 + 3); // data (2 pieces), model, components
    expect(screen.getByRole("img", { name: /Residual, data minus model/ })).toBeInTheDocument();
    expect(screen.getByText(/residual = data − model/)).toBeInTheDocument();
    expect(screen.getByText(/model − data/)).toBeInTheDocument();

    const table = screen.getByRole("region", { name: "Fitted components" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    // default cut 2.03: sigma 1.5 is CNM, sigma 2.03 exactly is others, sigma 3 is others
    expect(within(rows[0]!).getByText("CNM")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("others")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("others")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("272.5")).toBeInTheDocument();   // T_D = 121.1 * 1.5^2 = 272.475, shown to 4 figures
  });

  it("re-marks the components when the person changes the cut, and says the starting value is a convention, not a result", async () => {
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    await user.click(await screen.findByRole("switch", { name: /Gaussian components/ }));
    await screen.findByRole("region", { name: "Fitted components" });
    expect(screen.getByText(/is a convention, not a result/)).toBeInTheDocument();
    expect(screen.getByText(/CNM when σ < 2.03 km\/s \(T_D < 499 K\)/)).toBeInTheDocument();
    const cut = screen.getByLabelText("CNM width cut σ (km/s)");
    await user.clear(cut);
    await user.type(cut, "2.5");
    const rows = within(screen.getByRole("region", { name: "Fitted components" })).getAllByRole("row").slice(1);
    expect(within(rows[1]!).getByText("CNM")).toBeInTheDocument(); // 2.03 < 2.5
    expect(within(rows[2]!).getByText("others")).toBeInTheDocument();
    expect(screen.getByText(/Your cut: a component is CNM when σ < 2.5 km\/s/)).toBeInTheDocument();
    await user.clear(cut);
    await user.type(cut, "abc");
    expect(screen.getByText("Enter a positive number.")).toBeInTheDocument();
    for (const r of within(screen.getByRole("region", { name: "Fitted components" })).getAllByRole("row").slice(1)) {
      expect(within(r).queryByText("CNM")).toBeNull(); // no valid cut, no classification
    }
  });

  it("lets the person set the cut as a Doppler temperature, and classifies by the width it implies", async () => {
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    await user.click(await screen.findByRole("switch", { name: /Gaussian components/ }));
    await screen.findByRole("region", { name: "Fitted components" });
    await user.selectOptions(screen.getByLabelText("Set the CNM cut as"), "td");
    const cut = screen.getByLabelText("CNM Doppler temperature cut T_D (K)");
    expect(cut).toHaveValue("499.041");                       // the starting width 2.03 km/s as a temperature
    await user.clear(cut);
    await user.type(cut, "800");                              // sigma < sqrt(800 / 121.1) = 2.57 km/s
    expect(screen.getByText(/CNM when σ < 2.57 km\/s \(T_D < 800 K\)/)).toBeInTheDocument();
    const rows = within(screen.getByRole("region", { name: "Fitted components" })).getAllByRole("row").slice(1);
    expect(within(rows[1]!).getByText("CNM")).toBeInTheDocument();   // sigma 2.03 < 2.57
    await user.selectOptions(screen.getByLabelText("Set the CNM cut as"), "sigma");
    expect(screen.getByLabelText("CNM width cut σ (km/s)")).toHaveValue("2.57024");
  });

  it("does not draw the data or residual when the brightness unit is not confirmed equal, and says so", async () => {
    api.spectrum.mockResolvedValue({ ...spectrum, head: { ...spectrum.head, bunit: "Jy/beam" } });
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    await user.click(await screen.findByRole("switch", { name: /Gaussian components/ }));
    expect(await screen.findByText(/not confirmed equal to the unit of the fit's amplitudes/)).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /Residual/ })).toBeNull();
  });

  it("shows progress while the .dat is indexed, and the reason when it cannot be", async () => {
    api.index.mockResolvedValueOnce({ state: "building", done_bytes: 25, size_bytes: 100, rows: 1, malformed: 0, reason: null, examples: [], index_bytes: 8 });
    api.index.mockResolvedValueOnce({ state: "refused", done_bytes: 40, size_bytes: 100, rows: 2, malformed: 1, reason: "1 malformed row(s)", examples: [{ line: 11, problem: "3 columns instead of 5" }], index_bytes: 8 });
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    await user.click(await screen.findByRole("switch", { name: /Gaussian components/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("1 malformed row(s)");
    expect(screen.getByText(/line 11: 3 columns instead of 5/)).toBeInTheDocument();
    expect(api.comps).not.toHaveBeenCalled();
  });

  it("says the host is reading the .dat and keeping the index when the fit lives on a host", async () => {
    api.index.mockResolvedValueOnce({ state: "building", done_bytes: 25, size_bytes: 100, rows: 1, malformed: 0, reason: null, examples: [], index_bytes: 8, where: "host" });
    api.index.mockReturnValueOnce(new Promise(() => {})); // the next call is still running
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    await user.click(await screen.findByRole("switch", { name: /Gaussian components/ }));
    const status = await screen.findByText(/The host is reading the \.dat once to find each pixel: 25%/);
    expect(status).toHaveTextContent("kept in the account's cache folder on the host");
    expect(status).toHaveTextContent("the .dat does not leave it");
  });

  it("marks an invalid width and an unresolved velocity axis instead of inventing values", async () => {
    api.comps.mockResolvedValue({
      ...components,
      head: { ...components.head, axis: { kind: "channel", name: null, unit: null, source_unit: null, unit_status: null, note: "the spectral axis is not a resolved velocity axis" }, components: [comp(1, 2, null, false), comp(2, 1, null), comp(3, 0, null)] },
    });
    const user = userEvent.setup();
    render(<GaussianComponents v={viewer} />);
    await user.click(await screen.findByRole("switch", { name: /Gaussian components/ }));
    const table = await screen.findByRole("region", { name: "Fitted components" });
    expect(within(table).getByText("invalid σ, not drawn")).toBeInTheDocument();
    expect(within(table).queryByText("CNM")).toBeNull();
    expect(screen.getByText(/need a velocity axis/)).toBeInTheDocument();
  });
});
