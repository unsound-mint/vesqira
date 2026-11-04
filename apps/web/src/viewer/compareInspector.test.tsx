import { render, screen } from "@testing-library/react";
import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import type { Metric } from "../api/metrics";
import { metricRows, CompareInspector, paramDiff } from "./CompareInspector";
import type { Viewer } from "./useViewer";

const ds = (id: number, name: string, axial = false): Dataset => ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", hostId: 0, nature: "", header: {} as Dataset["header"], interpretation: axial ? { kind: "axial_orientation", unit: "rad", reliabilityId: null, declaredBy: "vgt-run-adapter", declaredAt: "", stale: false } : null });
const run = (id: number, name: string, ker: string, commit: string, dirty: boolean, out: number): Run => ({ id, name, kind: "vgt", importedAt: "", generatedAt: "", gitCommit: commit, dirty, reproducibleFromCommit: !dirty, elapsedSeconds: null, metrics: [], params: [{ key: "ker", value: ker, source: "config" }, { key: "dn", value: "10", source: "config" }], diagnostics: {}, inputName: "", inputSizeBytes: 0, inputHint: null, review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, outputs: [{ role: "psi", datasetId: out, name: "p" }] });
const viewer = (finite: number, nan: number) => ({ view: { bunit: null }, plane: { head: { channel: 0, channels: 1, stats: { finite_count: finite, nonfinite_count: nan } } } }) as unknown as Viewer;

describe("comparison inspector", () => {
  it("lists only the settings that differ", () => {
    expect(paramDiff([{ key: "ker", value: "8", source: "config" }, { key: "dn", value: "10", source: "config" }], [{ key: "ker", value: "12", source: "config" }, { key: "dn", value: "10", source: "config" }, { key: "x", value: "1", source: "config" }])).toEqual([{ key: "ker", a: "8", b: "12" }, { key: "x", a: "—", b: "1" }]);
  });

  it("shows the code versions, the changed settings and the coverage warning", () => {
    render(<CompareInspector a={ds(1, "a.fits", true)} b={ds(2, "b.fits", true)} va={viewer(90, 10)} vb={viewer(70, 30)} runs={[run(1, "ker8", "8", "abc123abc123abc123", true, 1), run(2, "ker12", "12", "def456def456def456", false, 2)]} compat={{ compatible: true, reasons: [] }} onCreated={() => {}} />);
    expect(screen.getByText(/abc123abc123 -dirty \(not reproducible\)/)).toBeInTheDocument();
    expect(screen.getByText("Run settings that differ (1)")).toBeInTheDocument();
    expect(screen.getByText(/not better agreement/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Axial difference map/ })).toBeInTheDocument();
    expect(screen.getByText(/1° and 179° differ by 2°, not 178/)).toBeInTheDocument();
  });

  it("proposes alignment, with its caveats, only when the grids differ", () => {
    render(<CompareInspector a={ds(1, "a.fits")} b={ds(2, "b.fits")} va={viewer(1, 0)} vb={viewer(1, 0)} runs={[]} compat={{ compatible: false, reasons: ["axis 1 step differs"] }} onCreated={() => {}} />);
    expect(screen.getByRole("button", { name: /Propose: align a.fits onto b.fits/ })).toBeInTheDocument();
    expect(screen.getByText(/does not match angular resolution/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Axial difference map/ })).toBeNull();
  });
});

describe("difference of scalar maps", () => {
  it("is offered only for two undeclared maps on one grid, and says the beams are not matched", () => {
    render(<CompareInspector a={ds(1, "a.fits")} b={ds(2, "b.fits")} va={viewer(1, 0)} vb={viewer(1, 0)} runs={[]} compat={{ compatible: true, reasons: [] }} onCreated={() => {}} />);
    expect(screen.getByRole("button", { name: /Difference map \(a.fits − b.fits\)/ })).toBeInTheDocument();
    expect(screen.getByText(/beams are not matched/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Axial difference map/ })).toBeNull();
  });
  it("is not offered for orientation maps (they have the axial difference) or for different grids", () => {
    const { rerender } = render(<CompareInspector a={ds(1, "a.fits", true)} b={ds(2, "b.fits", true)} va={viewer(1, 0)} vb={viewer(1, 0)} runs={[]} compat={{ compatible: true, reasons: [] }} onCreated={() => {}} />);
    expect(screen.queryByRole("button", { name: /^Difference map/ })).toBeNull();
    rerender(<CompareInspector a={ds(1, "a.fits")} b={ds(2, "b.fits")} va={viewer(1, 0)} vb={viewer(1, 0)} runs={[]} compat={{ compatible: false, reasons: ["x"] }} onCreated={() => {}} />);
    expect(screen.queryByRole("button", { name: /^Difference map/ })).toBeNull();
  });
});

describe("resolution", () => {
  it("keeps beam and pixel scale apart and never assumes one from the other", async () => {
    const { beamText, pixelScale } = await import("../catalog/resolution");
    const d = { header: { axes: [{ cdelt: -0.01, cunit: "deg" }, { cdelt: 0.01, cunit: "deg" }], beam: { status: "declared", bmaj: 0.03, bmin: 0.02, bpa: 45 } } } as unknown as Dataset;
    expect(pixelScale(d)).toBe("36.00 × 36.00 arcsec");
    expect(beamText(d)).toBe("108.00 × 72.00 arcsec, position angle 45°");
    const none = { header: { axes: [{ cdelt: -0.01, cunit: null }, { cdelt: 0.01, cunit: null }], beam: { status: "unresolved" } } } as unknown as Dataset;
    expect(pixelScale(none)).toBe("—");
    expect(beamText(none)).toBe("not declared");
  });
});

describe("run results side by side", () => {
  const m = (over: Partial<Metric>): Metric => ({ key: "am", value: 0.3, unit: "", uncertainty: null, uncertaintyKind: "", n: null, definition: "vs Planck", status: "declared", ...over });
  it("offers a comparison only when both runs state the same definition and unit", () => {
    const rows = metricRows([m({}), m({ key: "only_a" }), m({ key: "d" }), m({ key: "u", unit: "deg" })], [m({ value: 0.01 }), m({ key: "d", definition: "vs HI" }), m({ key: "u" }), m({ key: "nodef", definition: "" })]);
    const by = Object.fromEntries(rows.map((r) => [r.key, r]));
    expect(by.am?.basis).toBe("same definition and unit");
    expect(by.d?.basis).toBe("definitions differ: not directly comparable");
    expect(by.u?.basis).toBe("units differ: not directly comparable");
    expect(by.only_a?.basis).toBe("only one run reports it");
    expect(by.nodef?.b).toBe("0.3");
    expect(rows.find((r) => r.key === "am")?.b).toBe("0.01");
  });
  it("a metric a run did not record reads as unknown, not zero", () => {
    expect(metricRows([m({ value: null })], [m({ value: 0 })])[0]).toMatchObject({ a: "not recorded", b: "0" });
  });
});
