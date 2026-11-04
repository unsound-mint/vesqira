import { matchDataset, matchRun } from "./filter";
import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";

const run = (params: Record<string, string>, over: Partial<Run> = {}): Run =>
  ({ id: 1, name: "lmc_ker8", kind: "vgt", gitCommit: "abc123", inputName: "hnv1.fits", params: Object.entries(params).map(([key, value]) => ({ key, value, source: "config" })), review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over }) as Run;

describe("run filter", () => {
  it("tests parameters as numbers when it can, as exact text otherwise, and never matches a missing parameter", () => {
    const r = run({ ker: "8", noise: "0.37", fitter: "axial_vonmises" });
    expect(matchRun(r, "ker=8")).toBe(true);
    expect(matchRun(r, "ker=8.0")).toBe(true);
    expect(matchRun(r, "noise>0.3")).toBe(true);
    expect(matchRun(r, "noise<=0.3")).toBe(false);
    expect(matchRun(r, "fitter=axial_vonmises")).toBe(true);
    expect(matchRun(r, "fitter=gauss")).toBe(false);
    expect(matchRun(r, "dn=10")).toBe(false);          // the run has no such parameter: not a match, not a zero
    expect(matchRun(r, "ker=")).toBe(false);
  });

  it("narrows with every word and searches names, commit, input and the author's QC", () => {
    const r = run({ ker: "8" }, { review: { qc: "passed", interpretation: "supports", note: "" } });
    expect(matchRun(r, "")).toBe(true);
    expect(matchRun(r, "lmc ker=8 qc:passed")).toBe(true);
    expect(matchRun(r, "lmc ker=9")).toBe(false);
    expect(matchRun(r, "qc:failed")).toBe(false);
    expect(matchRun(r, "HNV1")).toBe(true);
    expect(matchRun(r, "abc123")).toBe(true);
  });
});

describe("dataset filter", () => {
  const d = { name: "hi_cube.fits", nature: "observed", header: { bunit: "K" } } as unknown as Dataset;
  it("looks in name, unit, nature and host", () => {
    expect(matchDataset(d, "hi", undefined)).toBe(true);
    expect(matchDataset(d, "observed K", undefined)).toBe(true);
    expect(matchDataset(d, "irgetas", "Irgetas")).toBe(true);
    expect(matchDataset(d, "simulated", undefined)).toBe(false);
  });
});
