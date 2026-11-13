import { render, screen } from "@testing-library/react";
import type { ProjectSummary, TreeEntry } from "../api/projects";
import { FirstLook } from "./FirstLook";
import { ProjectExplorer } from "./ProjectExplorer";

const RUN = "vgt_Tb_combined_mw_full_hnv_res1_3pperb_reprojected_on_gass";
const run = (suffix: string): TreeEntry => ({
  name: `${RUN}_${suffix}`,
  path: `validation_output/fg_slabs/${RUN}_${suffix}`,
  kind: "run",
  count: null,
  file: null,
  run: { runId: 1, record: "run_metadata.toml", recordKind: "vgt", settings: [{ key: "input", value: `Tb_combined_${suffix}` }], differs: [{ key: "input", value: `Tb_combined_mw_full_hnv_res1_3pperb_reprojected_on_gass_${suffix}` }] },
} as unknown as TreeEntry);

vi.mock("../api/projects", async (orig) => ({
  ...(await orig<typeof import("../api/projects")>()),
  projectTree: async (_id: number, group: string) => ({
    group, path: "", total: 3, offset: 0, limit: 200, nextOffset: null,
    entries: group === "runs" ? [run("v2p0_to_8p0kms"), run("vm2p0_to_2p0kms"), run("v0p0_to_4p0kms")] : [],
  }),
}));

const project = (exists: boolean, hostId = 0): ProjectSummary =>
  ({
    id: 1, hostId, path: "/shared/home/me/vgt", name: "vgt", scanned: true,
    scan: { state: "done", entries: 1, toRegister: 0, registered: 0, failed: 0, startedAt: null, finishedAt: null, error: "" },
    counts: { runs: 3, data: 0, configs: 0, jobs: 0, manualRuns: 0, byFormat: {}, noViewer: 0, skippedDirs: 0, archives: { count: 0, bytes: 0 }, neverRead: 0 },
    git: { present: false, commit: "", dirty: 0 }, links: { outside: 0, dangling: 0, examples: [] },
    resultLike: [], registration: { failed: 0, errors: [] }, notes: [], changes: null, proposedProjectFile: null,
    projectFile: { exists, error: "", name: exists ? "vgt" : "", hostRepos: {} },
  }) as unknown as ProjectSummary;

describe("the project file, said once", () => {
  it("does not claim the file exists on the project page when the folder has none", () => {
    render(<FirstLook project={project(false)} hostName={() => "Irgetas"} onWrite={async () => null} onUseWithout={() => {}} onCancel={() => {}} />);
    expect(screen.queryByText(/The project file exists/)).toBeNull();
    expect(screen.getByText(/No \.vesqira\/project\.toml yet\./)).toBeInTheDocument();
  });

  it("says it exists, with its name, when it does", () => {
    render(<FirstLook project={project(true)} hostName={() => "Irgetas"} onWrite={async () => null} onUseWithout={() => {}} onCancel={() => {}} />);
    expect(screen.getByText(/The project file exists \(project name: vgt\)/)).toBeInTheDocument();
  });

  it("names the host of a project on one, not its id", () => {
    render(<FirstLook project={project(true, 2)} hostName={(id) => (id === 2 ? "Irgetas" : "?")} onWrite={async () => null} onUseWithout={() => {}} onCancel={() => {}} />);
    expect(screen.getByText("Irgetas · /shared/home/me/vgt")).toBeInTheDocument();
    expect(screen.queryByText(/host 2/)).toBeNull();
  });

  it("uses the same wording in the explorer", () => {
    render(<ProjectExplorer project={project(false)} version={1} hostName={() => "x"} selectedDatasetId={null} onOpenRun={() => {}} onImportRun={async () => ({ error: "" })} onSelectDataset={() => {}} onOverview={() => {}} onImport={() => {}} onCancel={() => {}} />);
    expect(screen.getByText("No .vesqira/project.toml yet.")).toBeInTheDocument();
  });
});

describe("sibling run labels in the explorer", () => {
  it("shows the part that tells runs apart and keeps the whole label as the accessible name and title", async () => {
    render(<ProjectExplorer project={project(true)} version={1} hostName={() => "x"} selectedDatasetId={null} onOpenRun={() => {}} onImportRun={async () => ({ error: "" })} onSelectDataset={() => {}} onOverview={() => {}} onImport={() => {}} onCancel={() => {}} />);
    const full = `input=Tb_combined_mw_full_hnv_res1_3pperb_reprojected_on_gass_vm2p0_to_2p0kms`;
    const row = await screen.findByRole("button", { name: new RegExp(`${full}`) });
    expect(row).toHaveTextContent("…vm2p0_to_2p0kms");
    expect(row.getAttribute("title")).toContain(full);
    // three rows, three different drawn labels
    const drawn = (await screen.findAllByText(/^…v/)).map((n) => n.textContent);
    expect(new Set(drawn).size).toBe(3);
  });
});
