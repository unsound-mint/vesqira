import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Dataset, Header, ImageDataset } from "../api/catalog";
import type { Mask } from "../api/masks";
import type { Derived } from "../api/analysis";
import { AnalysisPanel, DerivedPanel } from "./AnalysisPanel";
import { carryChoices, carryUndecided } from "./MaskCarry";
import { masksChanged } from "../viewer/useMasks";

const ds = (id: number, name: string, hostId = 0): ImageDataset => ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId, header: {} as Header });
const cube = ds(1, "cube.fits");
const mask = (over: Partial<Mask>): Mask => ({
  id: 1, datasetId: 1, hostId: 0, type: "saturated", name: "Bright", width: 4, height: 3, channels: 0, flagged: 2, total: 12, tier: "declared", mappingVersion: "", method: "computed",
  rule: { rule: "value >= 100" }, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "x", stale: false, ...over,
});
const sat = mask({ id: 1 });
const stale = mask({ id: 2, type: "low_snr", name: "S/N < 3", stale: true });
const none = mask({ id: 3, type: "missing", name: "No value (NaN, BLANK)", tier: "verified" });

function mockFetch(masks: Mask[], extra: Record<string, () => { status?: number; body: unknown }> = {}) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    let body: unknown = { error: "not found" };
    let status = 404;
    if (url.startsWith("/api/datasets/1/masks")) { body = { masks }; status = 200; }
    const key = Object.keys(extra).find((k) => url.startsWith(k));
    if (key) { const r = extra[key]!(); body = r.body; status = r.status ?? 200; }
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());
const sent = (calls: { url: string; init?: RequestInit }[]) => JSON.parse(String(calls.find((c) => c.url === "/api/analysis")!.init!.body));

describe("carrying masks into a cutout, a slab, an alignment or a convolution", () => {
  it("offers a choice per mask, leaves everything behind by default, states the rule and never offers the no-value mask", async () => {
    const calls = mockFetch([sat, stale, none], { "/api/analysis": () => ({ status: 201, body: { dataset: ds(5, "cutout.fits"), summary: {} } }) });
    render(<AnalysisPanel dataset={cube} datasets={[cube]} channels={64} onCreated={() => {}} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "cutout");
    const group = await screen.findByTestId("mask-carry");
    expect(within(group).getByText(/Cut with exactly the same window as the data/)).toBeInTheDocument();
    expect(within(group).getByLabelText("Saturated: Bright")).toHaveValue("leave");
    expect(within(group).getByLabelText("Low signal-to-noise: S/N < 3")).toHaveValue("leave");
    expect(within(group).queryByLabelText(/No value/)).toBeNull();
    expect(within(group).getByText(/never carried/)).toBeInTheDocument();
    expect(within(group).getByText("stale: made from other bytes, it cannot be carried")).toBeInTheDocument();
    expect(within(within(group).getByLabelText("Low signal-to-noise: S/N < 3")).getByRole("option", { name: "Carry into the new file" })).toBeDisabled(); // a stale mask cannot be carried
    expect(within(within(group).getByLabelText("Saturated: Bright")).getByRole("option", { name: "Carry into the new file" })).toBeEnabled();
    await userEvent.type(screen.getByLabelText("x from"), "1");
    await userEvent.type(screen.getByLabelText("x to"), "4");
    await userEvent.type(screen.getByLabelText("y from"), "1");
    await userEvent.type(screen.getByLabelText("y to"), "4");
    await userEvent.click(screen.getByRole("button", { name: "Create the new file" }));
    await waitFor(() => expect(calls.some((c) => c.url === "/api/analysis")).toBe(true));
    expect(sent(calls).carryMasks).toEqual([{ id: 1, carry: false }, { id: 2, carry: false }]);
  });

  it("sends carry: true only for the mask the researcher chose", async () => {
    const calls = mockFetch([sat], { "/api/analysis": () => ({ status: 201, body: { dataset: ds(5, "slab.fits"), summary: {} } }) });
    render(<AnalysisPanel dataset={cube} datasets={[cube]} channels={64} onCreated={() => {}} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "slab");
    await userEvent.selectOptions(await screen.findByLabelText("Saturated: Bright"), "carry");
    await userEvent.click(screen.getByRole("button", { name: "Create the new file" }));
    await waitFor(() => expect(calls.some((c) => c.url === "/api/analysis")).toBe(true));
    expect(sent(calls).carryMasks).toEqual([{ id: 1, carry: true }]);
    expect(screen.getByText(/a mask of one plane applies to every channel and is kept whole/)).toBeInTheDocument();
  });

  it("asks for every mask to be decided before an alignment or a convolution, with no default", async () => {
    const calls = mockFetch([sat, mask({ id: 4, type: "user_excluded", name: "Knot", tier: "inferred", mappingVersion: "1" })], { "/api/analysis": () => ({ status: 201, body: { dataset: ds(6, "aligned.fits"), summary: {} } }) });
    render(<AnalysisPanel dataset={cube} datasets={[cube, ds(2, "ref.fits")]} channels={64} onCreated={() => {}} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "align");
    await userEvent.selectOptions(screen.getByLabelText("Align onto the grid of"), "2");
    const go = screen.getByRole("button", { name: "Create the new file" });
    const bright = await screen.findByLabelText("Saturated: Bright");
    expect(bright).toHaveValue("");
    expect(screen.getByText(/exact \(area-overlap\) method/)).toBeInTheDocument();
    expect(go).toBeDisabled();
    await userEvent.selectOptions(bright, "carry");
    expect(go).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Excluded by you: Knot"), "leave");
    expect(go).toBeEnabled();
    await userEvent.click(go);
    await waitFor(() => expect(calls.some((c) => c.url === "/api/analysis")).toBe(true));
    expect(sent(calls).carryMasks).toEqual([{ id: 1, carry: true }, { id: 4, carry: false }]);

    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "convolve");
    expect(screen.getByText(/reaches a flagged pixel/)).toBeInTheDocument();
    expect(await screen.findByLabelText("Saturated: Bright")).toHaveValue("");
  });

  it("shows each mask's tier next to its choice", async () => {
    mockFetch([mask({ id: 4, type: "user_excluded", name: "Knot", tier: "inferred", mappingVersion: "1" })]);
    render(<AnalysisPanel dataset={cube} datasets={[cube]} channels={64} onCreated={() => {}} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "cutout");
    expect(await screen.findByText(/inferred \(mapping 1\)/)).toBeInTheDocument();
  });

  it("offers no carry choice for a job on a host, and says the masks stay with the file (ADR 0064)", async () => {
    mockFetch([mask({ id: 1, hostId: 1 })]);
    render(<AnalysisPanel dataset={ds(1, "remote.fits", 1)} datasets={[]} channels={64} onCreated={() => {}} onJob={() => {}} />);
    await userEvent.selectOptions(screen.getByLabelText("Analysis"), "cutout");
    expect(await screen.findByTestId("no-carry-on-host")).toHaveTextContent(/make them again on the result/i);
    expect(screen.queryByTestId("mask-carry")).toBeNull();
    masksChanged();
  });

  it("works out the request from the choices", () => {
    expect(carryChoices([sat, none], {}, false)).toEqual([{ id: 1, carry: false }]);
    expect(carryChoices([sat, none], {}, true)).toEqual([]);
    expect(carryChoices([sat], { 1: "carry" }, true)).toEqual([{ id: 1, carry: true }]);
    expect(carryUndecided([sat, none], {}, true)).toBe(true);
    expect(carryUndecided([sat, none], { 1: "leave" }, true)).toBe(false);
    expect(carryUndecided([sat], {}, false)).toBe(false);
  });
});

describe("a result says what it carried", () => {
  it("lists the masks carried, those left behind and the recomputed no-value mask, with tiers and no ids", async () => {
    const d: Derived = {
      datasetId: 9, op: "cutout", sources: [1], software: {}, createdAt: "", summary: {},
      params: { options: {}, maskCarry: { operation: "cutout", by: "cutout x 4-20, y 2-15 (0-based pixels of the source, inclusive)", flagsFollow: "", carried: [{ type: "user_excluded", name: "Knot", tier: "declared", fingerprint: "sha256:a", carriedFingerprint: "sha256:b" }],
        leftBehind: [{ type: "saturated", name: "Bright", tier: "inferred", mappingVersion: "1", fingerprint: "sha256:c" }], recomputed: ["missing"] } },
    };
    mockFetch([], { "/api/datasets/9/derived": () => ({ body: d }) });
    render(<DerivedPanel dataset={ds(9, "cutout.fits") as Dataset} datasets={[cube]} />);
    const box = await screen.findByTestId("derived-mask-carry");
    expect(within(box).getByText(/Excluded by you “Knot”: carried by cutout x 4-20/)).toBeInTheDocument();
    expect(within(box).getByText(/Saturated “Bright”: left behind/)).toBeInTheDocument();
    expect(within(box).getByText(/No-value mask: computed again from this file’s values, not carried/)).toBeInTheDocument();
    expect(box.textContent).not.toMatch(/sha256|"id"/);
  });
});
