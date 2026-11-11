import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { ReportSummary, ReportView } from "../api/reports";

const api = vi.hoisted(() => ({ listReports: vi.fn(), getReport: vi.fn(), saveReport: vi.fn(), publishState: vi.fn() }));
vi.mock("../api/reports", async (orig) => ({ ...(await orig<typeof import("../api/reports")>()), ...api }));
vi.mock("../api/boards", () => ({ listFigures: vi.fn().mockResolvedValue([]), figurePreviewUrl: () => "" }));
vi.mock("../api/runs", () => ({ listRuns: vi.fn().mockResolvedValue([]) }));

import { ReportsView } from "./ReportsView";

const row = (revision: number): ReportSummary =>
  ({ id: 1, key: "k1", title: "R", revision, updatedAt: "2026-11-11T00:00:00Z", lastAuthor: "owner", agentUnseen: false, published: 0, publishedFrom: 0, parent: 0 });
const view = (revision: number, body: string): ReportView =>
  ({ ...row(revision), body, seenRevision: 1, createdAt: "2026-11-11T00:00:00Z", backlinks: [], publications: [], comments: [], drafts: [], unresolvedLinks: null, blockIds: [] }) as unknown as ReportView;

describe("saving while an earlier save is still in flight", () => {
  it("keeps both edits: the later one waits and is built on what the earlier one saved", async () => {
    let stored = view(1, "First part.");
    api.listReports.mockImplementation(async () => [row(stored.revision)]);
    api.publishState.mockResolvedValue(null);
    api.getReport.mockImplementation(async () => stored);
    let finishFirst: () => void = () => {};
    api.saveReport.mockImplementation(async (_id: number, rev: number, p: { body?: string }) => {
      if (rev === 1) await new Promise<void>((r) => { finishFirst = r; }); // the first save is slow, as under load
      stored = view(rev + 1, p.body ?? stored.body);
      return stored;
    });

    const user = userEvent.setup();
    render(<ReportsView />);
    await user.click(await screen.findByText("First part."));
    const box = await screen.findByRole("textbox", { name: /Edit this part/ });
    await user.clear(box);
    await user.type(box, "Changed part.");
    await user.keyboard("{Control>}{Enter}{/Control}");
    await waitFor(() => expect(api.saveReport).toHaveBeenCalledTimes(1));
    // the summary is typed while the first save has not returned
    await user.click(screen.getByRole("button", { name: "Add a one-line summary" }));
    await user.type(screen.getByRole("textbox", { name: /Summary/ }), "Nothing moves.{Enter}");
    expect(api.saveReport).toHaveBeenCalledTimes(1);
    finishFirst();
    await waitFor(() => expect(api.saveReport).toHaveBeenCalledTimes(2));
    const [, rev, p] = api.saveReport.mock.calls[1]!;
    expect(rev).toBe(2);
    expect(p.body).toContain("Changed part.");
    expect(p.body).toContain("Nothing moves.");
  });
});
