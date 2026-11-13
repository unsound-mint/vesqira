import { matchDataset, matchRun } from "./filter";
import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import { withFacts } from "./testDatasets";

const run = (params: Record<string, string>, over: Partial<Run> = {}): Run =>
  ({ id: 1, name: "lmc_ker8", kind: "vgt", gitCommit: "abc123", inputName: "hnv1.fits", params: Object.entries(params).map(([key, value]) => ({ key, value, source: "config" })), metrics: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over }) as Run;

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

describe("run filter on declared results", () => {
  const m = (key: string, value: number | null) => ({ key, value, unit: "", uncertainty: null, uncertaintyKind: "" as const, n: null, definition: "", status: "declared" as const });
  const r = run({ ker: "8" }, { metrics: [m("am", 0.344), m("zero", 0), m("lost", null)] });
  it("compares a result like a parameter, keeps a recorded zero, and never matches an unrecorded one", () => {
    expect(matchRun(r, "am>0.3")).toBe(true);
    expect(matchRun(r, "am<0.3")).toBe(false);
    expect(matchRun(r, "am>=0.344")).toBe(true);
    expect(matchRun(r, "zero=0")).toBe(true);
    expect(matchRun(r, "lost=0")).toBe(false); // unknown is not 0
    expect(matchRun(r, "lost<5")).toBe(false);
    expect(matchRun(r, "nothing>1")).toBe(false);
    expect(matchRun(r, "ker=8 am>0.3")).toBe(true);
  });
  it("a parameter wins over a result of the same name", () => {
    const both = run({ am: "5" }, { metrics: [m("am", 0.1)] });
    expect(matchRun(both, "am=5")).toBe(true);
    expect(matchRun(both, "am<1")).toBe(false);
  });
});

describe("dataset filter", () => {
  const d = withFacts({ name: "hi_cube.fits", nature: "observed", header: { bunit: "K" } }) as unknown as Dataset;
  it("looks in name, unit, nature and host", () => {
    expect(matchDataset(d, "hi", undefined)).toBe(true);
    expect(matchDataset(d, "observed K", undefined)).toBe(true);
    expect(matchDataset(d, "cluster", "Cluster")).toBe(true);
    expect(matchDataset(d, "simulated", undefined)).toBe(false);
  });
});

describe("dataset filter on declared fields", () => {
  const mk = (bunit: string | null, nature = "") => withFacts({ name: "x.fits", nature, header: { bunit } }) as unknown as Dataset;
  it("a unit you declared for a file without BUNIT is found like any other, and the file's own wins", () => {
    const declared = (bunit: string | null, unit: string) => withFacts({ name: "x.fits", nature: "", header: { bunit }, unitDeclaration: { unit } }) as unknown as Dataset;
    expect(matchDataset(declared(null, "Jy/beam"), "unit:jy/beam", undefined)).toBe(true);
    expect(matchDataset(declared(null, "Jy/beam"), "unit:none", undefined)).toBe(false);
    expect(matchDataset(declared("K", "Jy/beam"), "unit:K", undefined)).toBe(true);
    expect(matchDataset(declared("K", "Jy/beam"), "unit:jy/beam", undefined)).toBe(false);
  });
  it("unit:none matches only datasets that declare no unit, and unit:K only exact K", () => {
    expect(matchDataset(mk(null), "unit:none", undefined)).toBe(true);
    expect(matchDataset(mk(""), "unit:none", undefined)).toBe(true);
    expect(matchDataset(mk("K"), "unit:none", undefined)).toBe(false);
    expect(matchDataset(mk("K"), "unit:k", undefined)).toBe(true);
    expect(matchDataset(mk("K km/s"), "unit:k", undefined)).toBe(false);
    expect(matchDataset(mk(null), "unit:k", undefined)).toBe(false);
  });
  it("host and nature are exact; no nature declared is nature:none", () => {
    expect(matchDataset(mk("K", "observed"), "nature:observed", undefined)).toBe(true);
    expect(matchDataset(mk("K", ""), "nature:none", undefined)).toBe(true);
    expect(matchDataset(mk("K", "observed"), "nature:none", undefined)).toBe(false);
    expect(matchDataset(mk("K"), "host:cluster", "Cluster")).toBe(true);
    expect(matchDataset(mk("K"), "host:clust", "Cluster")).toBe(false);
    expect(matchDataset(mk("K"), "host:cluster", undefined)).toBe(false);
  });
});
