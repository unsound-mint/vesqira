import { getDerived } from "./analysis";

describe("getDerived", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("makes one request for the three inspector sections that read the same record, and reads afresh after it settles", async () => {
    const fetchMock = vi.fn(async (_url: string) => new Response(JSON.stringify({ message: "no derived record" }), { status: 404, headers: { "content-type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const reads = await Promise.allSettled([getDerived(19), getDerived(19), getDerived(19)]);
    expect(reads.every((r) => r.status === "rejected")).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]![0])).toMatch(/\/api\/datasets\/19\/derived$/);
    await Promise.allSettled([getDerived(19)]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
