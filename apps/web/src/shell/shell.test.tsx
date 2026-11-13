import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LIMITS, parseLayout } from "./useLayout";
import { Shell } from "./Shell";
import { withFacts } from "../catalog/testDatasets";

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
  afterEach(() => vi.unstubAllGlobals());

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
    // the layout state is what is under test; the first-run page below hides the inspector itself
    expect(screen.getAllByRole("button", { name: "Toggle explorer" })[0]).toHaveAttribute("aria-pressed", "false");
    expect(screen.getAllByRole("button", { name: "Toggle inspector" })[0]).toHaveAttribute("aria-pressed", "true");
    expect(explorer()).toHaveAttribute("hidden");
  });

  it("with nothing open the way in is opening a folder, without the viewer's inspector and timeline", async () => {
    render(<Shell />);
    expect(await screen.findByRole("heading", { name: "Open a project" })).toBeInTheDocument();
    expect(document.querySelector('aside[aria-label="Inspector"]')).toHaveAttribute("hidden");
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

  it("gives Jobs, Boards and Reports the page: no map inspector of empty sections and no channel timeline beside them", async () => {
    const user = userEvent.setup();
    localStorage.setItem("vesqira.layout.v1", JSON.stringify({ sideOpen: true, inspOpen: true, timelineOpen: true }));
    render(<Shell />);
    for (const name of ["Jobs", "Boards", "Reports"]) {
      await user.click(screen.getByRole("button", { name }));
      expect(document.querySelector('aside[aria-label="Inspector"]'), name).toHaveAttribute("hidden");
      expect(document.querySelector('section[aria-label="Channels"]'), name).toHaveAttribute("hidden");
    }
  });
});

describe("Shell on a narrow screen", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.unstubAllGlobals());

  it("opens with the explorer closed even when the saved layout says open", () => {
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue({ matches: true }));
    localStorage.setItem("vesqira.layout.v1", JSON.stringify({ sideOpen: true, inspOpen: true, timelineOpen: true }));
    render(<Shell />);
    expect(explorer()).toHaveAttribute("hidden");
  });

  it("closes the explorer overlay when a dataset is chosen, so the choice is not hidden behind it", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue({ matches: true }));
    const dataset = withFacts({ id: 7, path: "/d/a.fits", name: "a.fits", sizeBytes: 10, modifiedAt: "", addedAt: "", hostId: 0, interpretation: null, nature: "", unitDeclaration: null,
      header: { naxis: 2, bitpix: -32, bunit: null, bunit_status: "unresolved", specsys: null, radesys: null, axes: [] } });
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const body = { datasets: [dataset], runs: [], hosts: [], jobs: [], projects: [], notifications: [], unread: 0 };
      if (url.startsWith("/api/datasets/")) return new Response(JSON.stringify({ error: "not in this test" }), { status: 404, headers: { "Content-Type": "application/json" } });
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(<Shell />);
    expect(explorer()).toHaveAttribute("hidden"); // closed until asked for, even for a layout saved open on a wide screen
    await user.click(screen.getAllByRole("button", { name: "Toggle explorer" })[0]!);
    expect(explorer()).not.toHaveAttribute("hidden");
    await user.click(await screen.findByRole("button", { name: /a\.fits/ }));
    await waitFor(() => expect(explorer()).toHaveAttribute("hidden"));
  });
});
