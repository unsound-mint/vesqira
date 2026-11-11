import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ProjectSummary, TreeEntry } from "../api/projects";
import { ProjectExplorer } from "./ProjectExplorer";

const entry = (name: string, kind: TreeEntry["kind"], readOnly = false): TreeEntry => ({
  name,
  path: `x/${name}`,
  kind,
  count: null,
  run: null,
  file: { size: 10, mtimeNs: 1, format: "other", viewer: false, role: "", readOnly, datasetId: null },
});

vi.mock("../api/projects", async (orig) => ({
  ...(await orig<typeof import("../api/projects")>()),
  projectTree: async (_id: number, group: string) => ({
    group,
    path: "",
    total: 3,
    offset: 0,
    limit: 200,
    nextOffset: null,
    entries: group === "configs" ? [entry("pipeline.toml", "config"), entry("cube.fits", "config"), entry("locked.toml", "config", true)] : [],
  }),
}));

const project = (hostId: number): ProjectSummary =>
  ({
    id: 1,
    hostId,
    path: hostId ? "/scratch/me/proj/" : "/home/me/proj",
    name: "proj",
    scanned: true,
    scan: { state: "done", entries: 1, toRegister: 0, registered: 0, failed: 0, startedAt: null, finishedAt: null, error: "" },
    counts: { runs: 0, data: 0, configs: 3, jobs: 0 },
    changes: null,
    projectFile: { exists: true, error: "", name: "proj", hostRepos: {} },
  }) as unknown as ProjectSummary;

async function renderExplorer(hostId: number, onEditFile: (p: string, h: number) => void) {
  render(
    <ProjectExplorer project={project(hostId)} version={1} hostName={() => "Cluster login"} selectedDatasetId={null} onOpenRun={() => {}} onSelectDataset={() => {}} onEditFile={onEditFile} onOverview={() => {}} onImport={() => {}} onCancel={() => {}} />,
  );
  await userEvent.click(screen.getByText(/^Configs/)); // the group is collapsed until it is opened
}

describe("opening a project file in the editor from the explorer", () => {
  it("passes the host of a project on a host with the file's absolute path there", async () => {
    const onEdit = vi.fn();
    await renderExplorer(3, onEdit);
    await userEvent.click(await screen.findByText("pipeline.toml"));
    expect(onEdit).toHaveBeenCalledWith("/scratch/me/proj/x/pipeline.toml", 3);
  });

  it("passes host 0 for a project on this computer", async () => {
    const onEdit = vi.fn();
    await renderExplorer(0, onEdit);
    await userEvent.click(await screen.findByText("pipeline.toml"));
    expect(onEdit).toHaveBeenCalledWith("/home/me/proj/x/pipeline.toml", 0);
  });

  it("offers neither FITS nor a read-only file, on a host or not", async () => {
    const onEdit = vi.fn();
    await renderExplorer(3, onEdit);
    await userEvent.click(await screen.findByText("cube.fits"));
    await userEvent.click(screen.getByText("locked.toml"));
    expect(onEdit).not.toHaveBeenCalled();
  });
});
