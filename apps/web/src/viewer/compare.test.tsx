import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Header, ImageDataset } from "../api/catalog";
import { CompareBar, linkState } from "./CompareBar";
import type { LinkControls } from "./CompareBar";
import { defaultPrefs, linkGroup, linkPills } from "../scene/link";

const ds = (id: number, name: string): ImageDataset => ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId: 0, header: {} as Header });
const all = [ds(1, "a.fits"), ds(2, "b.fits"), ds(3, "c.fits")];

describe("comparing maps", () => {
  it("links only when the grids are known to match", () => {
    expect(linkState({ compatible: true, reasons: [] }, false)).toMatchObject({ linked: true, tone: "ok" });
    expect(linkState({ compatible: false, reasons: ["x"] }, false)).toMatchObject({ linked: false, tone: "warn" });
    expect(linkState(null, false).linked).toBe(false);
    expect(linkState({ compatible: true, reasons: [] }, true).linked).toBe(false); // still checking: not linked yet
  });

  it("offers the other datasets and picks one", async () => {
    const picked: (number | null)[] = [];
    render(<CompareBar datasets={all} selectedId={1} compareId={null} onChange={(id) => picked.push(id)} compat={null} loading={false} error={null} />);
    const sel = screen.getByLabelText("Compare with");
    expect(Array.from((sel as HTMLSelectElement).options).map((o) => o.text)).toEqual(["Nothing", "b.fits", "c.fits"]);
    await userEvent.selectOptions(sel, "2");
    expect(picked).toEqual([2]);
  });

  it("says nothing is resampled and lists every reason when the grids differ", async () => {
    render(<CompareBar datasets={all} selectedId={1} compareId={2} onChange={() => {}} compat={{ compatible: false, reasons: ["axis 1 step differs", "celestial frame (RADESYS) differs or is unresolved"] }} loading={false} error={null} />);
    expect(screen.getByText(/not linked, nothing is resampled/)).toBeInTheDocument();
    await userEvent.click(screen.getByText("Why (2)"));
    expect(screen.getByText("axis 1 step differs")).toBeVisible();
  });

  describe("link controls (ADR 0042, section 4)", () => {
    const idle = { status: "idle" as const, refused: null, map: null };
    const controls = (over: Partial<ReturnType<typeof defaultPrefs>>, sameGrid: boolean, onPrefs: LinkControls["onPrefs"] = () => {}): LinkControls => {
      const prefs = { ...defaultPrefs(), ...over };
      const group = linkGroup(prefs, sameGrid);
      return { group, prefs, onPrefs, pills: group ? linkPills(group, idle, idle) : [], channelLine: null };
    };
    const different = { compatible: false, reasons: ["axis 1 step differs"] };

    it("different grids are not linked until the person links them by WCS", async () => {
      const patches: unknown[] = [];
      render(<CompareBar datasets={all} selectedId={1} compareId={2} onChange={() => {}} compat={different} loading={false} error={null} link={controls({}, false, (p) => patches.push(p))} />);
      expect(screen.getByText(/not linked, nothing is resampled/)).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Link by WCS" }));
      expect(patches).toEqual([{ wcs: true, members: { a: true, b: true } }]);
      expect(screen.queryByRole("group", { name: "Link group" })).toBeNull();
    });

    it("a linked pair shows its name, facets and one Unlink per pane; the same-grid pill keeps its words", async () => {
      const patches: Record<string, unknown>[] = [];
      render(<CompareBar datasets={all} selectedId={1} compareId={2} onChange={() => {}} compat={{ compatible: true, reasons: [] }} loading={false} error={null} link={controls({}, true, (p) => patches.push(p))} />);
      expect(screen.getByText("Same grid: cursor and channel are linked")).toBeInTheDocument();
      const box = screen.getByRole("group", { name: "Link group" });
      expect(box).toBeInTheDocument();
      expect(screen.getByLabelText("Group name")).toHaveValue("Compare");
      expect(screen.getByLabelText("Cursor")).toBeChecked();
      expect(screen.getByLabelText("View")).not.toBeChecked();
      await userEvent.click(screen.getByLabelText("View"));
      expect(patches.at(-1)).toMatchObject({ facets: { cursor: true, channel: true, view: true } });
      await userEvent.click(screen.getByRole("button", { name: "Unlink map B" }));
      expect(patches.at(-1)).toEqual({ members: { a: true, b: false } });
    });

    it("after a pane leaves, the pair says so and can be linked again", async () => {
      const patches: unknown[] = [];
      render(<CompareBar datasets={all} selectedId={1} compareId={2} onChange={() => {}} compat={{ compatible: true, reasons: [] }} loading={false} error={null} link={controls({ members: { a: false, b: true } }, true, (p) => patches.push(p))} />);
      expect(screen.getByText("Not linked: a pane left the group")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Link again" }));
      expect(patches).toEqual([{ members: { a: true, b: true } }]);
    });

    it("says the active pane's channel sentence, and the WCS pill's basis", () => {
      const prefs = { ...defaultPrefs(), wcs: true };
      const group = linkGroup(prefs, false)!;
      const pills = [{ text: "Linked by WCS: frames ICRS↔Galactic (astropy), cursor within 0.03 px in this view", tone: "ok" as const }];
      render(<CompareBar datasets={all} selectedId={1} compareId={2} onChange={() => {}} compat={different} loading={false} error={null} link={{ group, prefs, onPrefs: () => {}, pills, channelLine: "B channel 41 is 0.12 km/s from A's 8 km/s" }} />);
      expect(screen.getByText(/Linked by WCS: frames ICRS↔Galactic \(astropy\)/)).toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("B channel 41 is 0.12 km/s from A's 8 km/s");
    });
  });
});
