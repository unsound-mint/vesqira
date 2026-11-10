import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset } from "../api/catalog";
import { CompareBar, linkState } from "./CompareBar";

const ds = (id: number, name: string): Dataset => ({ id, name, path: "", sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, nature: "", unitDeclaration: null, hostId: 0, header: {} as Dataset["header"] });
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
});
