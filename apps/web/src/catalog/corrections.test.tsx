import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { View } from "../api/planes";
import { captionLines } from "../viewer/figure";
import { unitLabel } from "../viewer/format";
import { CorrectionsPanel } from "./CorrectionsPanel";
import { effectiveUnit } from "./unit";

const ds = { id: 7, name: "cube.fits", path: "/d/cube.fits", hostId: 0, kind: "fits", header: { naxis: 3, bunit: "K", bunit_status: "declared", axes: [] }, interpretation: null, unitDeclaration: null, corrections: null } as unknown as Dataset;
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
const at = "2026-11-12T10:00:00Z";

const entry = (value: unknown, file: unknown, reason: string) => ({ value, file, reason, source: "", at });
const record = (id: number, action: string, changes: unknown[], reason: string, state: unknown = {}) => ({ id, action, state, changes, reason, source: "", author: "user", tier: "declared", at, revertsId: null, sizeBytes: 1, modifiedAt: at, sourceIdentity: "", stale: false });
const points = (lat0: number) => ["centre", "bottom left", "bottom right", "top left", "top right"].map((name, i) => ({ name, x: i, y: i, lon: 150, lat: lat0 + i * 0.01 }));
const coords = (lat0: number) => ({ sky: { kind: "celestial" }, points: points(lat0), spectral: null });
const impact = {
  groups: [
    { key: "figures", effect: "pinned", text: "1 saved figure was drawn from this dataset. Each keeps its picture and records the correction state it was drawn under.", count: 1, items: [{ id: 3, label: "Moment map" }] },
    { key: "regions", effect: "live", text: "1 saved region stays on the same pixels.", count: 1, items: [{ id: 4, label: "core" }] },
  ],
  live: ["Open views, linked views and readouts use the corrected WCS."],
  notes: ["Analyses run as jobs on a host do not take corrections."],
};

function stubAPI(list: unknown, preview: unknown) {
  const calls: { url: string; body: any }[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : null;
    calls.push({ url: String(url), body });
    if (String(url).endsWith("/preview")) return Promise.resolve(json(preview));
    if (init?.method === "POST") return Promise.resolve(json({ record: record(9, body.action, [], body.reason), dataset: ds }, 201));
    return Promise.resolve(json(list));
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("correcting what the file says", () => {
  it("shows the effect before saving: what the file says, the coordinates before and after, and what depends on the dataset; saves with a reason as a new record", async () => {
    const list = { datasetId: 7, current: null, history: [], last: 0, fileBunit: "K", unitCorrectable: true };
    const preview = {
      action: "set", changes: [{ what: "CRVAL2", file: 2, before: null, after: 2.05 }], state: { wcs: { CRVAL2: entry(2.05, 2, "x") } },
      preview: { before: coords(2), after: coords(2.05), refused: null, changes: [], note: "", file_bunit: "K", bunit_after: "K" }, impact,
    };
    const calls = stubAPI(list, preview);
    const user = userEvent.setup();
    render(<CorrectionsPanel dataset={ds} />);
    await screen.findByText(/Nothing is corrected/);
    await user.selectOptions(screen.getByLabelText("Keyword"), "CRVAL2");
    await user.type(screen.getByLabelText("New CRVAL2"), "2.05");
    await user.click(screen.getByRole("button", { name: "Show the effect" }));
    const before = await screen.findByRole("region", { name: "Before saving" });
    expect(before).toHaveTextContent("CRVAL2 2 → 2.05 (the file says 2)");
    const sky = within(before).getByRole("table", { name: /Sky position/ });
    expect(within(sky).getAllByRole("row")).toHaveLength(6);
    expect(sky).toHaveTextContent("150.000000, 2.000000");
    expect(sky).toHaveTextContent("150.000000, 2.050000");
    expect(sky).toHaveTextContent("180.00″"); // 0.05 degrees north
    expect(before).toHaveTextContent("Keep the values they were made with");
    expect(before).toHaveTextContent("Moment map");
    expect(before).toHaveTextContent("Read again with the new values");
    expect(before).toHaveTextContent("core");
    expect(before).toHaveTextContent("do not take corrections");
    expect(calls.find((c) => c.url.endsWith("/preview"))?.body).toMatchObject({ action: "set", wcs: { CRVAL2: 2.05 } });
    const save = screen.getByRole("button", { name: "Save as a new record" });
    expect(save).toBeDisabled(); // a reason is required
    await user.type(screen.getByLabelText("Reason (required)"), "pointing from the log");
    await user.type(screen.getByLabelText(/Source \(optional\)/), "doi:10.1/x");
    await user.click(save);
    await waitFor(() => expect(calls.find((c) => c.url.endsWith("/corrections") && c.body)?.body).toEqual({ action: "set", wcs: { CRVAL2: 2.05 }, reason: "pointing from the log", source: "doi:10.1/x", expect: 0 }));
  });

  it("shows a refusal from the worker and does not offer to save it", async () => {
    const list = { datasetId: 7, current: null, history: [], last: 0, fileBunit: "K", unitCorrectable: true };
    const preview = { action: "set", changes: [{ what: "CTYPE1", file: "RA---TAN", before: null, after: "FREQ" }], state: {}, preview: { before: coords(2), after: null, refused: "the WCS correction is not applied: axis 1 would change kind (lon to spectral)", changes: [], note: "" }, impact };
    stubAPI(list, preview);
    const user = userEvent.setup();
    render(<CorrectionsPanel dataset={ds} />);
    await screen.findByText(/Nothing is corrected/);
    await user.selectOptions(screen.getByLabelText("Keyword"), "CTYPE1");
    await user.type(screen.getByLabelText("New CTYPE1"), "FREQ");
    await user.click(screen.getByRole("button", { name: "Show the effect" }));
    expect(await screen.findByText(/axis 1 would change kind/)).toBeInTheDocument();
    await user.type(screen.getByLabelText("Reason (required)"), "a reason");
    expect(screen.getByRole("button", { name: "Save as a new record" })).toBeDisabled();
  });

  it("lists the history newest first, says what the file says beside the corrected value, and reverts by a new record", async () => {
    const state = { unit: entry("Jy/beam", "K", "unit from the log"), wcs: { CRVAL2: entry(2.05, 2, "pointing") } };
    const list = {
      datasetId: 7, last: 2, fileBunit: "K", unitCorrectable: true,
      current: { state, hash: "h", tier: "declared", recordId: 2, at, stale: false },
      history: [
        record(1, "set", [{ what: "BUNIT", file: "K", before: null, after: "Jy/beam" }], "unit from the log"),
        record(2, "set", [{ what: "CRVAL2", file: 2, before: null, after: 2.05 }], "pointing"),
      ],
    };
    const preview = { action: "revert", changes: [{ what: "CRVAL2", file: 2, before: 2.05, after: null }], state: { unit: state.unit }, preview: { before: coords(2.05), after: coords(2), refused: null, changes: [], note: "" }, impact };
    const calls = stubAPI(list, preview);
    const user = userEvent.setup();
    render(<CorrectionsPanel dataset={ds} />);
    const inForce = await screen.findByRole("region", { name: "Corrections in force" });
    expect(inForce).toHaveTextContent("BUNIT");
    expect(inForce).toHaveTextContent("K");
    expect(inForce).toHaveTextContent("Jy/beam");
    expect(inForce).toHaveTextContent("unit from the log");
    const hist = screen.getByRole("region", { name: "History of corrections" });
    const items = within(hist).getAllByRole("listitem").filter((li) => li.parentElement?.tagName === "OL");
    expect(items[0]).toHaveTextContent("pointing");
    expect(items[1]).toHaveTextContent("unit from the log");
    await user.click(within(items[0]!).getByRole("button", { name: "Revert this…" }));
    const before = await screen.findByRole("region", { name: "Before saving" });
    expect(before).toHaveTextContent("correction withdrawn: back to the file's 2");
    await user.type(screen.getByLabelText("Reason (required)"), "the pointing was right");
    await user.click(screen.getByRole("button", { name: "Save as a new record" }));
    await waitFor(() => expect(calls.find((c) => c.url.endsWith("/corrections") && c.body)?.body).toEqual({ action: "revert", reverts: 2, reason: "the pointing was right", source: "", expect: 2 }));
    expect(screen.getByRole("link", { name: "Export corrections (JSON)" })).toHaveAttribute("href", "/api/datasets/7/corrections/sidecar");
  });

  it("says stale corrections are not applied and offers only to confirm or withdraw them", async () => {
    const state = { wcs: { CRVAL2: entry(2.05, 2, "pointing") } };
    const list = { datasetId: 7, last: 1, fileBunit: "K", unitCorrectable: true, current: { state, hash: "h", tier: "declared", recordId: 1, at, stale: true }, history: [{ ...record(1, "set", [], "pointing"), stale: true }] };
    stubAPI(list, {});
    render(<CorrectionsPanel dataset={ds} />);
    expect(await screen.findByText(/The file has changed since these corrections were made, so they are not applied/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "They still apply…" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show the effect" })).toBeNull();
    expect(screen.getByText("made for an earlier version of the file")).toBeInTheDocument();
  });
});

describe("a corrected unit is said wherever the unit is shown", () => {
  it("labels it as yours, filters by it, and the figure caption says what the file says", () => {
    expect(unitLabel("Jy/beam", "corrected")).toBe("Jy/beam (corrected by you)");
    const d = { ...ds, corrections: { state: { unit: entry("Jy/beam", "K", "r") }, hash: "h", tier: "declared", recordId: 1, at, stale: false } } as unknown as Dataset;
    expect(effectiveUnit(d)).toBe("Jy/beam");
    expect(effectiveUnit({ ...d, corrections: { ...d.corrections!, stale: true } } as Dataset)).toBe("K");
    const view = { width: 2, height: 2, channels: 1, bunit: "Jy/beam", bunit_status: "corrected", file_bunit: "K", wcs_corrected: ["CRVAL2"], fits_axes: { x: 1, y: 2, channel: null }, sky: { kind: "pixel" }, spectral: null, flags: { declared: false, channels: [] } } as unknown as View;
    const lines = captionLines({ dataset: "cube.fits", kind: "plane", channelLine: "", displayLine: "", limits: [0, 1], limitsUnit: "Jy/beam" }, view, null, { colormap: "viridis", stretch: "linear", range: "minmax", axial: false, region: null });
    expect(lines).toContain("Brightness unit (Jy/beam) corrected by the author; the file says K.");
    expect(lines).toContain("WCS corrected by the author (CRVAL2); the file's own values differ and the file was not changed.");
  });
});
