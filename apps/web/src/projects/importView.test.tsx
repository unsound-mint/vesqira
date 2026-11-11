import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, vi } from "vitest";
import { ApiError } from "../api/http";
import type { ImportApplyReply, ImportPreview, ImportRule, ImportRulesState, ProjectSummary } from "../api/projects";
import * as api from "../api/projects";
import { ruleOf } from "../api/declared";
import { ImportView } from "./ImportView";
import { RuleEditor } from "./RuleEditor";
import { forRole, localProblems, sameRules, tidy } from "./importFormat";

vi.mock("../api/projects", async (orig) => ({
  ...(await orig<typeof import("../api/projects")>()),
  importRules: vi.fn(),
  importPreview: vi.fn(),
  saveImportRules: vi.fn(),
  writeImportRules: vi.fn(),
  importApply: vi.fn(),
}));

const psi: ImportRule = { name: "psi maps", kind: "glob", pattern: "vda/*/vgt/psi_*.fits", role: "orientation", angleUnit: "rad", angleFrame: "pixel", quantity: "b_field" };
const project = { id: 3, name: "owner", importRules: null } as unknown as ProjectSummary;

const preview = (over: Partial<ImportPreview> = {}): ImportPreview => ({
  folder: "", scannedAt: "2026-11-12T10:00:00Z", entries: 20, truncated: false, truncatedWhy: "", othersTruncated: false, notes: [], files: 9,
  groups: [
    { role: "dataset", ext: ".fits", by: "built-in", why: "FITS file: a self-describing format, registered as a dataset", count: 5, bytes: 28800, samples: [{ path: "data/a.fits", size: 5760, mtimeNs: 1, role: "dataset", why: "FITS file", proposedRole: "dataset", by: "built-in" }] },
    { role: "other", ext: ".dat", by: "built-in", why: "no built-in rule recognises this file", count: 2, bytes: 24, samples: [] },
  ],
  rulesFrom: "saved", rules: [], conflicts: [], conflictCount: 0, selected: { files: 0, byRole: {}, pending: 0, applied: 0 },
  skipped: { dirs: [], dirCount: 0, neverRead: ["blind_set_key"], archiveCount: 1, archiveBytes: 10, links: 0 }, ...over,
});
const state = (rules: ImportRule[], file: ImportRulesState["file"]["state"] = "none"): ImportRulesState => ({ rules, file: { state: file }, summary: null });

beforeEach(() => {
  vi.resetAllMocks();
});

describe("the editor helpers", () => {
  it("never default a unit, a frame or a quantity, and say what is missing", () => {
    expect(localProblems({ name: "o", kind: "glob", pattern: "*.fits", role: "orientation" })).toEqual(["Choose the angle unit: Vesqira does not guess degrees or radians."]);
    const cat = localProblems({ name: "c", kind: "glob", pattern: "*.dat", role: "catalog", lon: "RA", lat: "DEC" });
    expect(cat).toContain("Choose the coordinate frame: the file does not say and there is no default.");
    expect(cat).toContain("Choose how the positions are written.");
    expect(localProblems({ name: "c", kind: "glob", pattern: "*.dat", role: "catalog", lon: "a", lat: "b", coordFormat: "degrees", coordFrame: "fk5" })).toEqual(["An fk4 or fk5 frame needs an equinox (B1950, J2000, ...)."]);
    expect(localProblems(psi)).toEqual([]);
  });

  it("drops the declarations a role does not take when the role changes", () => {
    expect(forRole(psi, "dataset")).toEqual({ name: "psi maps", kind: "glob", pattern: "vda/*/vgt/psi_*.fits", role: "dataset", unit: undefined });
    expect(tidy(forRole(psi, "ignore"))).toEqual({ name: "psi maps", kind: "glob", pattern: "vda/*/vgt/psi_*.fits", role: "ignore" });
  });

  it("compares rule lists by what they say, in order", () => {
    expect(sameRules([psi], [{ ...psi, quantity: "" }])).toBe(false);
    expect(sameRules([psi, { ...psi, name: "b" }], [psi, { ...psi, name: "b", unit: "" }])).toBe(true);
    expect(sameRules([psi, { ...psi, name: "b" }], [{ ...psi, name: "b" }, psi])).toBe(false);
  });

  it("reads who made a declaration", () => {
    expect(ruleOf("rule:psi maps")).toBe("psi maps");
    expect(ruleOf("user")).toBeNull();
    expect(ruleOf("vgt-run-adapter")).toBeNull();
  });
});

describe("a rule", () => {
  const report = { name: "psi maps", matches: 3, wins: 2, shadowed: 1, examples: ["vda/n3/vgt/psi_1.fits"] };

  it("says how many files it matches, which it decides, and shows examples", () => {
    render(<RuleEditor rule={psi} index={0} count={2} report={report} onChange={vi.fn()} onMove={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("3 files match · 2 are decided by this rule, 1 by an earlier one");
    expect(screen.getByRole("list", { name: "Files psi maps matches" })).toHaveTextContent("vda/n3/vgt/psi_1.fits");
  });

  it("shows a pattern error and no matches", () => {
    render(<RuleEditor rule={psi} index={0} count={1} report={{ name: "psi maps", error: "pattern is not a valid regular expression", matches: 0, wins: 0, shadowed: 0, examples: [] }} onChange={vi.fn()} onMove={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("pattern is not a valid regular expression");
  });

  it("requires the angle unit of an orientation map: nothing is preselected", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<RuleEditor rule={{ name: "o", kind: "glob", pattern: "*.fits", role: "orientation" }} index={0} count={1} report={undefined} onChange={onChange} onMove={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByLabelText("Angle unit")).toHaveValue("");
    expect(screen.getByLabelText("Angle frame")).toHaveValue("");
    expect(screen.getByRole("list", { name: "To fix before saving" })).toHaveTextContent("does not guess degrees or radians");
    await user.selectOptions(screen.getByLabelText("Angle unit"), "rad");
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ angleUnit: "rad" }));
  });

  it("asks for a catalog's frame and the equinox only for fk4 and fk5", async () => {
    const base: ImportRule = { name: "c", kind: "glob", pattern: "stars/*.dat", role: "catalog", lon: "RA", lat: "DEC" };
    const { rerender } = render(<RuleEditor rule={base} index={0} count={1} report={undefined} onChange={vi.fn()} onMove={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByLabelText("Coordinate frame")).toHaveValue("");
    expect(screen.queryByLabelText("Equinox")).toBeNull();
    rerender(<RuleEditor rule={{ ...base, coordFrame: "fk5" }} index={0} count={1} report={undefined} onChange={vi.fn()} onMove={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByLabelText("Equinox")).toBeInTheDocument();
  });

  it("moves and removes by name", async () => {
    const onMove = vi.fn();
    const onRemove = vi.fn();
    const user = userEvent.setup();
    render(<RuleEditor rule={psi} index={1} count={3} report={report} onChange={vi.fn()} onMove={onMove} onRemove={onRemove} />);
    await user.click(screen.getByRole("button", { name: "Move psi maps up" }));
    await user.click(screen.getByRole("button", { name: "Move psi maps down" }));
    await user.click(screen.getByRole("button", { name: "Remove psi maps" }));
    expect(onMove.mock.calls).toEqual([[-1], [1]]);
    expect(onRemove).toHaveBeenCalled();
  });
});

describe("the import page", () => {
  it("lists what is and is not recognised with the reason, and the skipped", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([]));
    vi.mocked(api.importPreview).mockResolvedValue(preview());
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    const groups = await screen.findByRole("list", { name: "Files by role and extension" });
    expect(within(groups).getByText("Dataset (FITS)")).toBeInTheDocument();
    expect(within(groups).getByText("Not recognised")).toBeInTheDocument();
    expect(within(groups).getByText("FITS file: a self-describing format, registered as a dataset")).toBeInTheDocument();
    expect(screen.getByText(/1 never-read name/)).toBeInTheDocument();
    // the first scan is a real walk of the project, with the saved rules
    expect(api.importPreview).toHaveBeenCalledWith(3, expect.objectContaining({ folder: "", rescan: true, rules: undefined }));
  });

  it("says when the walk stopped early", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([]));
    vi.mocked(api.importPreview).mockResolvedValue(preview({ truncated: true, truncatedWhy: "stopped descending below 24 folder levels", othersTruncated: true }));
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    expect(await screen.findByText(/incomplete: stopped descending below 24 folder levels/)).toBeInTheDocument();
    expect(screen.getByText(/more unrecognised files exist than are listed/)).toBeInTheDocument();
  });

  it("previews unsaved edits live, never saves them by itself, and registers only what is saved", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([psi]));
    vi.mocked(api.importPreview).mockImplementation(async (_id, body) =>
      preview({ rulesFrom: body.rules ? "draft" : "saved", rules: (body.rules ?? [psi]).map((r) => ({ name: r.name, matches: r.pattern === "" ? 0 : 3, wins: 3, shadowed: 0, examples: [] })), selected: { files: 3, byRole: { orientation: 3 }, pending: 3, applied: 0 } }));
    const user = userEvent.setup();
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    const register = await screen.findByRole("button", { name: "Register 3 files" });
    await waitFor(() => expect(register).toBeEnabled());
    expect(screen.getByRole("button", { name: "Save rules" })).toBeDisabled();
    await user.clear(screen.getByLabelText("Pattern on the path from the project folder"));
    await user.type(screen.getByLabelText("Pattern on the path from the project folder"), "vda/**/psi_*.fits");
    // the draft is sent to the server's matcher; saving is separate
    await waitFor(() => expect(api.importPreview).toHaveBeenLastCalledWith(3, expect.objectContaining({ rules: [expect.objectContaining({ pattern: "vda/**/psi_*.fits" })] })));
    expect(await screen.findByText(/matched against your unsaved edits/)).toBeInTheDocument();
    expect(api.saveImportRules).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /Register/ })).toBeDisabled();
    expect(screen.getByText("Save the rules first.")).toBeInTheDocument();

    vi.mocked(api.saveImportRules).mockResolvedValue(state([{ ...psi, pattern: "vda/**/psi_*.fits" }]));
    await user.click(screen.getByRole("button", { name: "Save rules" }));
    expect(api.saveImportRules).toHaveBeenCalledWith(3, [expect.objectContaining({ name: "psi maps", pattern: "vda/**/psi_*.fits", angleUnit: "rad" })]);
    expect(await screen.findByText(/Saved 1 rule in the catalog. project.toml is unchanged./)).toBeInTheDocument();
  });

  it("shows every reason a save was refused and keeps the edits", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([]));
    vi.mocked(api.importPreview).mockResolvedValue(preview());
    vi.mocked(api.saveImportRules).mockRejectedValue(new ApiError("the rules are not valid", ["rule 1 (rule 1): pattern: is empty", "rule 1 (rule 1): angle unit is required"], 422));
    const user = userEvent.setup();
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "Add a rule" }));
    await user.click(screen.getByRole("button", { name: "Save rules" }));
    const problems = await screen.findByRole("alert", { name: "Problems" });
    expect(problems).toHaveTextContent("pattern: is empty");
    expect(problems).toHaveTextContent("angle unit is required");
    expect(screen.getByLabelText("Rule 1: rule 1", { exact: false })).toBeInTheDocument();
  });

  it("lists the conflicts: files two rules match, and who decides", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([psi, { ...psi, name: "all" }]));
    vi.mocked(api.importPreview).mockResolvedValue(preview({ conflictCount: 3, conflicts: [{ path: "vda/n3/vgt/psi_1.fits", rules: ["psi maps", "all"], winner: "psi maps", differ: true }], rules: [] }));
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    const list = await screen.findByRole("list", { name: "Files matched by more than one rule" });
    expect(list).toHaveTextContent("vda/n3/vgt/psi_1.fits: psi maps > all · they would say different things");
    expect(list).toHaveTextContent("and 2 more");
    expect(screen.getByText(/3 files match more than one rule; the first rule decides/)).toBeInTheDocument();
  });

  it("writes project.toml only after a second, explicit click", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([psi], "none"));
    vi.mocked(api.importPreview).mockResolvedValue(preview());
    vi.mocked(api.writeImportRules).mockResolvedValue({ written: true, path: ".vesqira/project.toml", rules: 1 });
    const user = userEvent.setup();
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "Write the rules to project.toml…" }));
    expect(api.writeImportRules).not.toHaveBeenCalled();
    expect(screen.getByText(/Only a marked section of that file changes. It is never committed./)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(api.writeImportRules).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Write the rules to project.toml…" }));
    await user.click(screen.getByRole("button", { name: "Write the rules" }));
    expect(api.writeImportRules).toHaveBeenCalledWith(3);
    expect(await screen.findByText(/Wrote 1 rule to .vesqira\/project.toml. Nothing was committed./)).toBeInTheDocument();
  });

  it("offers nothing to write for rules the researcher wrote in the file themselves", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([psi], "foreign"));
    vi.mocked(api.importPreview).mockResolvedValue(preview());
    render(<ImportView project={project} version={0} onChanged={vi.fn()} />);
    expect(await screen.findByText(/project.toml has \[\[import.rules\]\] you wrote there; Vesqira does not rewrite them./)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Write the rules/ })).toBeNull();
  });

  it("registers on a click and reports registered, skipped and duplicates with the reason; a duplicate is registered separately only on its own click", async () => {
    vi.mocked(api.importRules).mockResolvedValue(state([psi]));
    vi.mocked(api.importPreview).mockResolvedValue(preview({ selected: { files: 3, byRole: { orientation: 3 }, pending: 3, applied: 0 } }));
    const reply: ImportApplyReply = {
      results: [
        { path: "vda/n3/vgt/psi_1.fits", rule: "psi maps", role: "orientation", outcome: "registered", detail: "registered", datasetId: 4, declared: ["axial orientation in rad (declared by your rule psi maps)"] },
        { path: "vda/n3/vgt/psi_2.fits", rule: "psi maps", role: "orientation", outcome: "duplicate", detail: "A dataset already holds this content (dataset 2, verified). Not registered.", datasetId: null, declared: [], matches: [{ datasetId: 2, relation: "verified" }] },
        { path: "vda/n4/vgt/psi_1.fits", rule: "psi maps", role: "orientation", outcome: "skipped", detail: "kept your own orientation declaration", datasetId: 9, declared: [] },
      ],
      summary: { registered: 1, declared: 0, duplicates: 1, skipped: 1, failed: 0, remaining: 0 },
    };
    vi.mocked(api.importApply).mockResolvedValue(reply);
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(<ImportView project={project} version={0} onChanged={onChanged} />);
    const register = await screen.findByRole("button", { name: "Register 3 files" });
    await waitFor(() => expect(register).toBeEnabled());
    await user.click(register);
    expect(api.importApply).toHaveBeenCalledWith(3, { folder: "", paths: undefined, asNew: undefined });
    const summary = await screen.findByRole("status", { name: "Import summary" });
    expect(summary).toHaveTextContent("Registered 1 · declared on 0 already registered · 1 offered as duplicates · 1 skipped · 0 failed");
    const why = within(summary).getByRole("list", { name: "Files not registered, and why" });
    expect(why).toHaveTextContent("kept your own orientation declaration");
    expect(why).not.toHaveTextContent("psi_1.fits · registered");
    expect(onChanged).toHaveBeenCalled();
    vi.mocked(api.importApply).mockResolvedValue({ results: [{ ...reply.results[1]!, outcome: "registered", detail: "registered", datasetId: 11 }], summary: { registered: 1, declared: 0, duplicates: 0, skipped: 0, failed: 0, remaining: 0 } });
    await user.click(within(why).getByRole("button", { name: "Register separately" }));
    expect(api.importApply).toHaveBeenLastCalledWith(3, { folder: "", paths: ["vda/n3/vgt/psi_2.fits"], asNew: ["vda/n3/vgt/psi_2.fits"] });
  });

  it("says a scan found new files that match, with a way to review them", () => {
    const withNew = { ...project, importRules: { rules: 2, matched: 5, pending: 5, new: 2 } } as ProjectSummary;
    vi.mocked(api.importRules).mockResolvedValue(state([psi]));
    vi.mocked(api.importPreview).mockResolvedValue(preview());
    render(<ImportView project={withNew} version={0} onChanged={vi.fn()} />);
    expect(screen.getByText("2 new files match your rules.")).toBeInTheDocument();
  });
});
