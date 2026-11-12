import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset, Header, ImageDataset, Interpretation } from "../api/catalog";
import type { RelOrientSummary } from "../api/analysis";
import { RelativeOrientationPanel, RelOrientView } from "./RelativeOrientation";
import { PRESETS } from "./presetsFixture";
import { parseEdges, plotRange, strataCsv } from "./StratifiedOrientation";

const interp = (over: Partial<Interpretation> = {}): Interpretation =>
  ({ kind: "axial_orientation", unit: "deg", reliabilityId: null, frame: "pixel", quantity: "b_field", declaredBy: "user", declaredAt: "", stale: false, ...over }) as Interpretation;
const map = (id: number, name: string, i: Interpretation | null = interp(), over: Partial<ImageDataset> = {}): ImageDataset =>
  ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: i, nature: "", unitDeclaration: null, hostId: 0, header: {} as Header, ...over });

type Sum = RelOrientSummary;
const strata = (): NonNullable<Sum["stratified"]> => ({
  binning: { mode: "equal_count", asked: 3, bins: 3, edges: [1e20, 2e20, 4e20, 9e20], notes: [], rule: "x" },
  third_map: { unit: "cm-2", unit_source: "file", unit_note: "the unit is the file's BUNIT", pixels_without_value: 12 },
  counts: { compared: 3000, third_map_missing: 30, outside_edges: 0, in_bins: 2970 },
  bins: [
    { index: 0, lo: 1e20, hi: 2e20, centre: 1.5e20, median: 1.4e20, n: 990, am: 0.02, am_weighted: null, bootstrap_am_16_84: [-0.01, 0.05], bootstrap_samples: 2000, median_abs_dpsi_deg: 44, prs: { V: 0.6, V_weighted: null },
      independent_beams: 33.1, independent_beams_reason: null, uniform_reference: { am: 0, sigma_pixels: 0.0225, sigma_weighted_pixels: null, sigma_beams: 0.123 } },
    { index: 1, lo: 2e20, hi: 4e20, centre: 3e20, median: 3e20, n: 990, am: 0.21, am_weighted: null, bootstrap_am_16_84: [0.18, 0.24], bootstrap_samples: 2000, median_abs_dpsi_deg: 38, prs: { V: 6.6, V_weighted: null },
      independent_beams: 33.1, independent_beams_reason: null, uniform_reference: { am: 0, sigma_pixels: 0.0225, sigma_weighted_pixels: null, sigma_beams: 0.123 } },
    { index: 2, lo: 4e20, hi: 9e20, centre: 6.5e20, median: 5e20, n: 990, am: 0.55, am_weighted: null, bootstrap_am_16_84: [0.52, 0.58], bootstrap_samples: 2000, median_abs_dpsi_deg: 25, prs: { V: 17.3, V_weighted: null },
      independent_beams: 33.1, independent_beams_reason: null, uniform_reference: { am: 0, sigma_pixels: 0.0225, sigma_weighted_pixels: null, sigma_beams: 0.123 } },
  ],
  definitions: {}, policy: "Agreement statistics inside bins of a third map, not a validation of either tracer.",
});
const summary = (st = strata()): Sum => ({
  label: "agreement statistic", mode: "map_map", am: 0.26, am_weighted: null, median_abs_dpsi_deg: 36,
  parameters: { n_shifts: 0, min_shift_deg: 1, keep_fraction: 0.9, seed: 20261020, bins: 9 },
  definitions: {}, conversion: "Both maps as B-field orientation.", weighting: "unweighted", map: { nx: 60, ny: 50 }, counts: { pixels: 3000, used: 3000, share_used: 1, excluded: {} },
  histogram: { edges_deg: [0, 45, 90], counts: [2000, 1000], fractions: [2 / 3, 1 / 3] },
  null: { method: "shifted map", skipped: "no shifts were asked for" }, table: { file: null, rows: 3000 }, policy: "Agreement statistic.", stratified: st,
}) as Sum;

afterEach(() => vi.unstubAllGlobals());

describe("stratified relative orientation: the plot and the table", () => {
  it("draws one entry per bin with the counts, the zero line, the reference band and the unit on the axis", () => {
    render(<RelOrientView s={summary()} names={["psi.fits", "planck.fits", "nhi.fits"]} tableUrl={null} resultId={7} />);
    const reg = screen.getByRole("region", { name: "Alignment by bins of a third map" });
    const plot = within(reg).getByTestId("strata-plot");
    expect(plot.getAttribute("aria-label")).toMatch(/Alignment measure against nhi.fits in 3 bins/);
    expect(plot.querySelectorAll("[data-bin]")).toHaveLength(3);
    expect(plot.querySelectorAll("[data-ref]")).toHaveLength(3);
    const text = Array.from(plot.querySelectorAll("text"), (t) => t.textContent).join(" "); // the legend wraps over several lines
    expect(text).toContain("n=990");
    expect(text).toContain("nhi.fits (cm-2)");
    expect(text).toMatch(/Line at 0: no preferred orientation/);
    expect(text).toMatch(/for independent beams/);
    expect(text).not.toMatch(/validated|success|\bright\b|\bwrong\b/i);
  });

  it("lists every bin in a table with its range and centre in the third map's unit, its AM, interval, beams and uniform reference", () => {
    render(<RelOrientView s={summary()} names={["psi.fits", "planck.fits", "nhi.fits"]} tableUrl={null} />);
    const table = screen.getByRole("table", { name: "Alignment by bin" });
    expect(within(table).getByRole("columnheader", { name: "Range (cm-2)" })).toBeInTheDocument();
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    const cells = within(rows[2]!).getAllByRole("cell").map((c) => c.textContent);
    expect(cells).toEqual(["4e+20 to 9e+20", "6.5e+20", "990", "33", "0.550", "0.520 to 0.580", "17.3", "0.0225 px, 0.1230 beams"]);
    expect(screen.getByText(/2,970 of 3,000 compared pixels are in bins; 30 have no value in the third map/)).toBeInTheDocument();
    expect(screen.getByText(/V is the projected Rayleigh statistic of the bin and is shown without a p-value/)).toBeInTheDocument();
  });

  it("says when equal-count bins could not be split, shows an empty bin without a number, and falls back to pixels without beams", () => {
    const st = strata();
    st.binning.notes = ["3 equal-count bins were asked, but tied values in the third map cannot be split: 2 bins hold them, so their sizes differ"];
    st.bins[1] = { ...st.bins[1]!, n: 0, am: null, bootstrap_am_16_84: null, median: null, independent_beams: null, uniform_reference: null, prs: { V: null, V_weighted: null }, median_abs_dpsi_deg: null };
    for (const b of [st.bins[0]!, st.bins[2]!]) b.uniform_reference = { am: 0, sigma_pixels: 0.0225, sigma_weighted_pixels: null, sigma_beams: null };
    render(<RelOrientView s={summary(st)} names={["a", "b", "c"]} tableUrl={null} />);
    expect(screen.getByText(/tied values in the third map cannot be split/)).toBeInTheDocument();
    const plot = screen.getByTestId("strata-plot");
    expect(plot.querySelectorAll("[data-ref]")).toHaveLength(2);
    expect(plot.textContent).toContain("empty");
    expect(plot.textContent).toMatch(/for pixels/);
    const row = within(screen.getByRole("table", { name: "Alignment by bin" })).getAllByRole("row")[2]!;
    expect(within(row).getAllByRole("cell").map((c) => c.textContent).slice(2, 8)).toEqual(["0", "—", "—", "—", "—", "—"]);
  });

  it("scales the vertical axis to the drawn intervals and the reference, not to a fixed range", () => {
    expect(plotRange(strata().bins)).toBe(0.75);   // 0.58 * 1.08 rounds up to the next step
    const small = strata();
    small.bins.forEach((b) => { b.am = 0.01; b.bootstrap_am_16_84 = [0, 0.02]; b.uniform_reference = { am: 0, sigma_pixels: 0.01, sigma_weighted_pixels: null, sigma_beams: null }; });
    expect(plotRange(small.bins)).toBe(0.1);   // never tighter than the smallest step: a flat result is not blown up
  });
});

describe("stratified relative orientation: exports", () => {
  let blobs: Blob[];
  beforeEach(() => {
    blobs = [];
    URL.createObjectURL = vi.fn((b: Blob | MediaSource) => { blobs.push(b as Blob); return "blob:x"; });
    URL.revokeObjectURL = vi.fn();
    HTMLAnchorElement.prototype.click = vi.fn();
  });
  const text = (b: Blob) => new Promise<string>((res) => { const r = new FileReader(); r.onload = () => res(String(r.result)); r.readAsText(b); });

  it("exports the table as CSV with the unit, the binning and the counts in a header, and empty cells for what is not available", async () => {
    const st = strata();
    st.bins[1] = { ...st.bins[1]!, n: 0, am: null, bootstrap_am_16_84: null, uniform_reference: null, independent_beams: null, median: null, median_abs_dpsi_deg: null, prs: { V: null, V_weighted: null } };
    render(<RelOrientView s={summary(st)} names={["psi.fits", "planck.fits", "nhi.fits"]} tableUrl={null} />);
    await userEvent.click(screen.getByRole("button", { name: "Export table (CSV)" }));
    const csv = await text(blobs[0]!);
    expect(csv).toContain("# maps: psi.fits against planck.fits; binned by nhi.fits");
    expect(csv).toContain("# third map unit: cm-2 (the file's BUNIT)");
    expect(csv).toContain("# pixels: 3000 compared, 30 with no third-map value, 0 outside the edges, 2970 in bins");
    expect(csv).toContain("bin,lower_cm-2,upper_cm-2,centre_cm-2,median_cm-2,pixels,independent_beams,am,am_weighted");
    const lines = csv.trim().split("\n").filter((l) => !l.startsWith("#"));
    expect(lines).toHaveLength(4);
    expect(lines[1]).toBe("1,1e+20,2e+20,1.5e+20,1.4e+20,990,33.1,0.02,,-0.01,0.05,0.6,,0.0225,0.123,44");
    expect(lines[2]).toBe(`2,2e+20,4e+20,3e+20,,0${",".repeat(10)}`);   // an empty bin has no AM, not 0
    expect(strataCsv(st, ["a"], "t")).toContain("# maps: a against —");
  });

  it("exports the figure as SVG with its provenance (maps, unit, binning, counts, parameters) as metadata and no style sheet", async () => {
    render(<RelOrientView s={summary()} names={["psi.fits", "planck.fits", "nhi.fits"]} tableUrl={null} resultId={7} />);
    await userEvent.click(screen.getByRole("button", { name: "Export figure (SVG)" }));
    const svg = await text(blobs[0]!);
    expect(svg).toMatch(/^<\?xml/);
    const meta = svg.match(/<metadata id="vesqira-provenance">([^<]*)<\/metadata>/)![1]!.replace(/&quot;/g, '"').replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">");
    const p = JSON.parse(meta);
    expect(p).toMatchObject({ tool: "Vesqira stratified orientation plot", label: "agreement statistic", resultDatasetId: 7,
      maps: { first: "psi.fits", second: "planck.fits", third: "nhi.fits" }, thirdMapUnit: { unit: "cm-2", source: "file" }, counts: { compared: 3000, in_bins: 2970 } });
    expect(p.binning.edges).toEqual([1e20, 2e20, 4e20, 9e20]);
    expect(p.parameters.seed).toBe(20261020);
    expect(svg).not.toMatch(/var\(--/);   // tokens are resolved so the file stands alone
    expect(svg).not.toContain("<style");
  });
});

describe("stratified relative orientation: the request", () => {
  const edgeCases = [["1e20, 5e20 ; 1e21", [1e20, 5e20, 1e21]], ["1 2", [1, 2]]] as const;
  it("reads edges separated by commas, semicolons or spaces and refuses what cannot be a list of edges", () => {
    for (const [t, e] of edgeCases) expect(parseEdges(t)).toEqual({ edges: [...e] });
    expect(parseEdges("5")).toMatchObject({ error: expect.stringMatching(/at least two/) });
    expect(parseEdges("1, x")).toMatchObject({ error: expect.stringMatching(/must be a number/) });
    expect(parseEdges("3, 3")).toMatchObject({ error: expect.stringMatching(/must increase/) });
    expect(parseEdges("1, 0")).toMatchObject({ error: expect.stringMatching(/must increase/) });
  });

  it("offers only scalar maps on this computer, sends stratifyId with the bins, and leaves the request alone when no third map is chosen", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/analysis/relative-orientation/presets") return new Response(JSON.stringify(PRESETS), { status: 200, headers: { "Content-Type": "application/json" } });
      calls.push({ url, init });
      return new Response(JSON.stringify({ dataset: map(9, "relorient-1-2.fits", null), summary: summary() }), { status: 201, headers: { "Content-Type": "application/json" } });
    }));
    const psi = map(1, "psi.fits");
    const ds: Dataset[] = [psi, map(2, "planck.fits"), map(3, "nhi.fits", null), map(4, "remote.fits", null, { hostId: 5 }), map(5, "other_orientation.fits"), map(6, "gone.fits", null, { archived: true } as Partial<ImageDataset>)];
    const created: Dataset[] = [];
    render(<RelativeOrientationPanel dataset={psi} datasets={ds} onCreated={(d) => created.push(d)} />);
    expect(screen.queryByLabelText("Bin by a third map (optional)")).toBeNull();                // nothing chosen yet
    await userEvent.selectOptions(screen.getByLabelText("Compare orientation with"), "2");
    await screen.findByLabelText("Preset of default values");
    const opts = Array.from((screen.getByLabelText("Bin by a third map (optional)") as HTMLSelectElement).options).map((o) => o.text);
    expect(opts).toEqual(["None: compare the whole map", "nhi.fits"]);                        // not an orientation map, not on a host, not archived

    await userEvent.click(screen.getByRole("button", { name: "Compute the alignment measure" }));
    await waitFor(() => expect(created).toHaveLength(1));
    const posts = () => calls.filter((c) => c.url.startsWith("/api/analysis/relative-orientation"));
    const plain = JSON.parse(String(posts()[0]!.init!.body));
    expect(plain.stratifyId).toBeUndefined();
    expect(plain.params.strataBins).toBeUndefined();
    expect(plain.params.strataEdges).toBeUndefined();

    await userEvent.selectOptions(screen.getByLabelText("Bin by a third map (optional)"), "3");
    expect(screen.getByLabelText("Number of bins")).toHaveValue("5");
    await userEvent.click(screen.getByRole("button", { name: "Compute the alignment measure" }));
    await waitFor(() => expect(created).toHaveLength(2));
    expect(JSON.parse(String(posts()[1]!.init!.body))).toMatchObject({ stratifyId: 3, params: { strataBins: 5 } });

    await userEvent.selectOptions(screen.getByLabelText("Bins"), "edges");
    expect(screen.getByRole("button", { name: "Compute the alignment measure" })).toBeDisabled();   // no edges yet: never a silent default
    await userEvent.type(screen.getByLabelText("Bin edges (third map's unit)"), "1e20, 5e20, 1e21");
    await userEvent.click(screen.getByRole("button", { name: "Compute the alignment measure" }));
    await waitFor(() => expect(created).toHaveLength(3));
    const edged = JSON.parse(String(posts()[2]!.init!.body));
    expect(edged.stratifyId).toBe(3);
    expect(edged.params.strataEdges).toEqual([1e20, 5e20, 1e21]);
    expect(edged.params.strataBins).toBeUndefined();
  });
});
