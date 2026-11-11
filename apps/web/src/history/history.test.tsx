import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { initialPane, newScene, sceneReducer } from "../scene/scene";
import type { Scene } from "../scene/scene";
import type { ExplorationStep } from "../api/history";

const api = vi.hoisted(() => ({ listHistory: vi.fn(), addStep: vi.fn(), getStep: vi.fn() }));
vi.mock("../api/history", () => api);

import { HistoryPanel, branchNote } from "./HistoryPanel";
import { missingDatasets, useExplorationHistory } from "./useExplorationHistory";

const fresh = (id = 1): Scene => newScene(id, initialPane(id, false).keep.display, false, true);
const at = "2026-11-12T10:00:00Z";
const st = (id: number, parentId: number | null, label: string, kind: ExplorationStep["kind"] = "channel", datasetId = 1): ExplorationStep => ({ id, projectId: 4, parentId, datasetId, kind, label, at });

// 1 opened; 2 channel (child of 1); 3 region (child of 2); 4 continued from 2 (a branch); 5 channel (child of 4)
const stored = [st(1, null, "Opened cube", "opened"), st(2, 1, "Channel 10"), st(3, 2, "Drew a circle", "region"), st(4, 2, "Continued from “Channel 10”", "continued"), st(5, 4, "Channel 40")];

function Harness({ scene, restore, have = () => true, projectId = 4 }: { scene: Scene | null; restore: (s: Scene, o: boolean) => void; have?: (id: number) => boolean; projectId?: number }) {
  const h = useExplorationHistory({ projectId, scene, datasetName: (id) => `ds${id}`, haveDataset: have, restore });
  return <HistoryPanel steps={h.steps} keep={h.keep} error={h.error} onContinue={h.continueFrom} />;
}

beforeEach(() => {
  Object.values(api).forEach((f) => f.mockReset());
  api.listHistory.mockResolvedValue({ steps: stored, keep: 500 });
  api.addStep.mockImplementation(async (r: { kind: string; label: string }) => ({ ...st(99, null, r.label), kind: r.kind }));
});

describe("branchNote", () => {
  it("says where a step branched from, only when it did not follow the step before it", () => {
    expect(stored.map((_, i) => branchNote(stored, i))).toEqual([null, null, null, "branched from “Channel 10”", null]);
    expect(branchNote([st(7, 3, "orphan")], 0)).toBe("continues a step that was pruned");
    expect(branchNote([st(1, null, "a"), st(2, null, "b")], 1)).toBe("starts a new line");
  });
});

describe("exploration history in the panel", () => {
  it("shows the stored steps after a reload, newest first, nothing deleted, with the branch noted", async () => {
    render(<Harness scene={null} restore={() => undefined} />);
    const list = await screen.findByRole("list", { name: /exploration steps/i });
    const items = within(list).getAllByRole("listitem");
    expect(items.map((li) => within(li).getByRole("button").getAttribute("aria-label"))).toEqual(["Continue from here: Channel 40", "Continue from here: Continued from “Channel 10”", "Continue from here: Drew a circle", "Continue from here: Channel 10", "Continue from here: Opened cube"]);
    expect(items[1]).toHaveTextContent("branched from “Channel 10”");
    expect(items[0]).toHaveAttribute("aria-current", "step");
    expect(api.listHistory).toHaveBeenCalledWith(4, expect.anything());
  });

  it("continue from here restores the step's scene through the pane and records a continued step that branches from it", async () => {
    const target = sceneReducer(fresh(), { type: "setChannel", channel: 9 });
    api.getStep.mockResolvedValue({ ...st(2, 1, "Channel 10"), scene: target });
    const restore = vi.fn();
    render(<Harness scene={fresh()} restore={restore} />);
    await userEvent.click(await screen.findByRole("button", { name: "Continue from here: Channel 10" }));
    await waitFor(() => expect(restore).toHaveBeenCalledWith(target, false));
    await waitFor(() => expect(api.addStep).toHaveBeenCalledWith(expect.objectContaining({ projectId: 4, parentId: 2, kind: "continued", label: "Continued from “Channel 10”", scene: target })));
  });

  it("refuses to continue from a step whose dataset is gone, and says which", async () => {
    api.getStep.mockResolvedValue({ ...st(2, 1, "Channel 10"), scene: sceneReducer(fresh(3), { type: "setChannel", channel: 9 }) });
    const restore = vi.fn();
    render(<Harness scene={fresh()} restore={restore} have={(id) => id !== 3} />);
    await userEvent.click(await screen.findByRole("button", { name: "Continue from here: Channel 10" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/no longer in the catalog \(#3\)/);
    expect(restore).not.toHaveBeenCalled();
    expect(api.addStep).not.toHaveBeenCalledWith(expect.objectContaining({ kind: "continued" }));
  });

  it("records the scene it is given as steps in the project, branching from the newest stored step", async () => {
    render(<Harness scene={fresh()} restore={() => undefined} />);
    await waitFor(() => expect(api.addStep).toHaveBeenCalled());
    expect(api.addStep).toHaveBeenCalledWith(expect.objectContaining({ projectId: 4, parentId: 5, kind: "opened", label: "Opened ds1" }));
  });

  it("says so when the history cannot be read", async () => {
    api.listHistory.mockRejectedValue(new Error("down"));
    render(<Harness scene={null} restore={() => undefined} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("The history could not be read: down");
  });
});

describe("missingDatasets", () => {
  it("lists the base and overlay datasets the catalog no longer has", () => {
    const s = sceneReducer(fresh(1), { type: "addOverlay", overlay: { id: "c", kind: "beam", datasetId: 8, visible: true } });
    expect(missingDatasets(s, (id) => id === 1)).toEqual([8]);
    expect(missingDatasets(s, () => true)).toEqual([]);
  });
});
