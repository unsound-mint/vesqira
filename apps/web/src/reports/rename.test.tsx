import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { ReportSummary, ReportView } from "../api/reports";

const api = vi.hoisted(() => ({
  listReports: vi.fn(),
  getReport: vi.fn(),
  createReport: vi.fn(),
  saveReport: vi.fn(),
  publishState: vi.fn(),
}));
vi.mock("../api/reports", async (orig) => ({ ...(await orig<typeof import("../api/reports")>()), ...api }));
vi.mock("../api/boards", () => ({ listFigures: vi.fn().mockResolvedValue([]), figurePreviewUrl: () => "" }));
vi.mock("../api/runs", () => ({ listRuns: vi.fn().mockResolvedValue([]) }));

import { ReportsView } from "./ReportsView";

const summary = (id: number, title: string): ReportSummary =>
  ({ id, key: `k${id}`, title, revision: 1, updatedAt: "2026-11-11T00:00:00Z", lastAuthor: "owner", agentUnseen: false, published: 0, publishedFrom: 0, parent: 0 });
const view = (id: number, title: string): ReportView =>
  ({ ...summary(id, title), body: "", seenRevision: 1, createdAt: "2026-11-11T00:00:00Z", backlinks: [], publications: [], comments: [], drafts: [], unresolvedLinks: null, blockIds: [] }) as unknown as ReportView;

describe("renaming a report that was just created", () => {
  it("waits for that report's page, so the title goes to it and never to the one that was open", async () => {
    const a = summary(1, "Existing"), b = summary(2, "Untitled 1");
    let created = false;
    api.listReports.mockImplementation(async () => (created ? [a, b] : [a]));
    api.publishState.mockResolvedValue(null);
    api.createReport.mockImplementation(async () => { created = true; return view(2, "Untitled 1"); });
    api.saveReport.mockResolvedValue(view(2, "Block size"));
    let release: (v: ReportView) => void = () => {};
    api.getReport.mockImplementation((id: number) => (id === 1 ? Promise.resolve(view(1, "Existing")) : new Promise<ReportView>((r) => { release = r; })));

    const user = userEvent.setup();
    render(<ReportsView />);
    await screen.findByRole("heading", { level: 1, name: "Existing" });
    await user.click(screen.getAllByRole("button", { name: "New report" })[0]!);
    await waitFor(() => expect(api.getReport).toHaveBeenCalledWith(2));
    // the new page has not arrived: the title is not offered for editing, because the page on screen is still the old one
    expect(screen.queryByRole("textbox", { name: "Title" })).not.toBeInTheDocument();
    release(view(2, "Untitled 1"));
    const title = await screen.findByRole("textbox", { name: "Title" });
    await user.clear(title);
    await user.type(title, "Block size{Enter}");
    await waitFor(() => expect(api.saveReport).toHaveBeenCalledTimes(1));
    expect(api.saveReport.mock.calls[0]![0]).toBe(2);
    expect(api.saveReport.mock.calls[0]![2]).toEqual({ body: undefined, title: "Block size" });
  });
});
