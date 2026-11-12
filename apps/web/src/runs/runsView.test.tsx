import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Run } from "../api/runs";
import type { Job } from "../api/jobs";
import { metric, run } from "../catalog/runFixtures";
import { RunsView } from "./RunsView";
import { initialRunsView } from "./runsViewState";
import type { RunsViewState } from "./runsViewState";

const api = vi.hoisted(() => ({ listRunViews: vi.fn(), saveRunView: vi.fn(), deleteRunView: vi.fn() }));
vi.mock("../api/runViews", () => api);

const def = "Axial agreement with Planck";
const am = (v: number | null, over = {}) => metric("am", v, { definition: def, ...over });
const RUNS: Run[] = [
  run(1, { block_size: "8", gradient_method: "central" }, { metrics: [am(0.2, { uncertainty: 0.02, uncertaintyKind: "standard_error" })] }),
  run(2, { block_size: "16", gradient_method: "sobel" }, { metrics: [am(0.4, { uncertainty: 0.02, uncertaintyKind: "standard_error" })] }),
  run(3, { block_size: "32", gradient_method: "central" }, { metrics: [am(0.6, { status: "inferred" as never })] }),
  run(4, { block_size: "64", gradient_method: "sobel" }), // records no result at all
  run(5, { block_size: "128", gradient_method: "central" }, { metrics: [am(30, { unit: "deg" })] }), // another unit
];
const JOBS = [{ id: 7, hostId: 1, state: "completed", runId: 2 }] as unknown as Job[];

let onOpen: ReturnType<typeof vi.fn<(id: number) => void>>;
function Page({ runs = RUNS, initial = initialRunsView }: { runs?: Run[]; initial?: RunsViewState }) {
  const [state, setState] = useState(initial);
  return <RunsView runs={runs} jobs={JOBS} hostName={(id) => (id === 1 ? "cluster" : undefined)} state={state} onState={(p) => setState((s) => ({ ...s, ...p }))} onOpenRun={onOpen} />;
}

const rowNames = () => within(screen.getByRole("table", { name: /one row per run/i })).getAllByRole("row").slice(1).map((r) => within(r).getAllByRole("rowheader")[0]?.querySelector("button")?.textContent);

beforeEach(() => {
  onOpen = vi.fn<(id: number) => void>();
  api.listRunViews.mockResolvedValue({ views: [], max: 100 });
});
afterEach(() => vi.clearAllMocks());

describe("runs table", () => {
  it("lists every run newest import first, with status and host from the job, and says what a run did not record", async () => {
    render(<Page />);
    expect(rowNames()).toEqual(["lmc_5", "lmc_4", "lmc_3", "lmc_2", "lmc_1"]);
    const row2 = screen.getByRole("button", { name: "lmc_2" }).closest("tr")!;
    expect(within(row2).getByText("completed")).toBeTruthy();
    expect(within(row2).getByText("cluster")).toBeTruthy();
    const row1 = screen.getByRole("button", { name: "lmc_1" }).closest("tr")!;
    expect(within(row1).getAllByText("not recorded").length).toBeGreaterThanOrEqual(2); // status and host of an imported run
  });

  it("exposes a parameter or a result as a column, sorts on it and puts runs without a value last in both directions", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await u.click(screen.getByRole("button", { name: "Columns" }));
    await u.click(screen.getByRole("checkbox", { name: /^block_size/ }));
    await u.click(screen.getByRole("checkbox", { name: /^am/ }));
    const header = screen.getByRole("columnheader", { name: /^am/ });
    expect(header).toHaveAttribute("aria-sort", "none");
    await u.click(within(header).getByRole("button"));
    expect(header).toHaveAttribute("aria-sort", "ascending");
    expect(rowNames().slice(0, 3)).toEqual(["lmc_1", "lmc_2", "lmc_3"]); // by unit first (unitless, then deg), then by value: 30 deg is not 30 on their scale
    expect(rowNames().at(-2)).toBe("lmc_5");
    expect(rowNames().at(-1)).toBe("lmc_4"); // no result: last
    await u.click(within(header).getByRole("button"));
    expect(header).toHaveAttribute("aria-sort", "descending");
    expect(rowNames().at(-1)).toBe("lmc_4");
    const missing = screen.getByRole("button", { name: "lmc_4" }).closest("tr")!;
    expect(within(missing).getAllByText("not recorded").length).toBeGreaterThan(0);
    const bs = screen.getByRole("columnheader", { name: /^block_size/ });
    await u.click(within(bs).getByRole("button"));
    await u.click(within(bs).getByRole("button"));
    expect(rowNames()).toEqual(["lmc_5", "lmc_4", "lmc_3", "lmc_2", "lmc_1"]); // 128 before 64 before 32: numbers, not text
  });

  it("filters with the shared query, keeps the runs that did not record the asked-about result apart and shows them last on request", async () => {
    const u = userEvent.setup();
    render(<Page />);
    const box = screen.getByRole("combobox", { name: "Filter runs" });
    await u.type(box, "am>0.3 ");
    expect(rowNames()).toEqual(["lmc_5", "lmc_3", "lmc_2"]); // no unit in the word: the number as recorded, 30 deg included
    expect(screen.getAllByRole("status").some((s) => /1 run left out: it did not record am>0\.3/.test(s.textContent ?? ""))).toBe(true);
    // a column for what the query asks about is shown, so the reason for the order is visible
    expect(screen.getByRole("columnheader", { name: /^am/ })).toBeTruthy();
    await u.click(screen.getByRole("button", { name: "Show them, last" }));
    expect(rowNames()).toEqual(["lmc_5", "lmc_3", "lmc_2", "lmc_4"]);
    const row = screen.getByRole("button", { name: "lmc_4" }).closest("tr")!;
    expect(within(row).getByText("no am>0.3")).toBeTruthy();
    expect(within(row).getAllByText("not recorded").length).toBeGreaterThan(0);
  });

  it("opens a run with one click on its name", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await u.click(screen.getByRole("button", { name: "lmc_3" }));
    expect(onOpen).toHaveBeenCalledWith(3);
  });
});

describe("cross-run plot", () => {
  async function plotted(u: ReturnType<typeof userEvent.setup>, by = "gradient_method") {
    await u.selectOptions(screen.getByLabelText("Result (y)"), "am");
    await u.selectOptions(screen.getByLabelText("Against (x)"), "param:block_size");
    if (by) await u.selectOptions(screen.getByLabelText("Colour and shape by"), by);
  }

  it("draws the runs that record both values, puts results in another unit on their own labelled panel, and lists the runs it left out", async () => {
    const u = userEvent.setup();
    render(<Page />);
    expect(screen.getByText(/Choose a result and what to draw it against/)).toBeTruthy();
    await plotted(u);
    const svg = screen.getByTestId("runs-plot");
    expect(svg.querySelectorAll('[data-testid="plot-point"]')).toHaveLength(4); // lmc_4 records no result
    expect(svg.querySelectorAll("[data-panel]")).toHaveLength(2);
    expect(within(svg as unknown as HTMLElement).getByText(/not directly comparable: unit differs: deg here, none stated in the main panel/)).toBeTruthy();
    expect(within(svg as unknown as HTMLElement).getByText("am (deg)")).toBeTruthy();
    await u.click(screen.getByText(/1 of 5 runs in the table are not drawn/));
    expect(screen.getByText("lmc_4: am not recorded")).toBeTruthy();
  });

  it("labels the kind of uncertainty, and tells filled from hollow markers in the legend", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await plotted(u);
    const svg = screen.getByTestId("runs-plot");
    expect(within(svg as unknown as HTMLElement).getByText("± standard error")).toBeTruthy();
    expect(within(svg as unknown as HTMLElement).getByText("filled: stated by the run")).toBeTruthy();
    expect(within(svg as unknown as HTMLElement).getByText("hollow: inferred by a rule")).toBeTruthy();
    expect(svg.querySelectorAll('[data-inferred="true"]')).toHaveLength(1);
    // each category has a marker of its own beside its colour: the legend lists them with the value
    expect(within(svg as unknown as HTMLElement).getByText("central")).toBeTruthy();
    expect(within(svg as unknown as HTMLElement).getByText("sobel")).toBeTruthy();
  });

  it("draws only the filtered runs", async () => {
    const u = userEvent.setup();
    render(<Page initial={{ ...initialRunsView, query: "block_size:16..64" }} />);
    await plotted(u, "");
    expect(screen.getByTestId("runs-plot").querySelectorAll('[data-testid="plot-point"]')).toHaveLength(2);
  });

  it("selecting a point selects its row and the reverse, and a selected run opens from the plot", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await plotted(u);
    const point = screen.getByTestId("runs-plot").querySelector('[data-run="2"]')!;
    await u.click(point);
    const row = screen.getByRole("button", { name: "lmc_2" }).closest("tr")!;
    expect(row).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Selected: lmc_2")).toBeTruthy();
    await u.click(screen.getByRole("button", { name: "Select lmc_3" }));
    expect(screen.getByTestId("runs-plot").querySelector('[data-run="3"]')).toHaveAttribute("data-selected", "true");
    expect(screen.getByTestId("runs-plot").querySelector('[data-run="2"]')).not.toHaveAttribute("data-selected");
    await u.click(screen.getByRole("button", { name: "Open run" }));
    expect(onOpen).toHaveBeenCalledWith(3);
  });

  it("moves between points with the arrow keys and opens the selected run with Enter", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await plotted(u);
    const svg = screen.getByTestId("runs-plot");
    svg.focus();
    await u.keyboard("{ArrowRight}");
    const first = svg.querySelector('[data-selected="true"]')!;
    expect(first.getAttribute("data-run")).toBe("1"); // smallest x of the first panel
    await u.keyboard("{ArrowRight}{ArrowRight}");
    expect(svg.querySelector('[data-selected="true"]')!.getAttribute("data-run")).toBe("3");
    await u.keyboard("{Enter}");
    expect(onOpen).toHaveBeenCalledWith(3);
    await u.keyboard("{Escape}");
    expect(svg.querySelector('[data-selected="true"]')).toBeNull();
  });

  it("says on the main panel too when the results state no definition, so they are not read as comparable", async () => {
    const u = userEvent.setup();
    const bare = [run(1, { block_size: "8" }, { metrics: [metric("am", 0.2)] }), run(2, { block_size: "16" }, { metrics: [metric("am", 0.3)] })];
    render(<Page runs={bare} />);
    await plotted(u, "");
    expect(within(screen.getByTestId("runs-plot") as unknown as HTMLElement).getByText(/definition not stated: not shown as comparable/)).toBeTruthy();
  });

  it("never calls a run best or ranks the runs", async () => {
    const u = userEvent.setup();
    render(<Page />);
    await plotted(u);
    expect(document.body.textContent).not.toMatch(/\bbest\b|\bbetter\b|\brank/i);
  });
});

describe("exports", () => {
  let blobs: Blob[];
  beforeEach(() => {
    blobs = [];
    URL.createObjectURL = vi.fn((b: Blob | MediaSource) => { blobs.push(b as Blob); return "blob:x"; });
    URL.revokeObjectURL = vi.fn();
    HTMLAnchorElement.prototype.click = vi.fn();
  });
  const text = (b: Blob) => new Promise<string>((res) => { const r = new FileReader(); r.onload = () => res(String(r.result)); r.readAsText(b); });

  it("exports the filtered table as CSV with the query in a header comment, units and uncertainty beside each result", async () => {
    const u = userEvent.setup();
    render(<Page initial={{ ...initialRunsView, query: "am>0.3", columns: ["status", "p:block_size", "m:am"] }} />);
    await u.click(screen.getByRole("button", { name: /Export table/ }));
    const csv = await text(blobs[0]!);
    expect(csv).toContain("# query: am>0.3");
    expect(csv).toContain("# runs: 3 of 5");
    expect(csv).toContain("id,name,status,block_size,am,am_unit,am_uncertainty,am_uncertainty_kind,am_n,am_status");
    expect(csv).toContain("2,lmc_2,completed,16,0.4,,0.02,standard_error,,declared");
    expect(csv).toContain("3,lmc_3,,32,0.6,,,,,inferred");
    expect(csv).not.toContain("lmc_4"); // it did not record am: not part of the matches
  });

  it("exports the plot as SVG with the query and the run ids in its provenance", async () => {
    const u = userEvent.setup();
    render(<Page initial={{ ...initialRunsView, query: "block_size:16..64", plot: { y: "am", x: "block_size", xKind: "param", by: "" } }} />);
    await u.click(screen.getByRole("button", { name: "Export plot (SVG)" }));
    const svg = await text(blobs[0]!);
    expect(svg).not.toContain("var(--"); // the page's tokens are resolved: the file stands alone
    expect(svg).toMatch(/<svg[^>]* width="720" height="\d+"/); // a size of its own, not the page's 100%
    const doc = new DOMParser().parseFromString(svg, "image/svg+xml");
    const prov = JSON.parse(doc.querySelector("metadata#vesqira-provenance")!.textContent!);
    expect(prov).toMatchObject({ tool: "Vesqira cross-run plot", query: "block_size:16..64", runIds: [2, 3], y: "am", x: "param:block_size" });
    expect(svg).toContain("query: block_size:16..64 · 2 runs"); // and says so on the figure itself
  });
});

describe("saved views", () => {
  it("applies a saved view's query, columns, sort and plot, and saves the current one under a name", async () => {
    api.listRunViews.mockResolvedValue({ views: [{ id: 3, name: "LMC sweep", updatedAt: "2026-11-01T00:00:00Z", spec: { query: "block_size:16..64", columns: ["status", "m:am"], sort: { column: "m:am", dir: -1 }, plot: { y: "am", x: "block_size", xKind: "param", by: "" } } }], max: 100 });
    api.saveRunView.mockImplementation(async (name: string, spec: unknown) => ({ id: 4, name, spec, updatedAt: "2026-11-02T00:00:00Z" }));
    const u = userEvent.setup();
    render(<Page />);
    await screen.findByRole("option", { name: "LMC sweep" });
    await u.selectOptions(screen.getByLabelText("Saved view"), "LMC sweep");
    expect(rowNames()).toEqual(["lmc_3", "lmc_2", "lmc_4"]); // block_size 16..64; lmc_4 records no am: last
    expect(screen.getByRole("columnheader", { name: /^am/ })).toHaveAttribute("aria-sort", "descending");
    expect(screen.getByLabelText("Result (y)")).toHaveValue("am");
    await u.click(screen.getByRole("button", { name: "Save view…" }));
    const nameBox = screen.getByLabelText("View name");
    await u.clear(nameBox);
    await u.type(nameBox, "Second");
    await u.click(screen.getByRole("button", { name: "Save" }));
    expect(api.saveRunView).toHaveBeenCalledWith("Second", expect.objectContaining({ query: "block_size:16..64", columns: ["status", "m:am"], sort: { column: "m:am", dir: -1 } }));
    expect(await screen.findByRole("option", { name: "Second" })).toBeTruthy();
  });

  it("says why a view could not be saved", async () => {
    api.saveRunView.mockRejectedValue(new Error("at most 100 views are kept; delete one first"));
    const u = userEvent.setup();
    render(<Page />);
    await u.click(screen.getByRole("button", { name: "Save view…" }));
    await u.type(screen.getByLabelText("View name"), "x");
    await u.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toContain("at most 100 views");
  });
});
