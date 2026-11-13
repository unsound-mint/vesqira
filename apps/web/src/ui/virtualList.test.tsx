import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Explorer } from "../catalog/Explorer";
import type { Dataset } from "../api/catalog";
import { TreeRow } from "./TreeRow";
import { ROW_PX, VirtualList, windowRange } from "./VirtualList";
import tokens from "../../../../design/tokens.css?raw";
import { withFacts } from "../catalog/testDatasets";

// jsdom has no layout. The scroller is a div whose height and scroll position are set by hand, and the list's rectangle is
// derived from that position the way a browser would report it.
function geometry(scroller: HTMLElement, height: number) {
  let top = 0;
  Object.defineProperty(scroller, "clientHeight", { configurable: true, get: () => height });
  Object.defineProperty(scroller, "scrollTop", { configurable: true, get: () => top, set: (v: number) => { top = v; } });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    const y = this === scroller ? 0 : this.getAttribute("role") === "list" ? -top : 0;
    return { top: y, left: 0, right: 0, bottom: y, width: 0, height: 0, x: 0, y, toJSON: () => ({}) };
  });
  return { scrollTo: (v: number) => { act(() => { top = v; fireEvent.scroll(scroller); }); } };
}

const ids = (n: number) => Array.from({ length: n }, (_, i) => String(i));
const rows = () => screen.getAllByRole("listitem");

function Demo({ n, onSelect = () => {} }: { n: number; onSelect?: (id: string) => void }) {
  return (
    <div data-testid="scroller" style={{ overflowY: "auto", height: 400 }}>
      <VirtualList label="Rows" ids={ids(n)} renderRow={(id, rowProps) => <TreeRow rowProps={rowProps} onSelect={() => onSelect(id)}>row {id}</TreeRow>} />
    </div>
  );
}

afterEach(() => vi.restoreAllMocks());

describe("windowRange", () => {
  it("covers the viewport plus the overscan and stays inside the list", () => {
    expect(windowRange(0, 400, 28, 5000, 8)).toEqual([0, 23]);
    expect(windowRange(2800, 400, 28, 5000, 8)).toEqual([92, 123]);
    expect(windowRange(1e9, 400, 28, 5000, 8)).toEqual([5000, 5000]);
  });
});

describe("VirtualList", () => {
  it("places rows by the design token for a row, so a change of --row cannot leave gaps or overlaps", () => {
    expect(tokens).toMatch(new RegExp(`--row:\\s*${ROW_PX}px`));
  });

  it("draws a short list whole, in flow", () => {
    render(<Demo n={50} />);
    expect(rows()).toHaveLength(50);
    expect(screen.getByRole("list", { name: "Rows" }).style.position).toBe("");
  });

  it("draws only the rows near the viewport of a long list and tells assistive technology the whole size", () => {
    const { getByTestId } = render(<Demo n={5000} />);
    const scroller = getByTestId("scroller");
    const mine = geometry(scroller, 400);
    mine.scrollTo(0);
    const first = rows();
    expect(first.length).toBeLessThan(40);
    expect(first[0]!.getAttribute("aria-posinset")).toBe("1");
    expect(first[0]!.getAttribute("aria-setsize")).toBe("5000");
    expect(screen.getByRole("list", { name: "Rows" }).style.height).toBe(`${5000 * 28}px`);

    mine.scrollTo(28 * 2500);
    // row 1 is drawn as well: it holds the tab stop (next test)
    const mid = rows().map((r) => Number(r.getAttribute("aria-posinset"))).filter((n) => n !== 1);
    expect(mid.length).toBeLessThan(40);
    expect(Math.min(...mid)).toBeGreaterThan(2400);
    expect(Math.max(...mid)).toBeLessThan(2600);
    expect(screen.getByText("row 2500")).toBeInTheDocument();
    expect(screen.queryByText("row 10")).toBeNull();
  });

  it("keeps one tab stop, moves by index and brings Home and End rows into existence", async () => {
    const user = userEvent.setup();
    const { getByTestId } = render(<Demo n={5000} />);
    geometry(getByTestId("scroller"), 400);
    act(() => { fireEvent.scroll(getByTestId("scroller")); });
    const tabStops = () => screen.getAllByRole("button").filter((b) => b.tabIndex === 0);
    expect(tabStops()).toHaveLength(1);
    await user.tab();
    expect(screen.getByText("row 0").closest("button")).toHaveFocus();
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(screen.getByText("row 2").closest("button")).toHaveFocus();
    await user.keyboard("{End}");
    expect(screen.getByText("row 4999").closest("button")).toHaveFocus();
    expect(rows().length).toBeLessThan(45);
    await user.keyboard("{Home}");
    expect(screen.getByText("row 0").closest("button")).toHaveFocus();
  });

  it("still draws the row that holds the tab stop after it scrolled out of the window", async () => {
    const user = userEvent.setup();
    const { getByTestId } = render(<Demo n={5000} />);
    const g = geometry(getByTestId("scroller"), 400);
    act(() => { fireEvent.scroll(getByTestId("scroller")); });
    await user.tab();
    g.scrollTo(28 * 3000);
    expect(screen.getByText("row 0").closest("button")).not.toBeNull();
    expect(screen.getByText("row 0").closest("button")!.tabIndex).toBe(0);
  });
});

const dataset = (i: number): Dataset => withFacts({
  id: i + 1, path: `/data/synthetic/cube_${String(i).padStart(5, "0")}.fits`, name: `cube_${String(i).padStart(5, "0")}.fits`, sizeBytes: 1536 + i, modifiedAt: "", addedAt: "",
  hostId: 0, nature: "", unitDeclaration: null, interpretation: null,
  header: { naxis: 2, bitpix: -32, bunit: null, bunit_status: "unresolved", specsys: null, radesys: null, axes: [] },
}) as unknown as Dataset;

describe("the dataset list at scale", () => {
  it("renders 5000 datasets with a few dozen rows and within a time budget", () => {
    const datasets = Array.from({ length: 5000 }, (_, i) => dataset(i));
    const t0 = performance.now();
    render(<Explorer datasets={datasets} selectedId={null} loading={false} error={null} onSelect={() => {}} onRegister={async () => null} />);
    const ms = performance.now() - t0;
    const list = screen.getByRole("list", { name: "Datasets" });
    expect(list.getAttribute("data-rows")).toBe("5000");
    expect(rows().length).toBeLessThan(80);
    console.info(`vitest: 5000 datasets rendered in ${ms.toFixed(0)} ms, ${rows().length} rows in the page`);
    // generous: it catches a regression to drawing every row (about 1 s and 5000 rows here), not a slow machine
    expect(ms).toBeLessThan(1500);
  });
});
