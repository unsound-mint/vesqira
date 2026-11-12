import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CatalogDataset, CatalogDeclaration, Dataset, Header, ImageDataset, Interpretation } from "../api/catalog";
import type { RelOrientSummary } from "../api/analysis";
import { notReady, RelativeOrientationPanel, RelativeOrientationResult, RelOrientView } from "./RelativeOrientation";

const interp = (over: Partial<Interpretation> = {}): Interpretation =>
  ({ kind: "axial_orientation", unit: "deg", reliabilityId: null, frame: "pixel", quantity: "b_field", declaredBy: "user", declaredAt: "", stale: false, ...over }) as Interpretation;
const map = (id: number, name: string, i: Interpretation | null = interp(), hostId = 0): ImageDataset =>
  ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: i, nature: "", unitDeclaration: null, hostId, header: {} as Header });
const decl = (over: Partial<CatalogDeclaration> = {}): CatalogDeclaration =>
  ({ lon: "ra", lat: "dec", format: "degrees", frame: "icrs", equinox: null, values: [], declaredBy: "user", declaredAt: "", stale: false,
     angle: { column: "pa", unit: "deg", frame: "sky", quantity: "starlight_polarization", sigmaColumn: null }, ...over }) as CatalogDeclaration;
const cat = (id: number, name: string, d: CatalogDeclaration | null = decl()): CatalogDataset =>
  ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId: 0, kind: "catalog",
     header: { kind: "catalog", format: "csv", rows: 3, columns: [] }, catalogDeclaration: d }) as CatalogDataset;

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

const catalogSummary: RelOrientSummary = {
  label: "agreement statistic", mode: "map_catalog", am: 0.412, am_weighted: 0.45, median_abs_dpsi_deg: 24.1,
  parameters: { radius_arcmin: 10, min_pixels: 5, n_shifts: 3000, min_shift_deg: 1, keep_fraction: 0.9, seed: 20261020, bins: 9 },
  definitions: { am: "alignment measure AM = mean of cos 2Δψ" }, conversion: "Both sides as B-field orientation.", sampling: "disk", weighting: { disk: "unweighted", sources: "none" },
  map: { nx: 40, ny: 30 }, counts: { rows: 5, used: 3, share_used: 0.6, excluded: { "outside the map": 1, "the disk crosses the map edge": 1 } },
  bootstrap_am_16_84: [0.3, 0.5],
  histogram: { edges_deg: [0, 30, 60, 90], counts: [2, 1, 0], fractions: [2 / 3, 1 / 3, 0] },
  null: { method: "shifted source pattern", skipped: null, kept: 2950, attempted: 3000, n_sub: 2, chance95: 0.31, p_one_sided: 0.012, wrapped_below_min_shift: 4,
          histogram_band: { lo: [0.2, 0.2, 0.1], median: [0.33, 0.33, 0.33], hi: [0.5, 0.5, 0.5] } },
  segments: { x: [5, 20, 39], y: [5, 15, 29], map_pix_deg: [10, 80, null], other_pix_deg: [12, 70, 30], used: [true, true, false], truncated: false },
  table: { file: "relorient-1-2-0a0b0c0d.csv", rows: 5 },
  policy: "Agreement statistic between two tracers, not a validation of either.",
};

describe("relative orientation", () => {
  it("says what must be declared before a comparison, naming each missing piece", () => {
    expect(notReady(map(1, "a", null))).toMatch(/not declared as an orientation map/);
    expect(notReady(map(1, "a", interp({ frame: null, quantity: "unspecified" })))).toBe("the angle's frame and what the angle measures are unresolved: the convention must be declared first (the file does not state them, and none is assumed)");
    expect(notReady(map(1, "a", interp(), 3))).toMatch(/lives on a host.*download it with Transfers/);
    expect(notReady(cat(2, "c", null))).toMatch(/columns are not declared/);
    expect(notReady(cat(2, "c", decl({ angle: null })))).toMatch(/no angle column/);
    expect(notReady(cat(2, "c", decl({ stale: true })))).toMatch(/changed/);
    expect(notReady(map(1, "a"))).toBeNull();
    expect(notReady(cat(2, "c"))).toBeNull();
  });

  it("offers catalogs with an angle and other orientation maps, and sends the parameters with the seed", async () => {
    const created: Dataset[] = [];
    const calls = mockFetch({ "/api/analysis/relative-orientation": () => ({ status: 201, body: { dataset: cat(9, "relorient-1-2.csv"), summary: catalogSummary } }) });
    const psi = map(1, "psi.fits");
    const ds: Dataset[] = [psi, cat(2, "stars.csv"), cat(3, "plain.csv", decl({ angle: null })), map(4, "planck.fits"), map(5, "nhi.fits", null)];
    render(<RelativeOrientationPanel dataset={psi} datasets={ds} onCreated={(d) => created.push(d)} />);
    const opts = Array.from((screen.getByLabelText("Compare orientation with") as HTMLSelectElement).options).map((o) => o.text);
    expect(opts).toEqual(["Choose a catalog or map…", "stars.csv (catalog)", "planck.fits"]);
    await userEvent.selectOptions(screen.getByLabelText("Compare orientation with"), "2");
    expect(screen.getByLabelText("Disk radius (arcmin)")).toHaveValue("10");
    expect(screen.getByLabelText("Shifts in the null")).toHaveValue("3000");
    await userEvent.clear(screen.getByLabelText("Disk radius (arcmin)"));
    expect(screen.getByRole("button", { name: "Compute the alignment measure" })).toBeDisabled();   // empty is never a silent default
    expect(screen.getByText(/one is empty or not a number/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Disk radius (arcmin)"), "6.7");
    await userEvent.click(screen.getByRole("button", { name: "Compute the alignment measure" }));
    await waitFor(() => expect(created).toHaveLength(1));
    expect(JSON.parse(String(calls[0]!.init!.body))).toEqual({ datasetId: 1, otherId: 2, params: { nShifts: 3000, minShiftDeg: 1, keepFraction: 0.9, seed: 20261020, bins: 9, radiusArcmin: 6.7, minPixels: 5 } });
    // two maps: no radius, the maps' own default number of shifts
    await userEvent.selectOptions(screen.getByLabelText("Compare orientation with"), "4");
    expect(screen.queryByLabelText("Disk radius (arcmin)")).toBeNull();
    expect(screen.getByLabelText("Shifts in the null")).toHaveValue("1000");
  });

  it("offers the P/σP filter off by default, proposes the column pair, and sends it only when chosen", async () => {
    const calls = mockFetch({ "/api/analysis/relative-orientation": () => ({ status: 201, body: { dataset: cat(9, "r.csv"), summary: catalogSummary } }) });
    const psi = map(1, "psi.fits");
    const stars = cat(2, "stars.csv", decl({ values: ["Pcor", "sPcor", "P(%)", "sP(%)", "dist"] }));
    render(<RelativeOrientationPanel dataset={psi} datasets={[psi, stars]} onCreated={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Compare orientation with"), "2");
    expect(screen.getByLabelText("Filter rows: column P")).toHaveValue("");
    expect(screen.getByText(/Proposal, from the column names only: P = P\(%\), σP = sP\(%\)/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Filter rows: column P"), "Pcor");
    expect(screen.getByRole("button", { name: "Compute the alignment measure" })).toBeDisabled();   // half a filter is refused, not guessed
    await userEvent.click(screen.getByRole("button", { name: "Use this proposal" }));
    expect(screen.getByLabelText("Keep rows with P/σP ≥")).toHaveValue("3");
    await userEvent.click(screen.getByRole("button", { name: "Compute the alignment measure" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(JSON.parse(String(calls[0]!.init!.body)).params).toMatchObject({ snrColumn: "P(%)", snrSigmaColumn: "sP(%)", snrMin: 3 });
  });

  it("from a catalog it offers maps, and puts the map first in the request; a refusal shows its reasons", async () => {
    const calls = mockFetch({ "/api/analysis/relative-orientation": () => ({ status: 409, body: { error: "the maps are not known to share a grid", reasons: ["different CRVAL1"] } }) });
    const stars = cat(2, "stars.csv");
    render(<RelativeOrientationPanel dataset={stars} datasets={[stars, map(1, "psi.fits"), cat(3, "other.csv")]} onCreated={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Compare orientation with"), "1");
    await userEvent.click(screen.getByRole("button", { name: "Compute the alignment measure" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("different CRVAL1");
    expect(JSON.parse(String(calls[0]!.init!.body))).toMatchObject({ datasetId: 1, otherId: 2 });
  });

  it("shows the agreement statistic with its chance level, exclusions, figures and table, worded neutrally", () => {
    const { container } = render(<RelOrientView s={catalogSummary} names={["psi.fits", "stars.csv"]} tableUrl="/api/datasets/9/relative-orientation.csv" />);
    const region = screen.getByRole("region", { name: "Relative orientation result" });
    expect(within(region).getByText(/Agreement statistic · psi.fits against stars.csv/)).toBeInTheDocument();
    expect(within(region).getByText("0.412")).toBeInTheDocument();
    expect(within(region).getByText("0.310")).toBeInTheDocument();
    expect(within(region).getByText(/0.0120 from 2950 kept of 3000 shifts/)).toBeInTheDocument();
    expect(within(region).getByText("3 of 5 (60.0%)")).toBeInTheDocument();
    expect(within(region).getByLabelText("Excluded samples")).toHaveTextContent("1 outside the map");
    expect(within(region).getByRole("img", { name: /Histogram of \|Δψ\| in 3 equal-width bins with the null's 95% band/ })).toBeInTheDocument();
    expect(within(region).getByRole("img", { name: /Orientations at 3 sources/ })).toBeInTheDocument();
    expect(within(region).getByRole("link", { name: /per-source table \(CSV\)/ })).toHaveAttribute("href", "/api/datasets/9/relative-orientation.csv");
    expect(container.textContent).not.toMatch(/validated|success|correct|wrong/i);
    // the map segment of a source whose disk was not used is not drawn; the catalog's still is, faded
    expect(container.querySelectorAll("line[class*=segMap]")).toHaveLength(2);
    expect(container.querySelectorAll("line[class*=segOther]")).toHaveLength(3);
  });

  it("a map comparison shows V without a p-value and says why, and the beam count only as an approximation", () => {
    const s: RelOrientSummary = {
      ...catalogSummary, mode: "map_map", segments: undefined, bootstrap_am_16_84: undefined,
      counts: { pixels: 1000, used: 900, share_used: 0.9, excluded: {} },
      prs: { V: 41.3, V_weighted: null, n: 900, note: "no p-value is given for it" },
      independent_samples: { value: null, reason: "the first map declares no beam (BMAJ and BMIN), so no count of independent beams is offered" },
      null: { ...catalogSummary.null, skipped: "no shifts were asked for", histogram_band: null },
      table: { file: null, rows: 900, reason: "too many rows" },
    };
    render(<RelOrientView s={s} names={["a.fits", "b.fits"]} tableUrl={null} />);
    expect(screen.getByText(/41.3, n = 900/)).toBeInTheDocument();
    expect(screen.getByText("no p-value is given for it")).toBeInTheDocument();
    expect(screen.getByText(/not offered: the first map declares no beam/)).toBeInTheDocument();
    expect(screen.getByText(/not computed: no shifts were asked for/)).toBeInTheDocument();
    expect(screen.getByText("No table: too many rows.")).toBeInTheDocument();
    expect(screen.getByText(/No null was run/)).toBeInTheDocument();
  });

  it("the result section appears only for a relative-orientation result", async () => {
    mockFetch({ "/api/datasets/9/derived": () => ({ body: { datasetId: 9, op: "relorient_catalog", params: {}, sources: [1, 2], summary: catalogSummary, software: {}, createdAt: "" } }),
                "/api/datasets/8/derived": () => ({ body: { datasetId: 8, op: "moment", params: {}, sources: [1], summary: {}, software: {}, createdAt: "" } }) });
    const { rerender } = render(<RelativeOrientationResult dataset={cat(9, "r.csv")} datasets={[map(1, "psi.fits"), cat(2, "stars.csv")]} />);
    expect(await screen.findByText(/psi.fits against stars.csv/)).toBeInTheDocument();
    rerender(<RelativeOrientationResult dataset={map(8, "m.fits")} datasets={[]} />);
    await waitFor(() => expect(screen.queryByText(/Agreement statistic/)).toBeNull());
  });
});
