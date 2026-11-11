import { useReducer } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { Dataset } from "../api/catalog";
import type { GeometryMap } from "../scene/geometry";
import { DEFAULT_DISPLAY, initialPane, paneReducer } from "../scene/scene";
import type { PaneAction, Scene } from "../scene/scene";
import { OverlayPanel } from "./OverlayPanel";

const ds = (id: number, name: string, over: Record<string, unknown> = {}) =>
  ({ id, name, header: { bunit: "K", axes: [{ length: 32 }, { length: 24 }, { length: 64 }] }, interpretation: null, unitDeclaration: null, ...over }) as unknown as Dataset;
const datasets = [ds(1, "hi.fits"), ds(2, "co.fits"), ds(3, "psi.fits", { interpretation: { kind: "axial_orientation", unit: "deg", reliabilityId: null, frame: "pixel", quantity: "b_field" } })];

/** The panel over the real pane reducer, so a test sees what each action does to the scene, not only that it was sent. */
function Harness({ geometry = {}, onScene }: { geometry?: GeometryMap; onScene: (s: Scene) => void }) {
  const [pane, dispatch] = useReducer(paneReducer, null, () => {
    const p = initialPane(1, false);
    return { ...p, scene: { ...p.scene!, base: { ...p.scene!.base, display: DEFAULT_DISPLAY } } };
  });
  onScene(pane.scene!);
  return <OverlayPanel scene={pane.scene} dispatch={dispatch as (a: PaneAction) => void} geometry={geometry} datasets={datasets} />;
}

function rows() {
  return within(screen.getByRole("list", { name: /Overlays/ })).getAllByRole("listitem");
}

describe("the overlay list", () => {
  it("adds contours from another dataset at k x sigma in the file's unit, then reorders, hides and removes them", () => {
    let scene: Scene | null = null;
    render(<Harness onScene={(s) => (scene = s)} />);
    expect(rows()).toHaveLength(1); // the base map's regions
    fireEvent.click(screen.getByRole("button", { name: "Add overlay" }));
    fireEvent.change(screen.getByLabelText("From dataset"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Channel (1 to 64)"), { target: { value: "41" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(scene!.overlays.map((o) => o.id)).toEqual(["regions", "contours-1"]);
    expect(scene!.overlays[1]).toEqual({ kind: "contours", id: "contours-1", datasetId: 2, visible: true, color: "amber", channel: 40, levels: { mode: "sigma", multiples: [3, 5, 10] }, unit: "K" });
    expect(rows()[1]).toHaveTextContent("Contours of co.fits");
    expect(rows()[1]).toHaveTextContent("Computing its placement");

    // reorder: drawing the contours lower puts them under the regions
    fireEvent.click(screen.getByRole("button", { name: "Draw Contours of co.fits lower (earlier)" }));
    expect(scene!.overlays.map((o) => o.id)).toEqual(["contours-1", "regions"]);
    expect(screen.getByRole("button", { name: "Draw Contours of co.fits lower (earlier)" })).toBeDisabled();

    // hide, then remove; the base map's regions can be hidden but not removed
    fireEvent.click(within(rows()[0]!).getByRole("switch"));
    expect(scene!.overlays[0]!.visible).toBe(false);
    expect(rows()[0]).toHaveTextContent("Hidden.");
    fireEvent.click(screen.getByRole("button", { name: "Remove Contours of co.fits" }));
    expect(scene!.overlays.map((o) => o.id)).toEqual(["regions"]);
    expect(screen.queryByRole("button", { name: /Remove Regions/ })).toBeNull();
  });

  it("says why an overlay is not drawn, and refuses a bad level list next to the field", () => {
    let scene: Scene | null = null;
    const geometry: GeometryMap = { "beam-1": { key: "k", status: "refused", reason: "the header of the overlay map has no BMAJ and BMIN, so there is no beam to draw (a beam is never assumed)" } };
    render(<Harness geometry={geometry} onScene={(s) => (scene = s)} />);
    fireEvent.click(screen.getByRole("button", { name: "Add overlay" }));
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "beam" } });
    fireEvent.change(screen.getByLabelText("Beam of"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(scene!.overlays.at(-1)).toMatchObject({ id: "beam-1", kind: "beam", datasetId: 2 });
    expect(rows()[1]).toHaveTextContent("Not drawn: the header of the overlay map has no BMAJ and BMIN");

    fireEvent.click(screen.getByRole("button", { name: "Add overlay" }));
    fireEvent.change(screen.getByLabelText("Levels"), { target: { value: "list" } });
    fireEvent.change(screen.getByLabelText("Values in K"), { target: { value: "5, 2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByRole("alert")).toHaveTextContent("each larger than the last");
    expect(scene!.overlays).toHaveLength(2);
  });

  it("offers orientation lines only from maps declared axial, and edits an overlay in place", () => {
    let scene: Scene | null = null;
    render(<Harness onScene={(s) => (scene = s)} />);
    fireEvent.click(screen.getByRole("button", { name: "Add overlay" }));
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "orientation" } });
    const from = screen.getByLabelText("From dataset") as HTMLSelectElement;
    expect(Array.from(from.options).map((o) => o.textContent)).toEqual(["psi.fits"]);
    expect(screen.getByLabelText("Length")).toBeDisabled(); // no reliability map is declared with it
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(scene!.overlays.at(-1)).toMatchObject({ kind: "orientation", datasetId: 3, sampling: { mode: "auto", gapPx: 16 }, color: "cyan" });

    fireEvent.click(screen.getByRole("button", { name: "Edit Orientation lines of psi.fits" }));
    fireEvent.change(screen.getByLabelText("Spacing"), { target: { value: "step" } });
    fireEvent.change(screen.getByLabelText("Every n-th pixel"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Colour"), { target: { value: "lime" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(scene!.overlays.at(-1)).toMatchObject({ id: "orientation-1", sampling: { mode: "step", step: 5 }, color: "lime" });
    expect(scene!.overlays).toHaveLength(2);
  });
});
