import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { SubsetDetail, SubsetView } from "../api/subsets";
import { Explorer } from "./Explorer";
import { SubsetForm } from "./SubsetForm";
import { SubsetPage } from "./SubsetPage";
import { SubsetsSection } from "./SubsetsSection";
import { describe as describeSubset, fieldsOf, inputOf } from "./subsets";

const api = vi.hoisted(() => ({
  createSubset: vi.fn(), editSubset: vi.fn(), repinSubset: vi.fn(), materialiseSubset: vi.fn(), deleteSubset: vi.fn(),
  getSubset: vi.fn(), getSubsetView: vi.fn(), getSubsetCoord: vi.fn(), listSubsets: vi.fn(),
}));
vi.mock("../api/subsets", () => api);
vi.mock("../api/regions", () => ({
  listSavedRegions: vi.fn().mockResolvedValue([
    { id: 4, datasetId: 7, name: "Peak", shape: { shape: "circle", cx: 20, cy: 9, r: 12 } },
    { id: 5, datasetId: 7, name: "Cut", shape: { shape: "line" } },
  ]),
}));

const dataset = (over: Partial<Dataset> = {}) => ({ id: 7, path: "/data/a.fits", name: "a.fits", sizeBytes: 10, modifiedAt: "", addedAt: "", hostId: 0, nature: "", interpretation: null, unitDeclaration: null, kind: "fits", header: { naxis: 3, axes: [] }, ...over }) as unknown as Dataset;

const def = { box: { x0: 3, x1: 20, y0: 2, y1: 15 }, step: 2, channels: { c0: 10, c1: 40 } };
const view = (over: Partial<SubsetView> = {}): SubsetView => ({
  id: 1, sourceId: 7, sourceName: "a.fits", name: "Core", reason: "the peak and its surroundings", version: 1, kind: "create", definition: def, author: "user",
  createdAt: "2026-11-12T10:00:00Z", editedAt: "2026-11-12T10:00:00Z", virtual: true, stale: false,
  pinned: { size: 1000, modifiedAt: "2026-11-12T09:00:00Z", identity: "sha256:0123456789abcdef0123456789abcdef" }, materialised: null, ...over,
});
const detail = (sub: SubsetView): SubsetDetail => ({ subset: sub, versions: [{ version: 1, kind: "create", name: "Core", reason: "the peak and its surroundings", definition: def, sourceSize: 1000, sourceModifiedAt: "", sourceIdentity: "", author: "user", at: "2026-11-12T10:00:00Z" }], materialised: [] });
const win = { x0: 3, x1: 19, y0: 2, y1: 14, step: 2, c0: 10, c1: 40, width: 9, height: 7, channels: 31, source: { width: 32, height: 24, channels: 64 }, channel_rule: "the channel range as stated", world: null };

beforeEach(() => Object.values(api).forEach((f) => f.mockReset()));

describe("the subset definition as the form states it", () => {
  it("counts pixels and channels from 1 and sends the API's 0-based window", () => {
    const f = { ...fieldsOf("Core", def), reason: "why" };
    expect(f).toMatchObject({ area: "box", x0: "4", x1: "21", y0: "3", y1: "16", step: "2", spectral: "channels", c0: "11", c1: "41" });
    expect(inputOf(f, 3)).toEqual({ input: { name: "Core", reason: "why", box: def.box, step: 2, channels: def.channels, expectVersion: 3 } });
    expect(describeSubset(def)).toBe("x 4 to 21, y 3 to 16; every 2nd pixel; channels 11 to 41");
    expect(describeSubset({ step: 1, world: { lo: -20, hi: -10, unit: "km/s", frame: "LSRK" } })).toBe("whole image; -20 to -10 km/s (LSRK)");
  });

  it("says what is missing instead of sending it", () => {
    const base = { ...fieldsOf("Core", def), reason: "why" };
    expect(inputOf({ ...base, name: " " })).toEqual({ problem: "Give the subset a name." });
    expect(inputOf({ ...base, reason: "" })).toEqual({ problem: "Say why: a reason is kept with every version." });
    expect(inputOf({ ...base, x1: "" })).toMatchObject({ problem: expect.stringContaining("four pixel numbers") });
    expect(inputOf({ ...base, step: "0" })).toMatchObject({ problem: expect.stringContaining("N is a whole number") });
    expect(inputOf({ ...base, spectral: "world", lo: "-20", hi: "", unit: "km/s" })).toMatchObject({ problem: "The interval needs two numbers." });
    expect(inputOf({ ...base, spectral: "world", lo: "-20", hi: "-10", unit: " " })).toMatchObject({ problem: expect.stringContaining("its unit") });
  });
});

describe("the subset form", () => {
  it("makes a box with a step, a world interval with its unit and frame, a name and a reason, and reports the server's refusal", async () => {
    const user = userEvent.setup();
    const saved = { subset: view(), geometry: {} };
    api.createSubset.mockRejectedValueOnce(new Error("the box is not inside the source image (0 to 31 by 0 to 23)")).mockResolvedValueOnce(saved);
    const onSaved = vi.fn();
    render(<SubsetForm datasetId={7} submitLabel="Save the subset" save={(input) => api.createSubset(7, input)} onSaved={onSaved} />);
    await user.type(screen.getByLabelText("Name"), "Core");
    await user.click(screen.getByRole("button", { name: "Save the subset" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Say why"); // not sent
    expect(api.createSubset).not.toHaveBeenCalled();
    await user.type(screen.getByLabelText("Reason"), "the peak");
    await user.selectOptions(screen.getByLabelText("Area"), "box");
    for (const [label, v] of [["x from", "4"], ["x to", "99"], ["y from", "3"], ["y to", "16"]] as const) await user.type(screen.getByLabelText(label), v);
    await user.clear(screen.getByLabelText("Keep every N-th pixel"));
    await user.type(screen.getByLabelText("Keep every N-th pixel"), "2");
    await user.selectOptions(screen.getByLabelText("Spectral range"), "world");
    await user.type(screen.getByLabelText("From"), "-20");
    await user.type(screen.getByLabelText("To"), "-10");
    await user.type(screen.getByLabelText("Frame (optional)"), "LSRK");
    await user.click(screen.getByRole("button", { name: "Save the subset" }));
    expect(api.createSubset).toHaveBeenCalledWith(7, { name: "Core", reason: "the peak", box: { x0: 3, x1: 98, y0: 2, y1: 15 }, step: 2, world: { lo: -20, hi: -10, unit: "km/s", frame: "LSRK" } });
    expect(await screen.findByRole("alert")).toHaveTextContent("not inside the source image");
    expect(screen.getByLabelText("x to")).toHaveValue("99"); // what was typed stays
    await user.clear(screen.getByLabelText("x to"));
    await user.type(screen.getByLabelText("x to"), "21");
    await user.click(screen.getByRole("button", { name: "Save the subset" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(saved));
  });

  it("offers the box of a saved region, but not of a line", async () => {
    const user = userEvent.setup();
    api.createSubset.mockResolvedValue({ subset: view(), geometry: {} });
    render(<SubsetForm datasetId={7} submitLabel="Save the subset" save={(input) => api.createSubset(7, input)} onSaved={vi.fn()} />);
    await waitFor(() => expect(screen.getByRole("option", { name: "The box of a saved region" })).toBeEnabled());
    await user.selectOptions(screen.getByLabelText("Area"), "region");
    expect(screen.getByRole("option", { name: "Peak (circle)" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Cut/ })).toBeNull();
    await user.type(screen.getByLabelText("Name"), "Peak square");
    await user.type(screen.getByLabelText("Reason"), "the surroundings of the peak");
    await user.selectOptions(screen.getByLabelText("Region"), "4");
    await user.click(screen.getByRole("button", { name: "Save the subset" }));
    expect(api.createSubset).toHaveBeenCalledWith(7, { name: "Peak square", reason: "the surroundings of the peak", regionId: 4, step: 1 });
  });
});

describe("the explorer marks a subset as virtual", () => {
  const props = { datasets: [dataset(), dataset({ id: 8, name: "b.fits", path: "/data/b.fits" })], selectedId: null, loading: false, error: null, onSelect: vi.fn(), onRegister: vi.fn() };

  it("lists it under its source, never as a file, and says when its file changed", async () => {
    const user = userEvent.setup();
    const onSelectSubset = vi.fn();
    render(<Explorer {...props} subsets={[view(), view({ id: 2, name: "Old window", stale: true })]} onSelectSubset={onSelectSubset} />);
    // one flat list (it can be windowed): a.fits, its two subsets, then b.fits
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(4);
    expect(within(items[0]!).getByRole("button", { name: /a\.fits/ })).toBeInTheDocument();
    const core = within(items[1]!).getByRole("button", { name: /Core/ });
    expect(core).toHaveTextContent("virtual subset");
    expect(core).not.toHaveTextContent("stale");
    expect(within(items[2]!).getByRole("button", { name: /Old window/ })).toHaveTextContent("virtual subset · stale: its file changed");
    expect(within(items[3]!).getByRole("button", { name: /b\.fits/ })).toBeInTheDocument();
    expect(within(items[3]!).queryByText(/virtual subset/)).toBeNull();
    await user.click(core);
    expect(onSelectSubset).toHaveBeenCalledWith(1);
  });

  it("marks the subset, not its source, as the open row", () => {
    render(<Explorer {...props} selectedId={7} subsets={[view()]} selectedSubsetId={1} onSelectSubset={vi.fn()} />);
    expect(screen.getByRole("button", { name: /Core/ })).toHaveAttribute("aria-current", "true");
    expect(screen.getByRole("button", { name: /a\.fits/ })).not.toHaveAttribute("aria-current");
  });
});

describe("the subsets of a dataset", () => {
  it("lists its own subsets with the virtual mark and opens one", async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    render(<SubsetsSection dataset={dataset() as never} subsets={[view(), view({ id: 3, sourceId: 99, name: "Elsewhere" })]} onOpen={onOpen} onCreated={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Core" })).toBeInTheDocument();
    expect(screen.queryByText("Elsewhere")).toBeNull();
    expect(screen.getByText("virtual")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Core" }));
    expect(onOpen).toHaveBeenCalledWith(1);
    await user.click(screen.getByRole("button", { name: "New virtual subset" }));
    expect(screen.getByRole("form", { name: "Virtual subset" })).toBeInTheDocument();
  });
});

describe("a subset opened", () => {
  const open = (over: { onChanged?: () => void } = {}) => render(<SubsetPage id={1} onChanged={over.onChanged ?? vi.fn()} onOpenDataset={vi.fn()} onClose={vi.fn()} />);

  it("says it is a window, what it resolved to in the source's pixels, and reads coordinates through it", async () => {
    const user = userEvent.setup();
    api.getSubset.mockResolvedValue(detail(view()));
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win, spectral: { resolved: true, unit: "m/s" } });
    api.getSubsetCoord.mockResolvedValue({ x: 0, y: 0, channel: 0, sky: { lon: 150.06, lat: 1.9 }, spectral: { value: -22000 } });
    open();
    expect(await screen.findByRole("heading", { name: "Core" })).toBeInTheDocument();
    expect(screen.getByText("virtual subset")).toBeInTheDocument();
    expect(screen.getByText(/It is not a file: nothing is copied until you write it as one/)).toBeInTheDocument();
    expect(await screen.findByText("9 by 7 pixels, 31 channels")).toBeInTheDocument();
    expect(screen.getByText(/x 4 to 20, y 3 to 15, every 2 of 32 by 24/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Read coordinates" }));
    expect(api.getSubsetCoord).toHaveBeenCalledWith(1, 0, 0, 0);
    expect(await screen.findByRole("status")).toHaveTextContent("150.06°, 1.9° on the sky; spectral -22000 m/s");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("beside the viewer says it in one calm line, offers no probe of its own, and says what is not available", async () => {
    api.getSubset.mockResolvedValue(detail(view()));
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win });
    render(<SubsetPage id={1} embedded onChanged={vi.fn()} onOpenDataset={vi.fn()} onClose={vi.fn()} />);
    expect(await screen.findByTestId("subset-line")).toHaveTextContent("virtual subset of a.fits · version 1 · box x 4–21, y 3–16 every 2nd pixel · channels 11–41");
    expect(screen.queryByRole("heading", { name: "Core" })).toBeNull();
    expect(await screen.findByText("9 by 7 pixels, 31 channels")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Read coordinates" })).toBeNull();
    expect(screen.getByText(/Masks, saved regions, overlays, channel maps and analyses are the file's/)).toBeInTheDocument();
  });

  it("is not shown as current when its file changed: nothing is read, and re-pinning needs a reason", async () => {
    const user = userEvent.setup();
    const stale = view({ stale: true, staleReason: "The file this subset was defined on has changed since. Re-pin it to the current file to record a new version." });
    api.getSubset.mockResolvedValue(detail(stale));
    api.repinSubset.mockResolvedValue({ subset: view({ version: 2, kind: "repin" }), geometry: {} });
    const onChanged = vi.fn();
    open({ onChanged });
    expect(await screen.findByRole("alert")).toHaveTextContent("has changed since");
    expect(screen.getByText("stale")).toBeInTheDocument();
    expect(api.getSubsetView).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Read coordinates" })).toBeNull();
    expect(screen.getByRole("button", { name: "Write as a file" })).toBeDisabled();
    const repin = screen.getByRole("button", { name: "Re-pin to the current file" });
    expect(repin).toBeDisabled();
    await user.type(screen.getByLabelText("Why this window still fits the changed file"), "only the timestamp changed");
    api.getSubset.mockResolvedValue(detail(view({ version: 2, kind: "repin" })));
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win });
    await user.click(repin);
    expect(api.repinSubset).toHaveBeenCalledWith(1, "only the timestamp changed", 1);
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByText("stale")).toBeNull());
  });

  it("writes it as a file and says when the file already written is reused", async () => {
    const user = userEvent.setup();
    api.getSubset.mockResolvedValueOnce(detail(view())).mockResolvedValue(detail(view({ materialised: 12 }))); // after the first write the list says a file exists
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win });
    api.materialiseSubset.mockResolvedValueOnce({ dataset: { id: 12, name: "subset-1-v1-ab12.fits" }, reused: false, subset: view({ materialised: 12 }) })
      .mockResolvedValueOnce({ dataset: { id: 12, name: "subset-1-v1-ab12.fits" }, reused: true, subset: view({ materialised: 12 }) });
    open();
    await user.click(await screen.findByRole("button", { name: "Write as a file" }));
    expect(api.materialiseSubset).toHaveBeenCalledWith(1, 1);
    expect(await screen.findByRole("status")).toHaveTextContent("Written as subset-1-v1-ab12.fits. It is an ordinary dataset; a script reads it unmodified.");
    await user.click(await screen.findByRole("button", { name: "Use the file already written" }));
    expect(await screen.findByText(/had already been written from the same file, and that file is unchanged/)).toBeInTheDocument();
  });

  it("carries a mask of the source into the file only when it is chosen, and says the cut is the subset's own window", async () => {
    const user = userEvent.setup();
    const m = { id: 31, datasetId: 7, type: "saturated", name: "Bright", width: 32, height: 24, channels: 64, flagged: 10, total: 49152, tier: "declared", mappingVersion: "", method: "computed", rule: {}, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "x", stale: false };
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ masks: [m] }), { status: 200, headers: { "Content-Type": "application/json" } })));
    api.getSubset.mockResolvedValue(detail(view()));
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win });
    api.materialiseSubset.mockResolvedValue({ dataset: { id: 12, name: "subset-1-v1-ab12.fits" }, reused: false, subset: view({ materialised: 12 }) });
    try {
      open();
      const choice = await screen.findByLabelText("Saturated: Bright");
      expect(choice).toHaveValue("leave"); // nothing is carried by default
      expect(screen.getByText(/Cut with the subset's window, its step and its channel range/)).toBeInTheDocument();
      await user.selectOptions(choice, "carry");
      await user.click(screen.getByRole("button", { name: "Write as a file" }));
      expect(api.materialiseSubset).toHaveBeenCalledWith(1, 1, [{ id: 31, carry: true }]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("for a source on a host, offers the same carry choice: the file and its masks are written on the host (ADR 0064)", async () => {
    const user = userEvent.setup();
    const m = { id: 31, datasetId: 7, hostId: 2, type: "saturated", name: "Bright", width: 32, height: 24, channels: 64, flagged: 10, total: 49152, tier: "declared", mappingVersion: "", method: "computed", rule: {}, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "x", stale: false };
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ masks: [m] }), { status: 200, headers: { "Content-Type": "application/json" } })));
    api.getSubset.mockResolvedValue(detail(view()));
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win });
    api.materialiseSubset.mockResolvedValue({ dataset: { id: 12, name: "subset-1-v1-ab12.fits" }, reused: false, subset: view({ materialised: 12 }) });
    try {
      open();
      const choice = await screen.findByLabelText("Saturated: Bright");
      expect(choice).toHaveValue("leave");
      expect(screen.queryByTestId("no-carry-on-host")).toBeNull();
      await user.selectOptions(choice, "carry");
      await user.click(screen.getByRole("button", { name: "Write as a file" }));
      expect(api.materialiseSubset).toHaveBeenCalledWith(1, 1, [{ id: 31, carry: true }]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("shows the server's refusal in place and deletes only after a second click", async () => {
    const user = userEvent.setup();
    api.getSubset.mockResolvedValue(detail(view()));
    api.getSubsetView.mockResolvedValue({ width: 9, height: 7, channels: 31, window: win });
    api.materialiseSubset.mockRejectedValue(new Error("the source file lives on another host"));
    api.deleteSubset.mockResolvedValue({ deleted: 1 });
    const onClose = vi.fn();
    render(<SubsetPage id={1} onChanged={vi.fn()} onOpenDataset={vi.fn()} onClose={onClose} />);
    await user.click(await screen.findByRole("button", { name: "Write as a file" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("another host");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    expect(api.deleteSubset).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /Delete the subset \(files written from it stay\)/ }));
    await waitFor(() => expect(api.deleteSubset).toHaveBeenCalledWith(1));
    expect(onClose).toHaveBeenCalled();
  });
});
