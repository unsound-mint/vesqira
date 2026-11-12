import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset, Header, ImageDataset } from "../api/catalog";
import { FamilyView } from "./FamilyView";

vi.mock("../api/planes", () => ({
  getPlane: vi.fn(async () => ({
    head: { width: 2, height: 2, channel: 0, channels: 1, stats: { finite_count: 3, nonfinite_count: 1, min: 0, max: 3, p01: 0, p99: 3, overflow_count: 0 } },
    data: new Float32Array([0, 1, 2, NaN]),
  })),
}));

const ds = (id: number, name: string, extra: Partial<ImageDataset> = {}): ImageDataset => ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId: 0, header: { naxis: 2 } as Header, ...extra });
const maps = [ds(1, "a.fits"), ds(2, "b.fits"), ds(3, "c.fits")];

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const keys = Object.keys(routes).filter((k) => url.startsWith(k)).sort((a, b) => b.length - a.length);
    const r = keys.length ? routes[keys[0]!]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

const summary = {
  axial: false, labels: ["a.fits", "b.fits"], reference: 0, spread: "std", unit: "K", shape: [2, 2], comparison_maps: [1], warnings: [], policy: "Differences are member minus reference over pixels both maps have.",
  members: [{ index: 0, label: "a.fits", reference: true }, { index: 1, label: "b.fits", reference: false, pixels_compared: 3, mean: 0.5, median: 0.4, rms: 0.6, min: 0, max: 1 }],
  pairwise_rms: [[0, 0.6], [0.6, 0]],
};

describe("family view", () => {
  it("offers a recognised family and fills the selection from it", async () => {
    mockFetch({ "/api/analysis/families": () => ({ body: { families: [{ op: "smooth_spectral", sourceId: 1, varying: "width", fixed: { kernel: "boxcar" }, members: [{ datasetId: 2, value: "3" }, { datasetId: 3, value: "5" }] }] } }) });
    render(<FamilyView datasets={maps} preset={null} />);
    await userEvent.click(await screen.findByRole("button", { name: "Use these" }));
    expect(screen.getByLabelText("b.fits")).toBeChecked();
    expect(screen.getByLabelText("c.fits")).toBeChecked();
    expect(screen.getByLabelText("a.fits")).not.toBeChecked();
    expect(screen.getByRole("combobox", { name: "Compare each with" })).toHaveDisplayValue("width = 3");
  });

  it("sends the chosen maps, reference and spread, and states what was computed", async () => {
    const calls = mockFetch({
      "/api/analysis/families": () => ({ body: { families: [] } }),
      "/api/analysis/family": () => ({ status: 201, body: { summary, datasetIds: [1, 2], referenceId: 1, maps: [{ role: "difference", member: 1, dataset: ds(10, "diff.fits") }, { role: "spread", member: -1, dataset: ds(11, "spread.fits") }] } }),
    });
    render(<FamilyView datasets={maps} preset={null} />);
    await userEvent.click(screen.getByLabelText("a.fits"));
    await userEvent.click(screen.getByLabelText("b.fits"));
    await userEvent.selectOptions(screen.getByLabelText("Spread across the maps"), "mad");
    await userEvent.click(screen.getByRole("button", { name: "Compare" }));
    await waitFor(() => expect(screen.getByText(/member minus reference over pixels both maps have/)).toBeInTheDocument());
    const post = calls.find((c) => c.url === "/api/analysis/family")!;
    expect(JSON.parse(String(post.init!.body))).toEqual({ datasetIds: [1, 2], referenceId: 1, spread: "mad", labels: ["a.fits", "b.fits"] });
    const table = screen.getByRole("region", { name: "Agreement numbers" });
    expect(within(table).getByText("a.fits (reference)")).toBeInTheDocument();
    expect(within(table).getByText("0.5")).toBeInTheDocument();
  });

  it("shows a refusal as the server's sentence and no result", async () => {
    mockFetch({
      "/api/analysis/families": () => ({ body: { families: [] } }),
      "/api/analysis/family": () => ({ status: 409, body: { error: "These maps are not known to share a grid, and nothing is resampled." } }),
    });
    render(<FamilyView datasets={maps} preset={null} />);
    await userEvent.click(screen.getByLabelText("a.fits"));
    await userEvent.click(screen.getByLabelText("b.fits"));
    await userEvent.click(screen.getByRole("button", { name: "Compare" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("nothing is resampled");
    expect(screen.queryByRole("region", { name: "Agreement numbers" })).toBeNull();
  });

  it("needs two maps and a channel for a cube", async () => {
    mockFetch({ "/api/analysis/families": () => ({ body: { families: [] } }) });
    const cube = ds(4, "cube.fits", { header: { naxis: 3 } as Header });
    render(<FamilyView datasets={[maps[0]!, cube] as Dataset[]} preset={null} />);
    await userEvent.click(screen.getByLabelText("cube.fits"));
    expect(screen.getByRole("button", { name: "Compare" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText("a.fits"));
    expect(screen.getByRole("button", { name: "Compare" })).toBeEnabled();
    await userEvent.clear(screen.getByLabelText(/Channel \(1-based/));
    expect(screen.getByRole("button", { name: "Compare" })).toBeDisabled();
  });
});
