import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Metric } from "../api/metrics";
import { metricText } from "../api/metrics";
import { RunLine } from "../boards/RunLine";

const am: Metric = { key: "alignment_measure", value: 0.344, unit: "", uncertainty: 0.02, uncertaintyKind: "standard_error", n: 2695, definition: "Axial agreement with Planck 353 GHz", status: "declared" };

describe("metrics", () => {
  it("writes value, uncertainty with its kind, and the sample count", () => {
    expect(metricText(am)).toBe("0.344 ± 0.02 (standard error), n = 2695");
    expect(metricText({ ...am, unit: "deg", uncertaintyKind: "", n: null })).toBe("0.344 deg ± 0.02 (kind not stated)");
  });

  it("shows an unrecorded value as unknown, and a recorded zero as zero", () => {
    expect(metricText({ ...am, value: null })).toBe("not recorded");
    expect(metricText({ ...am, value: 0, uncertainty: null, n: null })).toBe("0");
  });

  it("the reviewer sees the results of the run with their definitions", () => {
    const run = { name: "r1", kind: "vgt", role: "psi", gitCommit: "abc", dirty: false, reproducibleFromCommit: true, generatedAt: "", params: [], metrics: [am, { ...am, key: "other", value: null, definition: "" }] };
    render(<RunLine run={run} />);
    expect(screen.getByText("0.344 ± 0.02 (standard error), n = 2695")).toBeTruthy();
    expect(screen.getByText("Axial agreement with Planck 353 GHz")).toBeTruthy();
    expect(screen.getByText("not recorded")).toBeTruthy();
  });
});
