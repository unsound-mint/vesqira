import { act, renderHook, waitFor } from "@testing-library/react";
import { useCatalog } from "./useCatalog";

const ds = (id: number, name: string) => ({ id, name, path: `/d/${name}`, sizeBytes: 1, modifiedAt: "2026-01-01T00:00:00Z", kind: "image", hostId: 0, locations: [] });
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("the first dataset list", () => {
  it("keeps a dataset registered while the first list read was still in flight", async () => {
    let answerList: (r: Response) => void = () => {};
    vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
      if (url.startsWith("/api/datasets?")) return new Promise<Response>((res) => { answerList = res; }); // held open
      if (url === "/api/datasets" && init?.method === "POST") return Promise.resolve(json(ds(2, "b.fits")));
      if (url.startsWith("/api/runs") || url.startsWith("/api/jobs")) return Promise.resolve(json({ runs: [], jobs: [] }));
      if (url.startsWith("/api/hosts")) return Promise.resolve(json({ hosts: [] }));
      return Promise.resolve(json({}));
    }));
    const { result } = renderHook(() => useCatalog());
    await act(async () => { await result.current.register("/d/b.fits"); });
    // the list was read before the registration finished: it does not know b.fits
    await act(async () => { answerList(json({ datasets: [ds(1, "a.fits")] })); });
    await waitFor(() => expect(result.current.allDatasets.map((d) => d.id)).toEqual([1, 2]));
  });
});
