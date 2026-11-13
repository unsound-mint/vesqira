import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, vi } from "vitest";
import type { ImageDataset } from "../api/catalog";
import type { ImportPreview, ImportRule, ImportRulesState, ProjectSummary } from "../api/projects";
import * as api from "../api/projects";
import { DatasetInspector } from "../catalog/DatasetInspector";
import { UnitControl } from "../catalog/UnitControl";
import { ImportView } from "./ImportView";
import { withFacts } from "../catalog/testDatasets";

vi.mock("../api/projects", async (orig) => ({
  ...(await orig<typeof import("../api/projects")>()),
  importRules: vi.fn(),
  importPreview: vi.fn(),
  saveImportRules: vi.fn(),
  writeImportRules: vi.fn(),
  importApply: vi.fn(),
}));

const header = { naxis: 2, bitpix: -32, bunit: null, bunit_status: "unresolved", specsys: null, radesys: null, axes: [] };
const ds = (declaredBy: string): ImageDataset =>
  withFacts({ id: 7, path: "/data/a.fits", name: "a.fits", sizeBytes: 10, modifiedAt: "", addedAt: "", hostId: 0, nature: "", interpretation: null, header,
    unitDeclaration: { unit: "km/s", declaredBy, declaredAt: "2026-11-10T10:00:00Z", stale: false } }) as unknown as ImageDataset;

beforeEach(() => vi.resetAllMocks());

describe("a unit declared by an import rule", () => {
  it("is never labelled as the person's own or as the file's", () => {
    const { rerender } = render(<DatasetInspector d={ds("rule:kms")} />);
    expect(screen.getByText("km/s (declared by your rule kms, not in the file)")).toBeInTheDocument();
    rerender(<DatasetInspector d={ds("user")} />);
    expect(screen.getByText("km/s (declared by you, not in the file)")).toBeInTheDocument();
  });

  it("says in the unit control that the rule declared it, and offers the person to replace it", () => {
    render(<UnitControl dataset={ds("rule:kms")} onDeclare={vi.fn()} onWithdraw={vi.fn()} />);
    expect(screen.getByText("declared by your rule kms", { exact: false })).toBeInTheDocument();
    expect(screen.getByText(/declared by your import rule kms, not by you and not by the file/)).toBeInTheDocument();
  });
});

describe("removing the rules section", () => {
  const project = { id: 3, name: "owner", importRules: null } as unknown as ProjectSummary;
  const preview = {
    folder: "", scannedAt: "2026-11-12T10:00:00Z", entries: 1, truncated: false, truncatedWhy: "", othersTruncated: false, notes: [], files: 0, groups: [], rulesFrom: "saved",
    rules: [], conflicts: [], conflictCount: 0, selected: { files: 0, byRole: {}, pending: 0, applied: 0 }, skipped: { dirs: [], dirCount: 0, neverRead: [], archiveCount: 0, archiveBytes: 0, links: 0 },
  } as ImportPreview;

  it("is offered when every rule was deleted but project.toml still holds them, and is its own confirmed write", async () => {
    const none: ImportRule[] = [];
    vi.mocked(api.importRules).mockResolvedValue({ rules: none, file: { state: "differs" }, summary: null } as ImportRulesState);
    vi.mocked(api.importPreview).mockResolvedValue(preview);
    vi.mocked(api.writeImportRules).mockResolvedValue({ written: true, path: ".vesqira/project.toml", rules: 0 });
    const user = userEvent.setup();
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "Remove the rules from project.toml…" }));
    expect(api.writeImportRules).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Remove the rules" }));
    expect(api.writeImportRules).toHaveBeenCalledWith(3);
    expect(await screen.findByText(/Removed the rules section from .vesqira\/project.toml. Nothing else in the file changed/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Remove the rules/ })).toBeNull();
  });
});
