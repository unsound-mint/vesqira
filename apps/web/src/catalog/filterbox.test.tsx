import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useMemo, useState } from "react";
import { describe, expect, it } from "vitest";
import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import { FilterBox } from "../ui";
import { matchDataset, matchRun } from "./filter";
import { datasetVocabulary, runVocabulary } from "./vocabulary";

const metric = (key: string, value: number | null) => ({ key, value, unit: "", uncertainty: null, uncertaintyKind: "" as const, n: null, definition: "", status: "declared" as const });
const run = (id: number, params: Record<string, string>, over: Partial<Run> = {}): Run =>
  ({ id, name: `lmc_${id}`, kind: "vgt", gitCommit: "abc", inputName: "hnv1.fits", params: Object.entries(params).map(([key, value]) => ({ key, value, source: "config" })), metrics: [], review: { qc: "not_assessed", interpretation: "not_interpreted", note: "" }, ...over }) as Run;

const passed = { qc: "passed" as const, interpretation: "supports" as const, note: "" };
const RUNS = [
  run(1, { ker: "4", noise: "0.2", fitter: "axial" }, { review: passed, metrics: [metric("am", 0.3)] }),
  run(2, { ker: "8", noise: "0.37", fitter: "axial" }, { metrics: [metric("am", null)] }),
  run(3, { ker: "8", noise: "0.5", fitter: "axial" }),
  run(4, { ker: "16", noise: "0.5", fitter: "axial" }, { review: passed }),
];

function Runs() {
  const [q, setQ] = useState("");
  const vocabulary = useMemo(() => runVocabulary(RUNS), []);
  const shown = RUNS.filter((r) => matchRun(r, q));
  return (
    <>
      <FilterBox label="Filter runs" placeholder="Filter runs" value={q} onChange={setQ} vocabulary={vocabulary} shown={shown.length} total={RUNS.length} noun="runs" />
      <ul aria-label="Runs">{shown.map((r) => <li key={r.id}>{r.name}</li>)}</ul>
      <p data-testid="query">{q}</p>
    </>
  );
}

const names = () => within(screen.getByRole("list", { name: "Runs" })).queryAllByRole("listitem").map((l) => l.textContent);
const options = () => screen.queryAllByRole("option").map((o) => o.textContent ?? "");

describe("runVocabulary", () => {
  const v = runVocabulary(RUNS);
  it("lists keys that occur, varying keys before the one that is the same everywhere, with values and counts", () => {
    expect(v.keys.map((k) => k.key)).toEqual(["ker", "noise", "qc", "am", "fitter"].sort((a, b) => v.keys.findIndex((k) => k.key === a) - v.keys.findIndex((k) => k.key === b)));
    expect(v.keys[v.keys.length - 1]?.key).toBe("fitter");
    expect(v.keys.find((k) => k.key === "fitter")?.same).toBe(true);
    const ker = v.keys.find((k) => k.key === "ker");
    expect(ker?.numeric).toBe(true);
    expect(ker?.values).toEqual([{ value: "4", count: 1 }, { value: "8", count: 2 }, { value: "16", count: 1 }]);
    expect(ker?.range).toEqual({ min: 4, max: 16, n: 3 });
  });
  it("offers only recorded results and only assessed QC states", () => {
    const am = v.keys.find((k) => k.key === "am");
    expect(am?.kind).toBe("result");
    expect(am?.values).toEqual([{ value: "0.3", count: 1 }]); // the run whose result is null contributes nothing
    expect(am?.count).toBe(1);
    expect(v.keys.find((k) => k.key === "qc")?.values).toEqual([{ value: "passed", count: 2 }]);
  });
  it("offers a result key only for runs that recorded it, so a result that is null everywhere is not a key", () => {
    expect(runVocabulary([run(1, {}, { metrics: [metric("lost", null)] })]).keys).toEqual([]);
  });
});

describe("datasetVocabulary", () => {
  const ds = (id: number, bunit: string | null, hostId = 0, nature = ""): Dataset => ({ id, name: `d${id}.fits`, hostId, nature, header: { bunit } }) as unknown as Dataset;
  const list = [ds(1, "K"), ds(2, "K", 1, "observed"), ds(3, null), ds(4, "Jy/beam")];
  const v = datasetVocabulary(list, [{ id: 1, name: "cluster" }]);
  it("offers units as declared and a separate 'no unit declared' that maps to unit:none", () => {
    const unit = v.keys.find((k) => k.key === "unit");
    expect(unit?.values.map((x) => x.value).sort()).toEqual(["Jy/beam", "K", "none"]);
    expect(unit?.labels?.none).toBe("no unit declared");
    expect(v.keys.find((k) => k.key === "host")?.values).toEqual([{ value: "cluster", count: 1 }]);
    expect(matchDataset(list[2]!, "unit:none", undefined)).toBe(true);
    expect(matchDataset(list[0]!, "unit:none", undefined)).toBe(false);
  });
});

describe("FilterBox on runs", () => {
  it("suggests a key with its count as you type, then operators, then values that occur", async () => {
    const u = userEvent.setup();
    render(<Runs />);
    await u.type(screen.getByRole("combobox", { name: "Filter runs" }), "ke");
    expect(options()[0]).toContain("ker");
    expect(options()[0]).toContain("4 runs");
    await u.keyboard("{Enter}"); // takes the highlighted key
    expect(options().map((o) => o.replace(/\s/g, ""))).toEqual(["ker=", "ker<", "ker>", "ker≤", "ker≥"]);
    expect(screen.getByRole("listbox").textContent).toContain("4 … 16, 3 values");
    await u.keyboard("{Enter}"); // takes "ker ="
    expect(options().map((o) => o.replace(/\s/g, ""))).toEqual(["41runs", "82runs", "161runs"]);
    await u.keyboard("{ArrowDown}{ArrowDown}{Enter}"); // 8
    expect(screen.getByRole("button", { name: "Remove filter ker = 8" })).toBeTruthy();
    expect(names()).toEqual(["lmc_2", "lmc_3"]);
    expect(screen.getByRole("status").textContent).toBe("2 of 4 runs match");
    expect(screen.getByTestId("query").textContent).toBe("ker=8");
  });

  it("Enter commits typed text, removing a chip restores the list, Backspace removes the last chip", async () => {
    const u = userEvent.setup();
    render(<Runs />);
    const box = screen.getByRole("combobox", { name: "Filter runs" });
    await u.type(box, "noise>=0.3{Enter}");
    expect(screen.getByRole("button", { name: "Remove filter noise ≥ 0.3" })).toBeTruthy();
    expect(names()).toEqual(["lmc_2", "lmc_3", "lmc_4"]);
    await u.type(box, "lmc_4{Enter}");
    expect(names()).toEqual(["lmc_4"]);
    await u.click(screen.getByRole("button", { name: "Remove filter noise ≥ 0.3" }));
    expect(names()).toEqual(["lmc_4"]);
    await u.keyboard("{Backspace}");
    expect(names()).toHaveLength(4);
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  it("a typed value is taken as typed, not completed to a longer one; a hand-typed query still filters", async () => {
    const u = userEvent.setup();
    render(<Runs />);
    await u.type(screen.getByRole("combobox", { name: "Filter runs" }), "noise>0.3 qc:passed ");
    expect(screen.getByTestId("query").textContent).toBe("noise>0.3 qc:passed");
    expect(names()).toEqual(["lmc_4"]); // noise 0.5 and QC passed; run 2 (0.37) was not completed to a value
    await u.click(screen.getByRole("button", { name: "Remove filter QC: passed" }));
    expect(names()).toEqual(["lmc_2", "lmc_3", "lmc_4"]);
  });

  it("never offers an unrecorded result as a value and never matches it", async () => {
    const u = userEvent.setup();
    render(<Runs />);
    const box = screen.getByRole("combobox", { name: "Filter runs" });
    await u.type(box, "am=");
    expect(options().map((o) => o.replace(/\s+/g, " "))).toEqual(["0.31 runs"]);
    await u.clear(box);
    await u.type(box, "am=0{Enter}");
    expect(names()).toEqual([]); // the run with a null result is not a run with am=0
  });

  it("works from the keyboard alone: arrows, Tab, Escape, and a chip can be edited", async () => {
    const u = userEvent.setup();
    render(<Runs />);
    await u.tab();
    expect(document.activeElement).toBe(screen.getByRole("combobox", { name: "Filter runs" }));
    await u.keyboard("{ArrowDown}"); // an empty box lists every key
    expect(options().length).toBeGreaterThan(3);
    expect(screen.getByRole("combobox").getAttribute("aria-activedescendant")).toBeTruthy();
    await u.keyboard("{Escape}");
    expect(screen.getByRole("listbox", { hidden: true }).hidden).toBe(true);
    await u.keyboard("qc"); // a key written key:value lists its values at once
    expect(options().map((o) => o.replace(/\s+/g, " "))).toEqual(["passed2 runs"]);
    await u.keyboard("{Tab}");
    expect(screen.getByTestId("query").textContent).toBe("qc:passed");
    expect(names()).toEqual(["lmc_1", "lmc_4"]);
    await u.click(screen.getByRole("button", { name: "Edit filter QC: passed" }));
    expect((screen.getByRole("combobox") as HTMLInputElement).value).toBe("qc:passed");
    expect(names()).toEqual(["lmc_1", "lmc_4"]);
  });

  it("shows how many match and clears every condition", async () => {
    const u = userEvent.setup();
    render(<Runs />);
    await u.type(screen.getByRole("combobox", { name: "Filter runs" }), "ker=16{Enter}");
    expect(screen.getByText("1 of 4")).toBeTruthy();
    await u.click(screen.getByRole("button", { name: "Clear" }));
    expect(names()).toHaveLength(4);
  });
});
