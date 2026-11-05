import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BoardsView } from "./BoardsView";
import { ReviewPage } from "../review/ReviewPage";
import type { Board, JobInfo } from "../api/boards";

// The viewer is its own, heavily tested module: here it only has to report a drawn plane.
const plane = { head: { preview: false } };
vi.mock("../viewer/useViewer", () => ({ useViewer: () => ({ plane, interp: null, reliability: null }) }));
vi.mock("../viewer/PlaneViewer", async () => {
  const { useEffect } = await import("react");
  return { PlaneViewer: ({ onDrawn }: { onDrawn?: (p: unknown) => void }) => { useEffect(() => { onDrawn?.(plane); }, [onDrawn]); return <div data-testid="viewer" />; } };
});
const saveViewFigure = vi.fn();
vi.mock("../viewer/saveFigure", () => ({ saveViewFigure: (...a: unknown[]) => saveViewFigure(...a) }));

const prov = { source: { name: "cube.fits", sizeBytes: 1, modifiedAt: "", identity: "a reference by name" }, view: { kind: "plane" }, statements: [] };
const figure = { id: 5, datasetId: 9, title: "vgt-ker16: psi.fits", caption: "", createdAt: "", revision: "rev", previewSha: "a".repeat(64), width: 4, height: 3, provenance: prov };
const job = (over: Partial<JobInfo>): JobInfo => ({ id: 3, name: "vgt-ker16", state: "running", submittedAt: new Date(Date.now() - 600_000).toISOString(), finishedAt: null, runId: null, ...over });
const boardWith = (j: JobInfo): Board => ({ id: 1, title: "Live", createdAt: "", updatedAt: "", items: [{ position: 0, kind: "job", job: j, note: "wait for this" }] });

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    if (url.endsWith("/snapshots")) return new Response(JSON.stringify({ snapshots: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
    const key = Object.keys(routes).sort((a, b) => b.length - a.length).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}

beforeEach(() => { localStorage.clear(); saveViewFigure.mockReset(); });
afterEach(() => vi.unstubAllGlobals());

describe("a board panel that follows a running job", () => {
  it("shows the state, the progress and an estimate that says it is one, and cannot be compared", async () => {
    const running = job({ progress: { done: 4, total: 16, etaSeconds: 2460 } });
    mockFetch({ "/api/boards/1": () => ({ body: boardWith(running) }), "/api/boards": () => ({ body: { boards: [boardWith(running)] } }) });
    render(<BoardsView />);
    expect(await screen.findByRole("heading", { name: "Run vgt-ker16" })).toBeInTheDocument();
    expect(screen.getAllByText("running · 25 %").length).toBeGreaterThan(0);
    expect(screen.getByText(/4 of 16 chunks done/)).toBeInTheDocument();
    expect(screen.getByText(/about 41 min left \(estimate at the rate so far\)/)).toBeInTheDocument();
    expect(screen.getByText(/Becomes the result when the job finishes/)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /Compare/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Move up" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove from board" })).toBeInTheDocument();
  });

  it("never invents a percentage or a time when the log reports no progress", async () => {
    const running = job({});
    mockFetch({ "/api/boards/1": () => ({ body: boardWith(running) }), "/api/boards": () => ({ body: { boards: [boardWith(running)] } }) });
    render(<BoardsView />);
    await screen.findByRole("heading", { name: "Run vgt-ker16" });
    expect(document.body.textContent).not.toMatch(/%|left \(estimate/);
    expect(screen.getByText(/reports no progress/)).toBeInTheDocument();
    expect(screen.getByText(/Elapsed 10 min/)).toBeInTheDocument();
  });

  it("explains a finished job that has no imported run, and a failed one", async () => {
    const done = job({ state: "completed" });
    mockFetch({ "/api/boards": () => ({ body: { boards: [boardWith(done)] } }) });
    const { unmount } = render(<BoardsView />);
    expect(await screen.findByText(/Finished; import its results on the Jobs screen to pin the result/)).toBeInTheDocument();
    unmount();
    mockFetch({ "/api/boards": () => ({ body: { boards: [boardWith(job({ state: "failed" }))] } }) });
    render(<BoardsView />);
    expect(await screen.findByText(/This run failed/)).toBeInTheDocument();
  });

  it("saves the finished run's result as a figure and pins it, and offers a retry when saving fails", async () => {
    const done = job({ state: "completed", runId: 7 });
    const run = { id: 7, name: "vgt-ker16", outputs: [{ role: "reliability", datasetId: 21, name: "rel.fits" }, { role: "psi", datasetId: 22, name: "psi.fits" }] };
    const calls = mockFetch({
      "/api/boards/1/items/0/pin": () => ({ body: { id: 1, title: "Live", createdAt: "", updatedAt: "", items: [{ position: 0, kind: "figure", figure, note: "wait for this" }] } }),
      "/api/boards": () => ({ body: { boards: [boardWith(done)] } }),
      "/api/runs": () => ({ body: { runs: [run] } }),
      "/api/datasets": () => ({ body: { datasets: [{ id: 21, name: "rel.fits" }, { id: 22, name: "psi.fits" }] } }),
    });
    saveViewFigure.mockRejectedValueOnce(new Error("The image is not drawn yet.")).mockResolvedValueOnce(figure);
    render(<BoardsView />);
    expect(await screen.findByRole("alert")).toHaveTextContent("The image is not drawn yet.");
    expect(calls.some((c) => c.url.endsWith("/pin"))).toBe(false); // nothing is pinned silently
    await userEvent.click(screen.getByRole("button", { name: "Pin the result" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "vgt-ker16: psi.fits" })).toBeInTheDocument());
    expect(saveViewFigure).toHaveBeenCalledTimes(2);
    expect(saveViewFigure.mock.calls[0]![1]).toMatchObject({ id: 22 }); // the orientation field, not the reliability map
    const pin = calls.filter((c) => c.url.endsWith("/pin"));
    expect(pin).toHaveLength(1);
    expect(JSON.parse(String(pin[0]!.init!.body))).toEqual({ figureId: 5 });
    expect(screen.getByRole("checkbox", { name: /Compare/ })).toBeInTheDocument(); // an ordinary figure now
  });

  it("pins by itself once the result is drawn", async () => {
    const done = job({ state: "completed", runId: 7 });
    const calls = mockFetch({
      "/api/boards/1/items/0/pin": () => ({ body: { id: 1, title: "Live", createdAt: "", updatedAt: "", items: [{ position: 0, kind: "figure", figure, note: "" }] } }),
      "/api/boards": () => ({ body: { boards: [boardWith(done)] } }),
      "/api/runs": () => ({ body: { runs: [{ id: 7, name: "r", outputs: [{ role: "support", datasetId: 30, name: "x.fits" }] }] } }),
      "/api/datasets": () => ({ body: { datasets: [{ id: 30, name: "x.fits" }] } }),
    });
    saveViewFigure.mockResolvedValue(figure);
    render(<BoardsView />);
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/pin"))).toBe(true));
    expect(saveViewFigure).toHaveBeenCalledTimes(1); // the first output when there is no psi
  });
});

describe("the reviewer's view of a panel that was still running", () => {
  it("is a placeholder with the state and the time, and nothing to comment on", async () => {
    const review = { snapshot: { boardTitle: "Group meeting", label: "v1", createdAt: "2026-11-04T00:00:00Z", expiresAt: "2026-12-01T00:00:00Z", items: [{ kind: "job", title: "vgt-ker16", state: "running", at: "2026-11-04T10:00:00Z", note: "ker 16 vs 8" }] }, comments: [], outcome: { value: "" } };
    mockFetch({ "/review-api/tok": () => ({ body: review }) });
    render(<ReviewPage token="tok" />);
    expect(await screen.findByText(/Run vgt-ker16: running when this snapshot was published/)).toBeInTheDocument();
    expect(screen.getByText(/ker 16 vs 8/)).toBeInTheDocument();
    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.queryByRole("button", { name: "Send comment" })).toBeNull();
  });
});
