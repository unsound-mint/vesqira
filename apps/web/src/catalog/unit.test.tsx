import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { ImageDataset } from "../api/catalog";
import { DatasetInspector } from "./DatasetInspector";
import { UnitControl } from "./UnitControl";

const header = (over: Record<string, unknown> = {}) => ({ naxis: 2, bitpix: -32, bunit: null, bunit_status: "unresolved", specsys: null, radesys: null, axes: [], ...over });
const ds = (over: Partial<ImageDataset> = {}, h: Record<string, unknown> = {}): ImageDataset =>
  ({ id: 7, path: "/data/a.fits", name: "a.fits", sizeBytes: 10, modifiedAt: "", addedAt: "", hostId: 0, nature: "", interpretation: null, unitDeclaration: null, header: header(h), ...over }) as unknown as ImageDataset;
const declared = (over: Record<string, unknown> = {}) => ({ ...{} as object, unit: "Jy/beam", declaredBy: "user", tier: "declared" as const, mappingVersion: "", declaredAt: "2026-11-10T10:00:00Z", stale: false, ...over });

describe("the unit of the values", () => {
  it("is shown as the file's, as yours, or as unresolved: never mixed up", () => {
    const { rerender } = render(<DatasetInspector d={ds({}, { bunit: "K", bunit_status: "declared" })} />);
    expect(screen.getByText("K")).toBeInTheDocument();
    rerender(<DatasetInspector d={ds({ unitDeclaration: declared() })} />);
    expect(screen.getByText("Jy/beam (declared by you, not in the file)")).toBeInTheDocument();
    rerender(<DatasetInspector d={ds()} />);
    expect(screen.getByText("— (unresolved)")).toBeInTheDocument();
  });

  it("treats a header with a unit but no status as stating one", () => {
    const { container } = render(<UnitControl dataset={ds({}, { bunit: "K", bunit_status: undefined })} onDeclare={vi.fn()} onWithdraw={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("offers nothing for a file that states its own unit", () => {
    const { container } = render(<UnitControl dataset={ds({}, { bunit: "K", bunit_status: "declared" })} onDeclare={vi.fn()} onWithdraw={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("declares what was typed, trimmed, and says it is yours and the file is not changed", async () => {
    const onDeclare = vi.fn().mockResolvedValue(null);
    const user = userEvent.setup();
    render(<UnitControl dataset={ds()} onDeclare={onDeclare} onWithdraw={vi.fn()} />);
    expect(screen.getByText(/Vesqira does not guess one/)).toBeInTheDocument();
    expect(screen.getByText(/never as the file's/)).toBeInTheDocument();
    const declare = screen.getByRole("button", { name: "Declare" });
    expect(declare).toBeDisabled(); // nothing typed: nothing to declare
    await user.type(screen.getByLabelText("Declare the unit"), "  Jy/beam ");
    await user.click(declare);
    expect(onDeclare).toHaveBeenCalledWith(7, "Jy/beam");
    await waitFor(() => expect(screen.getByLabelText("Declare the unit")).toHaveValue(""));
  });

  it("shows what the server refused and keeps what was typed", async () => {
    const onDeclare = vi.fn().mockResolvedValue("the file states its own unit (BUNIT); a declared unit only fills a missing one");
    const user = userEvent.setup();
    render(<UnitControl dataset={ds()} onDeclare={onDeclare} onWithdraw={vi.fn()} />);
    await user.type(screen.getByLabelText("Declare the unit"), "K");
    await user.click(screen.getByRole("button", { name: "Declare" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("states its own unit");
    expect(screen.getByLabelText("Declare the unit")).toHaveValue("K");
  });

  it("offers nothing for an orientation map: its values are angles, not a brightness", () => {
    const axial = ds({ interpretation: { kind: "axial_orientation", unit: "deg", reliabilityId: null, declaredBy: "user", declaredAt: "", stale: false } as never });
    const { container } = render(<UnitControl dataset={axial} onDeclare={vi.fn()} onWithdraw={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
    render(<DatasetInspector d={axial} />);
    expect(screen.getByText(/orientation map: values are angles, not a brightness/)).toBeInTheDocument();
  });

  it("limits the field to what the server accepts", () => {
    render(<UnitControl dataset={ds()} onDeclare={vi.fn()} onWithdraw={vi.fn()} />);
    expect(screen.getByLabelText("Declare the unit")).toHaveAttribute("maxlength", "40");
  });

  it("shows a declared unit as yours with its date and can withdraw it", async () => {
    const onWithdraw = vi.fn().mockResolvedValue(null);
    const user = userEvent.setup();
    render(<UnitControl dataset={ds({ unitDeclaration: declared() })} onDeclare={vi.fn()} onWithdraw={onWithdraw} />);
    expect(screen.getByText("Jy/beam")).toBeInTheDocument();
    expect(screen.getByText(/declared by you/)).toBeInTheDocument();
    expect(screen.getByText(/The file has no BUNIT\. This is your declaration/)).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Withdraw the unit" }));
    expect(onWithdraw).toHaveBeenCalledWith(7);
  });

  it("says a stale declaration is not being used, and lets you confirm it with one press or withdraw it", async () => {
    const onDeclare = vi.fn().mockResolvedValue(null);
    const user = userEvent.setup();
    render(<UnitControl dataset={ds({ unitDeclaration: declared({ stale: true }) })} onDeclare={onDeclare} onWithdraw={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("so it is not being used");
    await user.click(screen.getByRole("button", { name: "It still applies" }));
    expect(onDeclare).toHaveBeenCalledWith(7, "Jy/beam");
  });

  it("does not present a stale unit as the dataset's unit", () => {
    render(<DatasetInspector d={ds({ unitDeclaration: declared({ stale: true }) })} />);
    expect(screen.getByText(/unresolved; you declared Jy\/beam for an earlier version of the file, not applied/)).toBeInTheDocument();
    expect(screen.queryByText("Jy/beam (declared by you, not in the file)")).toBeNull();
  });
});
