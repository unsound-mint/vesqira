import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BoardCompare } from "./BoardCompare";
import { BoardsView } from "./BoardsView";
import type { Board, Figure } from "../api/boards";
import type { Metric } from "../api/metrics";

vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), listBoards: vi.fn(), listSnapshots: vi.fn().mockResolvedValue([]) }));
import { listBoards } from "../api/boards";

const metric = (o: Partial<Metric>): Metric => ({ key: "alignment_measure", value: 0.344, unit: "", uncertainty: 0.02, uncertaintyKind: "standard_deviation", n: 2695, definition: "mean of cos 2θ", status: "declared", ...o });
const prov = (name: string | null, ker: string, extra: Partial<NonNullable<Figure["provenance"]["run"]>> = {}, channel = 3): Figure["provenance"] => ({
  source: { name: "lmc_hi_cube.fits", sizeBytes: 1000, modifiedAt: "", identity: "a reference by name, size and modification time" },
  view: { kind: "plane", channel },
  statements: [],
  ...(name ? { run: { name, kind: "vgt", role: "psi", gitCommit: "bcbaed4ec980ffff", dirty: true, reproducibleFromCommit: false, generatedAt: "", params: [{ key: "ker", value: ker }, { key: "dn", value: "10" }], metrics: [metric({})], ...extra } } : {}),
});
const fig = (id: number, title: string, p: Figure["provenance"]): Figure => ({ id, datasetId: id * 10, title, caption: "", createdAt: "", revision: `r${id}`, previewSha: "a".repeat(64), width: 4, height: 3, provenance: p });
const A = fig(1, "Ker 8", prov("vgt-ker8", "8"));
const B = fig(2, "Ker 12", prov("vgt-ker12", "12", { metrics: [metric({ value: 0.211, uncertainty: 0.03, n: 3120 })] }));

describe("what differs between two panels", () => {
  it("lists the differing setting, not the equal one, and both results with the basis", () => {
    render(<BoardCompare a={A} b={B} />);
    expect(screen.getByRole("heading", { name: "Compare · vgt-ker8 vs vgt-ker12" })).toBeInTheDocument();
    expect(screen.getByText("Settings that differ (1)")).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: "ker" })).toBeInTheDocument();
    expect(screen.queryByRole("rowheader", { name: "dn" })).toBeNull();
    const row = screen.getByRole("rowheader", { name: "alignment_measure" }).closest("tr") as HTMLElement;
    expect(within(row).getByText(/0\.344/)).toBeInTheDocument();
    expect(within(row).getByText(/0\.211/)).toBeInTheDocument();
    expect(within(row).getByText("same definition and unit")).toBeInTheDocument();
    expect(screen.getAllByText(/-dirty/)).toHaveLength(2);
    expect(screen.getAllByText(/not reproducible from the commit/)).toHaveLength(2);
    expect(screen.getByText(/Same file name and size.*not compared byte for byte/)).toBeInTheDocument();
    expect(screen.getByText(/did not compute or check these numbers/)).toBeInTheDocument();
  });

  it("says when no setting differs and when channels differ", () => {
    render(<BoardCompare a={A} b={fig(3, "Same", prov("vgt-ker8b", "8", {}, 5))} />);
    expect(screen.getByText("No recorded setting differs.")).toBeInTheDocument();
    expect(screen.getByText(/different channels: that is a difference in the view/)).toBeInTheDocument();
  });

  it("handles a figure without a run and falls back to its title", () => {
    render(<BoardCompare a={A} b={fig(4, "Plain map", prov(null, ""))} />);
    expect(screen.getByRole("heading", { name: "Compare · vgt-ker8 vs Plain map" })).toBeInTheDocument();
    expect(screen.getByText("No run is recorded for Plain map.")).toBeInTheDocument();
    expect(screen.queryByText(/Settings that differ/)).toBeNull();
  });

  it("offers the side-by-side button only when the shell provides it", async () => {
    const onCompare = vi.fn();
    const { rerender } = render(<BoardCompare a={A} b={B} />);
    expect(screen.queryByRole("button", { name: "Open both maps side by side" })).toBeNull();
    rerender(<BoardCompare a={A} b={B} onCompare={onCompare} />);
    await userEvent.click(screen.getByRole("button", { name: "Open both maps side by side" }));
    expect(onCompare).toHaveBeenCalledWith(10, 20);
  });
});

describe("ticking panels on the board", () => {
  it("shows the table for two ticks and a third tick replaces the oldest", async () => {
    const C = fig(3, "Ker 16", prov("vgt-ker16", "16"));
    const board: Board = { id: 1, title: "Kernels", createdAt: "", updatedAt: "", items: [A, B, C].map((f, i) => ({ position: i, figure: f, note: "" })) };
    vi.mocked(listBoards).mockResolvedValue([board]);
    render(<BoardsView />);
    await userEvent.click(await screen.findByRole("checkbox", { name: "Compare Ker 8" }));
    expect(screen.queryByRole("region", { name: "What differs" })).toBeNull();
    await userEvent.click(screen.getByRole("checkbox", { name: "Compare Ker 12" }));
    expect(screen.getByRole("heading", { name: "Compare · vgt-ker8 vs vgt-ker12" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Compare Ker 16" }));
    expect(screen.getByRole("heading", { name: "Compare · vgt-ker12 vs vgt-ker16" })).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Compare Ker 8" })).not.toBeChecked();
  });
});
