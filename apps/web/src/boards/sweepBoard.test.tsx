import { render, screen } from "@testing-library/react";
import { BoardsView } from "./BoardsView";
import type { Board, JobInfo } from "../api/boards";

vi.mock("../viewer/useViewer", () => ({ useViewer: () => ({ plane: null, interp: null, reliability: null }) }));
vi.mock("../viewer/PlaneViewer", () => ({ PlaneViewer: () => <div /> }));

const job = (id: number, label: string): JobInfo => ({ id, name: `s-${id}`, state: "draft", submittedAt: null, finishedAt: null, runId: null, label, sweepId: 9 });
const board: Board = {
  id: 1, title: "Sweep: s", createdAt: "", updatedAt: "",
  items: [{ position: 0, kind: "job", job: job(3, "N = 8, λ = 3"), note: "" }, { position: 1, kind: "job", job: job(4, "N = 8, λ = 10"), note: "" }],
};
const grid = { sweepId: 9, name: "s", plotNote: "", plot: null, params: [{ key: "N", label: "", values: ["8"] }], metrics: [], rows: [] };

afterEach(() => vi.unstubAllGlobals());

describe("a board made from a sweep", () => {
  it("labels each job panel with its parameters in order and shows the sweep's grid", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const body = url.startsWith("/api/sweeps/9/grid") ? grid : url.endsWith("/snapshots") ? { snapshots: [] } : url.startsWith("/api/boards/1") ? board : url === "/api/boards" ? { boards: [board] } : {};
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(<BoardsView />);
    const heads = await screen.findAllByRole("heading", { level: 3 });
    expect(heads.map((h) => h.textContent).slice(0, 2)).toEqual(["N = 8, λ = 3 · Run s-3", "N = 8, λ = 10 · Run s-4"]);
    expect(await screen.findByRole("region", { name: "Sweep grid" })).toBeInTheDocument();
  });
});
