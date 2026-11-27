import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ApplyPreview, ApplyResult, Dataset } from "../api/catalog";
import { ApplyToRuns, reasonCounts } from "./ApplyToRuns";
import type { ApplyActions } from "./ApplyToRuns";

const declared = (extra: object = {}): Dataset =>
  ({ id: 1, name: "psi_a.fits", path: "/a/psi.fits", interpretation: { kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: "pixel", quantity: "b_field", declaredBy: "user", tier: "declared", mappingVersion: "", declaredAt: "", stale: false, ...extra } }) as unknown as Dataset;

const row = (datasetId: number, outcome: "declare" | "declared" | "skipped" | "refused", reason?: string) => ({ datasetId, name: `psi_${datasetId}.fits`, runName: `run${datasetId}`, outcome, reason, reliability: null });
const preview: ApplyPreview = {
  eligible: true, scope: "project", scopes: [{ scope: "project", label: "Project LMC", runs: 5 }, { scope: "folder", label: "this folder", runs: 2 }],
  source: { datasetId: 1, name: "psi_a.fits", runName: "run1", profile: "profile:VGT.jl@1", unit: "rad", frame: "pixel", quantity: "b_field" },
  rows: [row(2, "declare"), row(3, "declare"), row(4, "skipped", "already declared by you"), row(5, "skipped", "already declared by you"), row(6, "skipped", "the file’s own header states the convention")],
  counts: { declare: 2, skipped: 3, refused: 0 },
};
const result: ApplyResult = {
  applyId: "ap1", scope: "project",
  rows: [row(2, "declared"), { ...row(3, "refused", "values run to 9.1, beyond 2π, so they are not radians") }, row(4, "skipped", "already declared by you")],
  counts: { declared: 1, skipped: 1, refused: 1 },
};

function actions(over: Partial<ApplyActions> = {}): ApplyActions & { applied: unknown[]; undone: string[] } {
  const applied: unknown[] = [];
  const undone: string[] = [];
  return { applied, undone, preview: async () => preview, apply: async (...a) => (applied.push(a), result), undo: async (id) => (undone.push(id), { applyId: id, removed: 1 }), ...over };
}

describe("apply to the other runs", () => {
  it("previews counts and groups the reasons, writes nothing until confirmed, then reports and undoes", async () => {
    const a = actions();
    render(<ApplyToRuns dataset={declared()} actions={a} />);
    expect(await screen.findByText(/Would declare 2; skip 3; refuse 0\./)).toBeInTheDocument();
    expect(screen.getByText(/2 × already declared by you/)).toBeInTheDocument();
    expect(screen.getByText(/1 × the file’s own header states the convention/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Apply to 2 runs/ }));
    expect(a.applied).toEqual([]);                                    // the first click only asks
    expect(screen.getByText(/declared by you and applied from psi_a\.fits; it is not verified/)).toBeInTheDocument();
    expect(screen.getByText(/checked against radians now/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(a.applied).toEqual([]);

    await userEvent.click(screen.getByRole("button", { name: /Apply to 2 runs/ }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm: declare 2" }));
    expect(a.applied).toEqual([[1, "project", [2, 3]]]);              // only the previewed declarable targets
    expect(await screen.findByText(/Declared 1; skipped 1; refused 1\./)).toBeInTheDocument();
    expect(screen.getByText(/1 × values run to 9\.1, beyond 2π/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Undo this apply" }));
    expect(a.undone).toEqual(["ap1"]);
    expect(await screen.findByText(/Removed 1 declaration\./)).toBeInTheDocument();
  });

  it("offers nothing for a map that is not a declaration made directly by the person", () => {
    const a = actions();
    const { container, rerender } = render(<ApplyToRuns dataset={declared({ declaredBy: "vgt-run-adapter", tier: "inferred" })} actions={a} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<ApplyToRuns dataset={declared({ applyId: "ap0", appliedFrom: 9 })} actions={a} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<ApplyToRuns dataset={declared({ stale: true })} actions={a} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("offers nothing when the server says the declaration cannot be applied", async () => {
    const a = actions({ preview: async () => ({ ...preview, eligible: false, reason: "not an output of a run", source: null, scopes: [], rows: [], counts: {} }) });
    const { container } = render(<ApplyToRuns dataset={declared()} actions={a} />);
    await new Promise((r) => setTimeout(r, 0));
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the server's error when an apply fails, and keeps the preview", async () => {
    const a = actions({ apply: async () => { throw new Error("boom"); } });
    render(<ApplyToRuns dataset={declared()} actions={a} />);
    await userEvent.click(await screen.findByRole("button", { name: /Apply to 2 runs/ }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm: declare 2" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/Could not reach the server/);
    expect(screen.getByText(/Would declare 2/)).toBeInTheDocument();
  });

  it("counts rows of one outcome by reason, most common first", () => {
    expect(reasonCounts(preview.rows, "skipped")).toEqual([{ reason: "already declared by you", n: 2 }, { reason: "the file’s own header states the convention", n: 1 }]);
  });
});
