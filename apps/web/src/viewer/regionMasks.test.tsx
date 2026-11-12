import { render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { vi } from "vitest";
import type { Mask, MaskUse } from "../api/masks";
import { getRegionHistogram, getSpectrum, maskQuery } from "../api/planes";
import type { PlaneStats, RegionHistogramResult } from "../api/planes";
import type { PolicyChoice } from "../catalog/MaskPolicies";
import { usesOf } from "../catalog/MaskPolicies";
import { Histogram } from "./Histogram";
import { RegionPanel } from "./RegionPanel";
import type { Viewer } from "./useViewer";
import { useMaskedHistogram } from "./useMaskedHistogram";

const mask = (over: Partial<Mask> = {}): Mask => ({
  id: 7, datasetId: 1, hostId: 0, type: "saturated", name: "Saturated ≥ 100", width: 4, height: 3, channels: 0, flagged: 2, total: 12, tier: "declared", mappingVersion: "", method: "computed",
  rule: {}, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "x", stale: false, ...over,
});

const applied = { id: 7, name: "Saturated ≥ 100", type: "saturated" as const, policy: "exclude" as const, tier: "declared" as const, fingerprint: "sha256:x", pixels_flagged_with_value: 3, pixels_excluded: 3 };
const result = (over: Partial<RegionHistogramResult> = {}): RegionHistogramResult => ({
  ok: true, channel: 0, min: 0, max: 10, bins: 64, counts: Array.from({ length: 64 }, (_, i) => (i === 0 ? 5 : 0)), pixels: 10, no_value: 2, excluded_by_mask: 3, masks: [applied],
  region: { pixels: 10, requested_pixels: 10, clipped: false }, ...over,
});

afterEach(() => vi.unstubAllGlobals());

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

describe("masks on a region: the requests", () => {
  it("names each mask with its policy in the query, and names none unless the person chose", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (u: string) => { urls.push(u); return json(result()); }));
    const r = { shape: "rect", x0: 1, y0: 2, x1: 3, y1: 4 } as const;
    expect(maskQuery([])).toBe("");
    expect(maskQuery([{ id: 7, policy: "exclude" }, { id: 9, policy: "keep" }])).toBe("&mask=7:exclude&mask=9:keep");
    await getRegionHistogram(1, 3, r, 0, 10, 64, [{ id: 7, policy: "exclude" }]);
    expect(urls[0]).toContain("/api/datasets/1/region-histogram?channel=3&min=0&max=10&bins=64&shape=rect&x0=1&y0=2&x1=3&y1=4&mask=7:exclude");
    await getSpectrum(1, r, undefined, undefined, []).catch(() => undefined);
    expect(urls[1]).not.toContain("mask=");
    await getSpectrum(1, r, undefined, undefined, [{ id: 9, policy: "keep" }]).catch(() => undefined);
    expect(urls[2]).toMatch(/\/spectrum\?shape=rect&x0=1&y0=2&x1=3&y1=4&mask=9:keep$/);
  });

  it("counts a masked region on the worker only when masks are named, and never returns another request's answer", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (u: string) => { urls.push(u); return json(result()); }));
    const shape = { shape: "circle", cx: 2, cy: 2, r: 1 } as const;
    const none = renderHook(() => useMaskedHistogram(1, 0, shape, 0, 10, 64, []));
    expect(none.result.current).toBeNull();
    const { result: hook, rerender } = renderHook(({ uses }) => useMaskedHistogram(1, 0, shape, 0, 10, 64, uses), { initialProps: { uses: [{ id: 7, policy: "exclude" }] as MaskUse[] } });
    expect(hook.current).toEqual({ result: null, error: null }); // counting
    await waitFor(() => expect(hook.current?.result?.excluded_by_mask).toBe(3));
    expect(urls).toHaveLength(1);
    rerender({ uses: [{ id: 7, policy: "keep" }] });
    expect(hook.current).toEqual({ result: null, error: null }); // the old answer was for another policy
  });

  it("shows the refusal sentence (a stale mask is 409) instead of a count", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "the mask \"S\" was made from other bytes than this file has now (the file changed): make it again" }, 409)));
    const { result: hook } = renderHook(() => useMaskedHistogram(1, 0, { shape: "rect", x0: 0, y0: 0, x1: 1, y1: 1 }, 0, 10, 64, [{ id: 7, policy: "exclude" }]));
    await waitFor(() => expect(hook.current?.error).toContain("make it again"));
    expect(hook.current?.result).toBeNull();
  });
});

describe("masks on a region: the histogram", () => {
  const stats = { min: 0, max: 10, finite_count: 100, nonfinite_count: 0, overflow_count: 0 } as unknown as PlaneStats;
  const scope = { name: "Core", shape: { shape: "rect", x0: 0, y0: 0, x1: 4, y1: 1 } as const, width: 8, height: 6, preview: false };

  it("draws the worker's count, says no-value and masked pixels apart, and lists the masks applied", () => {
    render(<Histogram data={new Float32Array(48)} stats={stats} limits={null} unit="K" onChange={() => {}} scope={scope} masked={{ result: result(), error: null }} />);
    expect(screen.getByTestId("histogram-scope").textContent).toContain("10 pixels, 2 not finite");
    expect(screen.getByRole("img").getAttribute("aria-label")).toContain("Histogram of 5 finite pixels"); // 10 - 2 without a value - 3 left out
    const list = within(screen.getByTestId("histogram-masks"));
    expect(list.getByText(/Saturated mask “Saturated ≥ 100”: excluded; 3 pixels with a value flagged, 3 left out/)).toBeInTheDocument();
    expect(screen.getByText(/2 pixels with no value are not counted/)).toBeInTheDocument(); // the no-value count stays its own
    expect(list.getByText(/3 pixels with a value were left out by masks/)).toBeInTheDocument();
  });

  it("says why there is no count when the server refused", () => {
    render(<Histogram data={new Float32Array(48)} stats={stats} limits={null} unit="K" onChange={() => {}} scope={scope} masked={{ result: null, error: "a mask belongs to another dataset" }} />);
    expect(screen.getByRole("alert").textContent).toContain("a mask belongs to another dataset");
  });
});

describe("masks on a region: the controls", () => {
  function Harness({ masks }: { masks: Mask[] }) {
    const [choice, setChoice] = useState<PolicyChoice>({});
    const v = {
      view: {}, region: { shape: "rect", x0: 0, y0: 0, x1: 1, y1: 1 }, spectrum: null, plane: null, tool: "pointer", setTool: () => {}, setRegion: () => {}, path: null,
      savedRegions: { saved: [], hidden: new Set(), selectedId: null, undo: null, error: null }, datasetId: 1, interp: null, axial: null,
      datasetMasks: masks, maskChoice: choice, setMaskChoice: setChoice, maskUses: usesOf(choice),
    } as unknown as Viewer;
    return (
      <>
        <RegionPanel v={v} />
        <output data-testid="uses">{JSON.stringify(usesOf(choice))}</output>
      </>
    );
  }

  it("offers the same per-mask policy as statistics, applies nothing until chosen, and lets a no-value mask only be excluded", async () => {
    render(<Harness masks={[mask(), mask({ id: 8, type: "missing", name: "No value" })]} />);
    expect(screen.getByTestId("uses").textContent).toBe("[]");
    const sat = screen.getByLabelText("Saturated: Saturated ≥ 100");
    expect(within(sat).getByRole("option", { name: "Not applied" })).toBeInTheDocument();
    await userEvent.selectOptions(sat, "exclude");
    expect(JSON.parse(screen.getByTestId("uses").textContent!)).toEqual([{ id: 7, policy: "exclude" }]);
    const miss = screen.getByLabelText("No value: No value");
    expect(within(miss).queryByRole("option", { name: /Keep/ })).toBeNull();
    expect(within(screen.getByLabelText("Masks for this region")).queryByRole("option", { name: /Weight/ })).toBeNull(); // no weights for a spectrum
  });

  it("shows no controls for a dataset without masks", () => {
    render(<Harness masks={[]} />);
    expect(screen.queryByLabelText("Masks for this region")).toBeNull();
  });
});
