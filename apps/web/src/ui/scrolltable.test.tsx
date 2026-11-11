import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Basis, leadClause, ScrollTable, useRoving } from ".";
import { rovingTarget } from "./roving";
import { scrollEdges } from "./ScrollTable";

describe("scrollEdges", () => {
  it("has a right edge to show only while more is hidden there", () => {
    expect(scrollEdges({ scrollLeft: 0, clientWidth: 300, scrollWidth: 300 })).toEqual({ left: false, right: false });
    expect(scrollEdges({ scrollLeft: 0, clientWidth: 300, scrollWidth: 900 })).toEqual({ left: false, right: true });
    expect(scrollEdges({ scrollLeft: 300, clientWidth: 300, scrollWidth: 900 })).toEqual({ left: true, right: true });
    expect(scrollEdges({ scrollLeft: 600, clientWidth: 300, scrollWidth: 900 })).toEqual({ left: true, right: false });
  });

  it("tolerates a fractional scroll position at the end", () => {
    expect(scrollEdges({ scrollLeft: 599.4, clientWidth: 300, scrollWidth: 900 }).right).toBe(false);
  });
});

function size(el: HTMLElement, clientWidth: number, scrollWidth: number) {
  Object.defineProperty(el, "clientWidth", { configurable: true, value: clientWidth });
  Object.defineProperty(el, "scrollWidth", { configurable: true, value: scrollWidth });
}

describe("ScrollTable", () => {
  it("is a labelled, focusable region (keyboard scrollable) and shades the edge that has more", () => {
    const { container } = render(<ScrollTable label="Fitted components"><table><tbody><tr><td>x</td></tr></tbody></table></ScrollTable>);
    const region = screen.getByRole("region", { name: "Fitted components" });
    expect(region).toHaveAttribute("tabindex", "0");
    expect(container.querySelector('[aria-hidden="true"]')).toBeNull(); // fits: no shade
    size(region, 300, 900);
    fireEvent.scroll(region);
    expect(container.querySelectorAll('[aria-hidden="true"]')).toHaveLength(1); // right edge
    region.scrollLeft = 600;
    fireEvent.scroll(region);
    expect(container.querySelectorAll('[aria-hidden="true"]')).toHaveLength(1); // now the left one
    expect(container.firstElementChild).toHaveAttribute("data-more-right", "false");
    expect(container.firstElementChild).toHaveAttribute("data-more-left", "true");
  });
});

describe("rovingTarget", () => {
  it("moves one row, clamps at the ends, jumps with Home and End, ignores other keys", () => {
    expect(rovingTarget("ArrowDown", 0, 3)).toBe(1);
    expect(rovingTarget("ArrowDown", 2, 3)).toBe(2);
    expect(rovingTarget("ArrowUp", 0, 3)).toBe(0);
    expect(rovingTarget("Home", 2, 3)).toBe(0);
    expect(rovingTarget("End", 0, 3)).toBe(2);
    expect(rovingTarget("a", 0, 3)).toBeNull();
    expect(rovingTarget("ArrowDown", 0, 0)).toBeNull();
  });
});

function List({ selected }: { selected?: string }) {
  const r = useRoving(["a", "b", "c"], selected);
  return (
    <>
      <button type="button">Before</button>
      <div role="list" {...r.listProps}>
        {["a", "b", "c"].map((id) => <button key={id} type="button" {...r.itemProps(id)}>{id}</button>)}
      </div>
      <button type="button">After</button>
    </>
  );
}

describe("useRoving", () => {
  it("is one tab stop at the selected row; arrows move, Tab leaves", async () => {
    const user = userEvent.setup();
    render(<List selected="b" />);
    expect(screen.getByRole("button", { name: "b" })).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("button", { name: "a" })).toHaveAttribute("tabindex", "-1");
    await user.tab(); // Before
    await user.tab(); // into the list: the selected row
    expect(screen.getByRole("button", { name: "b" })).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("button", { name: "c" })).toHaveFocus();
    await user.keyboard("{Home}");
    expect(screen.getByRole("button", { name: "a" })).toHaveFocus();
    await user.tab(); // out of the list, not to the next row
    expect(screen.getByRole("button", { name: "After" })).toHaveFocus();
    await user.tab({ shift: true }); // back in at the row last used
    expect(screen.getByRole("button", { name: "a" })).toHaveFocus();
  });

  it("starts at the first row when none is selected", () => {
    render(<List />);
    expect(screen.getByRole("button", { name: "a" })).toHaveAttribute("tabindex", "0");
  });
});

describe("Basis", () => {
  it("keeps the whole sentence one click away and the summary short", () => {
    const text = "hi_cube.fits contours of channel 41 at 3, 5 σ; placed by WCS geometry (astropy); never resampled onto the base grid";
    expect(leadClause(text)).toBe("hi_cube.fits contours of channel 41 at 3, 5 σ");
    expect(leadClause("a ".repeat(80), 20).length).toBeLessThanOrEqual(21);
    expect(leadClause("x.fits contours of channel 41 (8 km/s) at 3, 5, 10 × σ (σ = 0.2347 K, 1.4826 × MAD of that channel)", 70)).toBe("x.fits contours of channel 41 (8 km/s) at 3, 5, 10 × σ…");
    render(<Basis summary={leadClause(text)}>{text}</Basis>);
    expect(screen.getByText("Basis")).toBeInTheDocument();
    expect(screen.getByText(text)).toBeInTheDocument();
  });

  it("is just the line when there is nothing behind it", () => {
    render(<Basis summary="One line" />);
    expect(screen.queryByText("Basis")).toBeNull();
  });
});
