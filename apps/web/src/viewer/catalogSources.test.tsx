import { useReducer, useState } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { Dataset } from "../api/catalog";
import { catalogRows } from "../api/catalog";
import { parseGeometry } from "../api/overlays";
import type { OverlayGeometryHead } from "../api/overlays";
import type { GeometryMap } from "../scene/geometry";
import { DEFAULT_DISPLAY, initialPane, paneReducer } from "../scene/scene";
import type { PaneAction, Scene } from "../scene/scene";
import { OverlayPanel } from "./OverlayPanel";
import { SourcePopover } from "./SourceRow";
import type { PickedSource } from "./SourceRow";
import { withFacts } from "../catalog/testDatasets";

vi.mock("../api/catalog", async (orig) => ({ ...(await orig<typeof import("../api/catalog")>()), catalogRows: vi.fn() }));

const placement = { method: "placed by WCS geometry (astropy)", from_frame: "ICRS", to_frame: "ICRS", same_frame: true, same_dataset: false };
function frame(head: Record<string, unknown>, nums: number[]): ArrayBuffer {
  let json = new TextEncoder().encode(JSON.stringify(head));
  const pad = (8 - ((4 + json.length) % 8)) % 8;
  json = new Uint8Array([...json, ...new Array<number>(pad).fill(32)]);
  const buf = new ArrayBuffer(4 + json.length + 4 * nums.length);
  const dv = new DataView(buf);
  dv.setUint32(0, json.length, true);
  new Uint8Array(buf, 4, json.length).set(json);
  nums.forEach((v, i) => dv.setFloat32(4 + json.length + 4 * i, v, true));
  return buf;
}
const head = { kind: "catalog", dtype: "<f4", coords: "", placement, markers: 3, counts: { rows: 4, no_position: 0, outside: 1, filtered: 0, drawn: 3 }, filter: null, vectors: null, catalog: { format: "tsv", frame: "ICRS", frame_status: "declared", skipped_lines: 0 },
  color: { column: "P(%)", unit: "%", min: 0.8, max: 1.3, unknown: 1, scale: "linear between the smallest and largest value drawn; a row with no value is drawn hollow" } } as unknown as OverlayGeometryHead;
// x, y, angle, length, colour value, row
const geometry: GeometryMap = { "catalog-1": { key: "k", status: "ready", g: parseGeometry(frame(head, [4, 5, NaN, NaN, 1.17, 1, 8, 9, NaN, NaN, NaN, 2, 12, 3, NaN, NaN, 0.79, 3])) } };

const col = (name: string, dtype = "float", unit: string | null = null) => ({ name, dtype, scalar: true, unit });
const catalogDs = withFacts({
  id: 7, name: "starlight.tsv", kind: "catalog", interpretation: null, unitDeclaration: null,
  header: { kind: "catalog", format: "tsv", rows: 4, columns: [col("RA"), col("DEC"), col("PA"), col("P(%)", "float", "%"), col("sP(%)")] },
  catalogDeclaration: { lon: "RA", lat: "DEC", format: "degrees", frame: "icrs", equinox: null, angle: { column: "PA", unit: "deg", frame: "sky", quantity: "starlight_polarization", sigmaColumn: null }, values: [], declaredBy: "user", declaredAt: "2026-11-11T00:00:00Z", stale: false },
}) as unknown as Dataset;
const base = withFacts({ id: 1, name: "hi.fits", header: { bunit: "K", axes: [{ length: 32 }, { length: 24 }] }, interpretation: null, unitDeclaration: null }) as unknown as Dataset;

function Harness({ onScene }: { onScene: (s: Scene) => void }) {
  const [pane, dispatch] = useReducer(paneReducer, null, () => {
    const p = initialPane(1, false);
    return { ...p, scene: { ...p.scene!, base: { ...p.scene!.base, display: DEFAULT_DISPLAY } } };
  });
  const [picked, setPicked] = useState<PickedSource | null>(null);
  onScene(pane.scene!);
  return <OverlayPanel scene={pane.scene} dispatch={dispatch as (a: PaneAction) => void} geometry={geometry} datasets={[base, catalogDs]} picked={picked} onPick={setPicked} />;
}

describe("a catalog in the overlay list", () => {
  beforeEach(() => {
    vi.mocked(catalogRows).mockReset();
    vi.mocked(catalogRows).mockImplementation(async (_id, offset) => ({
      offset, total: 4, columns: ["Star", "P(%)", "sP(%)"],
      rows: [offset === 1 ? ["69-7", null, 0.09] : [`s${offset + 1}`, 1.17, 0.08]], lines: [offset + 2],
    }));
  });

  it("adds a catalog with vectors, a colour column and a filter from its declaration, with the form refusing vectors until the angle is declared", () => {
    let scene: Scene | null = null;
    render(<Harness onScene={(s) => (scene = s)} />);
    fireEvent.click(screen.getByRole("button", { name: "Add overlay" }));
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "catalog" } });
    expect(screen.getByLabelText("Catalog")).toHaveValue("7");
    expect(screen.getByText(/Angles are east of north in the catalog's declared frame/)).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/Orientation vectors from the declared angle \(PA\)/));
    fireEvent.change(screen.getByLabelText("Vector length"), { target: { value: "P(%)" } });
    fireEvent.change(screen.getByLabelText("Colour markers by"), { target: { value: "sP(%)" } });
    fireEvent.change(screen.getByLabelText("Keep rows where"), { target: { value: "P(%)" } });
    fireEvent.change(screen.getByLabelText("divided by"), { target: { value: "sP(%)" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(scene!.overlays[1]).toEqual({ kind: "catalog", id: "catalog-1", datasetId: 7, visible: true, color: "lime", vectors: true, lengthColumn: "P(%)", colorColumn: "sP(%)", filter: { column: "P(%)", sigmaColumn: "sP(%)", min: 3 } });
  });

  it("states the colour range numerically and lists the drawn sources as buttons that pick a row and show its values, missing ones as missing", async () => {
    let scene: Scene | null = null;
    render(<Harness onScene={(s) => (scene = s)} />);
    fireEvent.click(screen.getByRole("button", { name: "Add overlay" }));
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "catalog" } });
    fireEvent.change(screen.getByLabelText("Colour markers by"), { target: { value: "P(%)" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(scene!.overlays.map((o) => o.kind)).toEqual(["regions", "catalog"]);
    const row = screen.getByText(/Catalog of starlight\.tsv/).closest("li")!;
    const legend = within(row).getByRole("group", { name: "Colour legend: P(%)" });
    expect(legend).toHaveTextContent("0.8");
    expect(legend).toHaveTextContent("1.3");
    fireEvent.click(within(row).getByText(/3 sources drawn: list/));
    const list = within(row).getByRole("list", { name: "Sources drawn by this overlay" });
    expect(within(list).getAllByRole("button").map((b) => b.textContent)).toEqual(["Row 1", "Row 2", "Row 3"]);
    expect(within(list).getAllByRole("listitem")[1]).toHaveTextContent("P(%): no value"); // an unknown value is said, not coloured only
    expect(within(list).getAllByRole("listitem")[0]).toHaveTextContent("P(%) 1.17");
    fireEvent.click(within(list).getByRole("button", { name: "Row 2" }));
    expect(within(list).getByRole("button", { name: "Row 2" })).toHaveAttribute("aria-pressed", "true");
    const detail = await within(row).findByRole("region", { name: "Row 2" });
    await waitFor(() => expect(within(detail).getByText("69-7")).toBeInTheDocument());
    expect(within(detail).getByText("missing")).toBeInTheDocument(); // a missing P is never shown as 0
    expect(within(detail).getByText(/Row 2, line 3 of the file/)).toBeInTheDocument();
    expect(catalogRows).toHaveBeenCalledWith(7, 1, 1);
    fireEvent.click(within(list).getByRole("button", { name: "Row 2" })); // pressing again clears it
    expect(within(row).queryByRole("region", { name: "Row 2" })).toBeNull();
  });
});

describe("the popover beside a clicked marker", () => {
  it("shows the row and its line, stays inside the picture and closes", async () => {
    vi.mocked(catalogRows).mockResolvedValue({ offset: 4, total: 9, columns: ["Star", "P(%)"], rows: [["67-2", 1.17]], lines: [7] });
    const onClose = vi.fn();
    render(<SourcePopover datasetId={7} row={5} name="Catalog source" at={{ x: 395, y: 10 }} frame={{ w: 400, h: 300 }} onClose={onClose} />);
    const dialog = screen.getByRole("dialog", { name: "Catalog source, row 5" });
    expect(Number.parseFloat(dialog.style.left)).toBeLessThanOrEqual(400 - 240 - 4); // pushed back inside the picture
    await waitFor(() => expect(within(dialog).getByText("67-2")).toBeInTheDocument());
    expect(within(dialog).getByText(/Row 5, line 7 of the file/)).toBeInTheDocument();
    expect(catalogRows).toHaveBeenCalledWith(7, 4, 1);
    fireEvent.click(within(dialog).getByRole("button", { name: "Close the row" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("says when the row cannot be read", async () => {
    vi.mocked(catalogRows).mockRejectedValue(new Error("nope"));
    render(<SourcePopover datasetId={7} row={1} name="Catalog source" at={{ x: 1, y: 1 }} frame={{ w: 400, h: 300 }} onClose={() => {}} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("The row could not be read.");
  });
});
