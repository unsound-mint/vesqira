import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReviewPage } from "./ReviewPage";

// What the reviewer's listener sends for a published cross-run plot (ADR 0065): the frozen picture and the words, never a run.
const provenance = {
  source: { name: "cross-run plot of 12 runs", sizeBytes: 0, modifiedAt: "2026-11-12T00:00:00Z", identity: "no file: drawn from the run records in the catalog, as they were when it was saved", headerDeclaredByFile: { axes: [], bunit: null, radesys: null, specsys: null, bitpix: null } },
  view: { kind: "runplot" },
  tiers: [],
  runPlot: { query: "block_size:8..64", y: "am", x: "block_size", xKind: "param", runs: 12, conversion: { to: "rad", method: "m", from: [{ unit: "deg", factor: 0.0174533, count: 4 }] } },
  statements: ["am drawn against block_size for 12 runs that match the filter \"block_size:8..64\".", "4 runs recorded in deg are shown converted to rad (x 0.0174533), by plain unit conversion with no equivalency; what each run recorded is kept by the author."],
};
const review = {
  snapshot: { boardTitle: "Sweep", label: "v1", createdAt: "2026-11-12T00:00:00Z", expiresAt: "2026-12-01T00:00:00Z", items: [{ revision: "r9", title: "am across the sweep", caption: "", note: "", previewSha: "b".repeat(64), width: 720, height: 400, provenance }] },
  comments: [], outcome: { value: "" },
};

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(review), { status: 200, headers: { "Content-Type": "application/json" } })));
});
afterEach(() => vi.unstubAllGlobals());

describe("a cross-run plot in a review", () => {
  it("shows the frozen picture with what it is made of, as a source that is not a file, and the conversion in words", async () => {
    render(<ReviewPage token="tok" />);
    expect(await screen.findByRole("heading", { name: "Sweep" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /am across the sweep/ })).toHaveAttribute("src", `/review-api/tok/preview/${"b".repeat(64)}`);
    expect(screen.getByText(/Source:/)).toBeInTheDocument();
    expect(screen.queryByText(/File:/)).toBeNull();
    expect(screen.getByText(/no file: drawn from the run records/)).toBeInTheDocument();
    expect(screen.getByText(/4 runs recorded in deg are shown converted to rad/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\/home\/|datasetId/);
  });
});
