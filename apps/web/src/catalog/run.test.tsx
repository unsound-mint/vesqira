import { render, screen } from "@testing-library/react";
import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import { RunPanel, runFor } from "./RunPanel";

const run = (over: Partial<Run> = {}): Run => ({
  id: 1, name: "s1_ker8", kind: "vgt", importedAt: "", generatedAt: "2026-06-25T09:21:44Z", gitCommit: "bcbaed4ec9800ef95c561cfb4ad45c344743e469-dirty",
  dirty: true, reproducibleFromCommit: false, elapsedSeconds: 106.2, params: [{ key: "ker", value: "8", source: "config" }], diagnostics: {},
  inputName: "hnv1.fits", inputSizeBytes: 4007885760, inputHint: null,
  outputs: [{ role: "psi", datasetId: 5, name: "psi.fits" }, { role: "reliability", datasetId: 6, name: "rel.fits" }], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over,
});
const none: Dataset[] = [];

describe("run panel", () => {
  it("finds the run a dataset belongs to", () => {
    expect(runFor([run()], 6)?.role).toBe("reliability");
    expect(runFor([run()], 99)).toBeNull();
    expect(runFor([run()], null)).toBeNull();
  });

  it("says a dirty commit is not reproducible and that the input link is only a hint", () => {
    render(<RunPanel run={run()} role="psi" datasets={none} onOpen={() => {}} onCompare={() => {}} />);
    expect(screen.getByText(/Not reproducible from the commit: uncommitted changes/)).toBeInTheDocument();
    expect(screen.getByText(/No registered file matches it by name and size/)).toBeInTheDocument();
    expect(screen.getByText("bcbaed4ec980 -dirty")).toBeInTheDocument();
  });

  it("calls a clean commit recorded and offers the inferred input with its basis", () => {
    const d = { id: 9, name: "hnv1.fits" } as Dataset;
    render(<RunPanel run={run({ dirty: false, reproducibleFromCommit: true, gitCommit: "abc", inputHint: { datasetId: 9, basis: "inferred from file name and size; not proof of identity" } })} role="psi" datasets={[d]} onOpen={() => {}} onCompare={() => {}} />);
    expect(screen.getByText("Commit recorded")).toBeInTheDocument();
    expect(screen.getByText(/not proof of identity/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open it" })).toBeInTheDocument();
  });
});

describe("assessment", () => {
  it("starts as not assessed and says finishing is not a scientific verdict", () => {
    render(<RunPanel run={run()} role="psi" datasets={[]} onOpen={() => {}} onCompare={() => {}} />);
    expect(screen.getByLabelText("Scientific QC")).toHaveValue("not_assessed");
    expect(screen.getByLabelText("Interpretation")).toHaveValue("not_interpreted");
    expect(screen.getByText(/whether the science holds is a separate question/)).toBeInTheDocument();
    expect(screen.queryByText(/\bvalid\b/i)).toBeNull();
  });
});
