import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LIMITS, parseLayout } from "./useLayout";
import { Shell } from "./Shell";

describe("parseLayout", () => {
  it("clamps stored sizes into their limits", () => {
    const l = parseLayout(JSON.stringify({ side: 9999, insp: 1, timeline: 100 }));
    expect(l.side).toBe(LIMITS.side.max);
    expect(l.insp).toBe(LIMITS.insp.min);
    expect(l.timeline).toBe(100);
  });
  it("falls back to defaults for garbage", () => {
    for (const raw of [null, "", "{", "[]", JSON.stringify({ side: "wide", sideOpen: "yes" })]) {
      const l = parseLayout(raw);
      expect(l.side).toBeGreaterThanOrEqual(LIMITS.side.min);
      expect(typeof l.sideOpen).toBe("boolean");
    }
  });
});

const explorer = () => document.querySelector('aside[aria-label="Explorer"]');

describe("Shell", () => {
  beforeEach(() => localStorage.clear());

  it("exposes the main landmarks", () => {
    render(<Shell />);
    for (const name of ["Explorer", "Viewer", "Inspector", "Channels"]) {
      expect(screen.getAllByLabelText(name).length).toBeGreaterThan(0);
    }
    expect(screen.getByRole("heading", { level: 1, name: "Vesqira" })).toBeInTheDocument();
  });

  it("opening one side panel closes the other on narrow screens", async () => {
    const mq = vi.fn().mockReturnValue({ matches: true });
    vi.stubGlobal("matchMedia", mq);
    const user = userEvent.setup();
    render(<Shell />);
    await user.click(screen.getAllByRole("button", { name: "Toggle explorer" })[0]!);
    await user.click(screen.getAllByRole("button", { name: "Toggle inspector" })[0]!);
    expect(explorer()).toHaveAttribute("hidden");
    expect(document.querySelector('aside[aria-label="Inspector"]')).not.toHaveAttribute("hidden");
    vi.unstubAllGlobals();
  });

  it("collapses and restores the explorer, and remembers it", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<Shell />);
    const [toggle] = screen.getAllByRole("button", { name: "Toggle explorer" });
    await user.click(toggle!);
    expect(explorer()).toHaveAttribute("hidden");
    unmount();
    render(<Shell />);
    expect(explorer()).toHaveAttribute("hidden");
  });
});
