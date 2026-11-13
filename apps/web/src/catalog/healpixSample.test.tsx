import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset, HealpixDataset, Header, ImageDataset } from "../api/catalog";
import type { HealpixSampleResult } from "../api/analysis";
import { HealpixSampleDone, HealpixSamplePanel, mapReason, targetReason } from "./HealpixSample";
import { withFacts } from "./testDatasets";

const axis = (n: number, ctype: string) => ({ axis: n, length: 10, ctype });
const image = (id: number, name: string, ctypes: string[], hostId = 0): ImageDataset =>
  withFacts({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId, header: { axes: ctypes.map((c, i) => axis(i + 1, c)) } as unknown as Header });
const planck = (over: Record<string, unknown> = {}, hostId = 0): HealpixDataset =>
  withFacts({ id: 1, name: "planck353.fits", path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId, kind: "healpix", catalogDeclaration: null,
     header: { kind: "healpix", nside: 2048, npix: 50331648, ordering: "NESTED", coordsys: "GALACTIC", frame: "galactic", polcconv: "COSMO", columns: [], stokes_unit: "K_CMB", covariance_unit: null, has_qu_cov: true, has_i: true, bad_data: null, pixel_resolution_arcmin: 1.7, provenance_note: "", ...over } }) as unknown as HealpixDataset;

const lmc = image(2, "lmc.fits", ["RA---SIN", "DEC--SIN", "VRAD"]);
const datasets: Dataset[] = [planck(), lmc, image(3, "pixels.fits", ["X", "Y"]), image(4, "remote.fits", ["RA---TAN", "DEC--TAN"], 5), image(5, "gal.fits", ["GLON-CAR", "GLAT-CAR"])];

function mockFetch(status: number, body: unknown) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

const result = (): HealpixSampleResult => ({
  dataset: image(9, "healpix-b_field-1-2.fits", ["RA---SIN", "DEC--SIN"]), p: image(10, "p.fits", ["RA---SIN", "DEC--SIN"]), sigmaP: image(11, "sp.fits", ["RA---SIN", "DEC--SIN"]),
  summary: { product: "b_field", derivation: { sampling: "Nearest pixel.", noise: "P is not debiased." }, policy: "Dust polarization turned by 90 degrees." } as unknown as HealpixSampleResult["summary"],
});

describe("HEALPix sampling form", () => {
  it("states the map's header and lists unusable targets with the reason", () => {
    render(<HealpixSamplePanel dataset={planck({ has_qu_cov: false })} datasets={datasets} onResult={() => {}} />);
    const facts = screen.getByLabelText("What this HEALPix map is");
    expect(facts).toHaveTextContent(/NSIDE2048/);
    expect(facts).toHaveTextContent(/NESTED/);
    expect(facts).toHaveTextContent(/GALACTIC/);
    expect(facts).toHaveTextContent(/COSMO: U has the opposite sign to IAU/);
    expect(facts).toHaveTextContent(/absent: the Q–U covariance is taken as 0/);
    const opts = Array.from((screen.getByLabelText("Sample onto") as HTMLSelectElement).options).map((o) => [o.text, o.disabled]);
    expect(opts).toEqual([["Choose a map with a celestial grid…", false], ["lmc.fits", false], ["pixels.fits (unavailable)", true], ["remote.fits (unavailable)", true], ["gal.fits", false]]);
    const list = screen.getByLabelText("Datasets that cannot receive this map");
    expect(list).toHaveTextContent(/pixels\.fits: its first two axes are not a celestial pair/);
    expect(list).toHaveTextContent(/remote\.fits: it lives on a host/);
    expect(screen.getByText(/P is not debiased\. Correlated noise between HEALPix pixels/)).toBeInTheDocument();
  });

  it("refuses a non-Galactic or remote map before offering anything", () => {
    expect(mapReason(planck({ frame: "ecliptic", coordsys: "E" }))).toMatch(/ecliptic system.*only Galactic/);
    expect(mapReason(planck({}, 3))).toMatch(/lives on a host/);
    expect(targetReason(lmc)).toBeNull();
    render(<HealpixSamplePanel dataset={planck({ frame: "ecliptic", coordsys: "E" })} datasets={datasets} onResult={() => {}} />);
    expect(screen.queryByLabelText("Sample onto")).toBeNull();
  });

  it("sends the chosen product, method and cut, and hands the result over", async () => {
    const got: HealpixSampleResult[] = [];
    const calls = mockFetch(201, result());
    render(<HealpixSamplePanel dataset={planck()} datasets={datasets} onResult={(r) => got.push(r)} />);
    const go = screen.getByRole("button", { name: "Sample onto the map" });
    expect(go).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Sample onto"), "2");
    expect(screen.getByText(/dust polarization angle plus 90°/)).toBeInTheDocument();   // the default product is the field orientation
    await userEvent.selectOptions(screen.getByLabelText("Product"), "dust_polarization");
    await userEvent.selectOptions(screen.getByLabelText("Method"), "gaussian");
    expect(go).toBeDisabled();   // a Gaussian needs its FWHM
    await userEvent.type(screen.getByLabelText("Gaussian FWHM (arcmin)"), "5");
    await userEvent.type(screen.getByLabelText(/Blank the angle where P\/σ_P is below/), "3");
    await userEvent.click(go);
    await waitFor(() => expect(got).toHaveLength(1));
    expect(JSON.parse(String(calls[0]!.init!.body))).toEqual({ datasetId: 1, targetId: 2, product: "dust_polarization", method: "gaussian", fwhmArcmin: 5, snrMin: 3 });
  });

  it("shows the server's refusal as it is and opens nothing", async () => {
    const got: HealpixSampleResult[] = [];
    mockFetch(422, { error: "the target has no celestial WCS: its axes are not a sky pair" });
    render(<HealpixSamplePanel dataset={planck()} datasets={datasets} onResult={(r) => got.push(r)} />);
    await userEvent.selectOptions(screen.getByLabelText("Sample onto"), "5");
    await userEvent.click(screen.getByRole("button", { name: "Sample onto the map" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the target has no celestial WCS: its axes are not a sky pair");
    expect(got).toHaveLength(0);
  });

  it("shows the section and links the companions from the map's own record, with no prior state", async () => {
    const opened: number[] = [];
    const r = result();
    mockFetch(200, { datasetId: 9, op: "healpix_sample", params: { companions: { p: 10, sigmaP: 11 } }, sources: [1, 2], summary: r.summary, software: {}, createdAt: "" });
    render(<HealpixSampleDone dataset={r.dataset} datasets={[r.dataset, r.p!, r.sigmaP!]} onOpen={(d) => opened.push(d.id)} />);
    await userEvent.click(await screen.findByRole("button", { name: "Open P" }));
    await userEvent.click(screen.getByRole("button", { name: "Open σ_P" }));
    expect(opened).toEqual([10, 11]);
    expect(screen.getByText("Dust polarization turned by 90 degrees.")).toBeInTheDocument();
  });

  it("says when a map sampled earlier has no recorded companions, and stays out of other maps", async () => {
    const r = result();
    mockFetch(200, { datasetId: 9, op: "healpix_sample", params: {}, sources: [1, 2], summary: r.summary, software: {}, createdAt: "" });
    const { unmount } = render(<HealpixSampleDone dataset={r.dataset} datasets={[r.dataset]} onOpen={() => {}} />);
    expect(await screen.findByText("P: not recorded for this map.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open P" })).toBeNull();
    unmount();
    mockFetch(200, { datasetId: 9, op: "align", params: {}, sources: [], summary: {}, software: {}, createdAt: "" });
    render(<HealpixSampleDone dataset={r.dataset} datasets={[r.dataset]} onOpen={() => {}} />);
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled());
    expect(screen.queryByRole("region", { name: "Sampled from a HEALPix map" })).toBeNull();
  });
});
