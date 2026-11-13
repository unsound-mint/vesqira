import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, vi } from "vitest";
import type { Need, ProjectSummary, TreeAnswer } from "../api/projects";
import * as api from "../api/projects";
import { groupNeeds } from "./needGroups";
import { NeedsView } from "./NeedsView";

vi.mock("../api/projects", async (orig) => ({
  ...(await orig<typeof import("../api/projects")>()),
  projectNeeds: vi.fn(),
  answerTree: vi.fn(),
  answerNeed: vi.fn(),
}));

const bunit = (folder: string, count = 1, state: Need["state"] = "open"): Need => ({
  id: `bunit:${folder}`, kind: "missing_bunit", scope: { paths: [], count, folder }, message: `${count} FITS file(s) in ${folder} have no BUNIT.`, source: "the FITS headers: no BUNIT card",
  answers: [{ id: "declare", label: "Declare a unit", input: "text" }, { id: "unknown", label: "Leave unknown", input: "" }], state, answer: null,
});
const vcs: Need = { id: "vcs", kind: "no_version_control", scope: { paths: [], count: 0 }, message: "no git", source: "no .git", answers: [{ id: "unknown", label: "Leave unknown", input: "" }], state: "open", answer: null };
const project = { id: 4, name: "p", hostId: 0 } as unknown as ProjectSummary;

beforeEach(() => vi.resetAllMocks());

describe("grouping the missing-unit questions", () => {
  const needs = [
    bunit("validation_output/rohsa/a", 2), vcs, bunit("validation_output/rohsa/b", 3), bunit("validation_output/fg/c", 1),
    bunit("data/only", 4), bunit("sims/x", 1, "answered"), bunit("sims/y", 1), bunit("sims/z", 1),
  ];

  it("groups by top-level folder, keeps a lone folder and other kinds plain, and never groups an answered one", () => {
    const items = groupNeeds(needs);
    expect(items.map((i) => (i.kind === "group" ? `group ${i.group.key}` : i.need.id))).toEqual(["group validation_output", "vcs", "bunit:data/only", "bunit:sims/x", "group sims"]);
    const g = items[0]!;
    if (g.kind !== "group") throw new Error("not a group");
    expect(g.group).toMatchObject({ ancestor: "validation_output", folders: 3, files: 6 });
    // the answer can be scoped to a child folder that holds more than one question, with its own counts
    expect(g.group.scopes).toEqual([{ folder: "validation_output", folders: 3, files: 6 }, { folder: "validation_output/rohsa", folders: 2, files: 5 }]);
    const sims = items[4]!;
    if (sims.kind !== "group") throw new Error("not a group");
    expect(sims.group.needs.map((n) => n.id)).toEqual(["bunit:sims/y", "bunit:sims/z"]);
  });

  it("uses the deepest shared folder as the default scope", () => {
    const [g] = groupNeeds([bunit("v/o/a"), bunit("v/o/b")]);
    expect(g!.kind === "group" && g!.group.ancestor).toBe("v/o");
  });
});

describe("answering a group", () => {
  const reply = (over: Partial<TreeAnswer>): TreeAnswer => ({ folder: "validation_output", applied: false, folders: 3, files: 6, sample: [], answered: 1, writtenToProject: false, open: 2, ...over });

  it("counts first, says what is left alone, and saves only after the confirmation", async () => {
    vi.mocked(api.projectNeeds).mockResolvedValue({ needs: needs3(), open: 3 });
    vi.mocked(api.answerTree).mockResolvedValueOnce(reply({})).mockResolvedValueOnce(reply({ applied: true, folders: 3, open: 0 }));
    const onChanged = vi.fn();
    render(<NeedsView project={project} version={0} onChanged={onChanged} />);
    await screen.findByText(/in 3 folders under/);
    await userEvent.type(screen.getByLabelText(/Every file under validation_output without BUNIT is in/), "K");
    await userEvent.click(screen.getByRole("button", { name: "Count the files" }));
    expect(api.answerTree).toHaveBeenLastCalledWith(4, "validation_output", "declare", "K", false);
    expect(await screen.findByText(/6 files in 3 folders under validation_output would be declared as K/)).toBeInTheDocument();
    expect(screen.getByText(/1 folder you already answered is left as it is/)).toBeInTheDocument();
    expect(api.answerTree).toHaveBeenCalledTimes(1); // nothing was confirmed yet
    await userEvent.click(screen.getByRole("button", { name: "Confirm for 3 folders" }));
    await waitFor(() => expect(api.answerTree).toHaveBeenLastCalledWith(4, "validation_output", "declare", "K", true));
    expect(onChanged).toHaveBeenCalled();
    expect(await screen.findByText(/Saved for 3 folders under validation_output, as declared by you/)).toBeInTheDocument();
  });

  it("cancel leaves everything as it was, and the folders can still be answered one by one", async () => {
    vi.mocked(api.projectNeeds).mockResolvedValue({ needs: needs3(), open: 3 });
    vi.mocked(api.answerTree).mockResolvedValue(reply({}));
    render(<NeedsView project={project} version={0} onChanged={vi.fn()} />);
    await screen.findByText(/in 3 folders under/);
    await userEvent.click(screen.getByRole("button", { name: "Leave unknown for all" }));
    await screen.findByText(/would be left with no unit/);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("button", { name: /Confirm for/ })).toBeNull();
    expect(api.answerTree).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: /one by one/ }));
    const list = screen.getByRole("list", { name: "Folders under validation_output" });
    expect(within(list).getAllByRole("listitem").length).toBeGreaterThanOrEqual(3);
  });
});

function needs3(): Need[] {
  return [bunit("validation_output/rohsa/a", 2), bunit("validation_output/rohsa/b", 3), bunit("validation_output/fg/c", 1)];
}
