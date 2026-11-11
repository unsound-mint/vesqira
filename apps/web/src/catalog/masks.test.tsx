import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Mask } from "../api/masks";
import { tierLine, tierText } from "../api/tier";
import { HATCH, maskPath } from "../viewer/maskHatch";
import { MaskLegend, MaskOverlay } from "../viewer/MaskOverlay";
import { TierBadge } from "../ui/TierBadge";
import { MaskPolicies, badWeight, usesOf } from "./MaskPolicies";
import type { PolicyChoice } from "./MaskPolicies";

const mask = (over: Partial<Mask> = {}): Mask => ({
  id: 1, datasetId: 1, type: "saturated", name: "Saturated ≥ 100", width: 4, height: 3, channels: 0, flagged: 2, total: 12, tier: "declared", mappingVersion: "", method: "computed",
  rule: { rule: "value >= 100" }, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "x", stale: false, ...over,
});

describe("hatch overlays", () => {
  it("draws runs of flagged pixels only, north up, and nothing for a plane with none (zero is not a flag)", () => {
    const none = maskPath(new Uint8Array(12), 4, 3, 10);
    expect(none.d).toBe("");
    // row 0 (the bottom) has pixels 1 and 2 flagged: one run, at the bottom of a 3-row plane drawn at 10 px per pixel
    const data = new Uint8Array(12);
    data[1] = 1; data[2] = 1;
    expect(maskPath(data, 4, 3, 10)).toEqual({ d: "M10 20h20v10h-20z", step: 1 });
  });

  it("coarsens instead of drawing an unbounded number of runs, and says by how much", () => {
    const data = new Uint8Array(64 * 64);
    for (let i = 0; i < data.length; i += 2) data[i] = 1; // every other pixel: the most runs
    const p = maskPath(data, 64, 64, 1, 100);
    expect(p.step).toBeGreaterThan(1);
  });

  it("gives every type its own pattern, so types are told apart without colour", () => {
    const tiles = Object.values(HATCH).map((h) => (h.dots ? "dots" : h.tile));
    expect(new Set(tiles).size).toBe(tiles.length);
  });

  it("names each shown mask in a legend, with its type, pixels and staleness, in words", () => {
    render(<MaskLegend list={[mask(), mask({ id: 2, type: "low_snr", name: "S/N < 3", flagged: 5, stale: true })]} />);
    const legend = screen.getByTestId("mask-legend");
    expect(within(legend).getByText("Saturated: Saturated ≥ 100")).toBeInTheDocument();
    expect(within(legend).getByText("Low signal-to-noise: S/N < 3")).toBeInTheDocument();
    expect(within(legend).getByText("5 pixels, stale")).toBeInTheDocument();
  });

  it("fetches the plane of each shown mask for the channel and paints one hatched path per mask", async () => {
    const head = new TextEncoder().encode(JSON.stringify({ width: 4, height: 3 }));
    const buf = new ArrayBuffer(4 + head.length + 12);
    new DataView(buf).setUint32(0, head.length, true);
    new Uint8Array(buf, 4, head.length).set(head);
    new Uint8Array(buf, 4 + head.length)[1] = 1;
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(buf.slice(0), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);
    const { container } = render(<MaskOverlay list={[mask()]} channel={7} width={4} height={3} cell={10} />);
    await waitFor(() => expect(container.querySelector("path[data-mask-type='saturated']")).not.toBeNull());
    expect(String(fetchMock.mock.calls[0]![0])).toContain("/api/masks/1/plane?channel=7");
    expect(container.querySelector("path[data-mask-type='saturated']")!.getAttribute("fill")).toMatch(/^url\(#/);
    vi.unstubAllGlobals();
  });
});

describe("policy controls", () => {
  const masks = [mask(), mask({ id: 2, type: "missing", name: "No value" })];
  it("applies nothing until a policy is chosen per mask, and a no-value mask can only be excluded", async () => {
    const user = userEvent.setup();
    let choice: PolicyChoice = {};
    const { rerender } = render(<MaskPolicies masks={masks} choice={choice} onChange={(c) => { choice = c; }} />);
    expect(usesOf(choice)).toEqual([]);
    const sat = screen.getByLabelText("Saturated: Saturated ≥ 100");
    expect(within(sat).getByRole("option", { name: "Keep them (recorded as kept)" })).toBeInTheDocument();
    expect(within(sat).queryByRole("option", { name: "Weight them" })).toBeNull(); // no weights in this analysis
    const miss = screen.getByLabelText("No value: No value");
    expect(within(miss).queryByRole("option", { name: /Keep/ })).toBeNull();
    await user.selectOptions(sat, "exclude");
    rerender(<MaskPolicies masks={masks} choice={choice} onChange={(c) => { choice = c; }} />);
    expect(usesOf(choice)).toEqual([{ id: 1, policy: "exclude", weight: undefined }]);
  });

  it("offers a weight factor only where the analysis has weights, and checks it is from 0 to 1", async () => {
    const user = userEvent.setup();
    let choice: PolicyChoice = {};
    const { rerender } = render(<MaskPolicies masks={[mask()]} choice={choice} onChange={(c) => { choice = c; }} allowWeight />);
    await user.selectOptions(screen.getByLabelText("Saturated: Saturated ≥ 100"), "weight");
    rerender(<MaskPolicies masks={[mask()]} choice={choice} onChange={(c) => { choice = c; }} allowWeight />);
    expect(usesOf(choice)).toEqual([{ id: 1, policy: "weight", weight: 0.5 }]);
    expect(badWeight(choice)).toBe(false);
    expect(badWeight({ 1: { policy: "weight", weight: "1.5" } })).toBe(true);
    expect(badWeight({ 1: { policy: "weight", weight: "" } })).toBe(true);
  });

  it("does not let a stale mask be applied", () => {
    render(<MaskPolicies masks={[mask({ stale: true })]} choice={{}} onChange={() => {}} />);
    expect(screen.getByLabelText("Saturated: Saturated ≥ 100")).toBeDisabled();
  });
});

describe("tiers", () => {
  it("say what they are in words, with the rule and mapping version of an inference", () => {
    expect(tierText("verified")).toBe("verified");
    expect(tierText("declared")).toBe("declared");
    expect(tierText("inferred", "2", "vgt-run-adapter")).toBe("inferred by vgt-run-adapter/2");
    expect(tierText("unresolved")).toBe("unresolved");
    expect(tierText(undefined)).toBe("tier not recorded");
    render(<TierBadge tier="inferred" mappingVersion="1" rule="rohsa-run-adapter" />);
    expect(screen.getByText("inferred by rohsa-run-adapter/1")).toBeInTheDocument();
  });

  it("make a provenance line a reviewer can read, with no id in it", () => {
    const line = tierLine({ what: "mask: saturated (Saturated ≥ 100)", tier: "declared", note: "pixels flagged saturated were excluded in this result" });
    expect(line).toBe("mask: saturated (Saturated ≥ 100): declared. pixels flagged saturated were excluded in this result.");
  });
});
