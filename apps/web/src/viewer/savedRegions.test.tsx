import { act, renderHook, waitFor } from "@testing-library/react";
import { useSavedRegions } from "./useSavedRegions";

/** A tiny in-memory server for the saved-region routes, so the hook is tested through its real requests. */
function fakeApi() {
  let next = 1;
  const rows: Record<string, unknown>[] = [];
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    calls.push(`${init?.method ?? "GET"} ${url}`);
    const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
    const shapeOf = (s: Record<string, number | string>) => ({ shape: "rect", x0: 0, y0: 0, x1: 0, y1: 0, cx: 0, cy: 0, r: 0, ...s });
    if (url.endsWith("/regions") && !init) return json({ regions: rows });
    if (url.endsWith("/regions")) {
      const r = { id: next++, datasetId: 1, name: body.name, shape: shapeOf(body.shape), createdAt: "", updatedAt: "" };
      rows.push(r);
      return json(r, 201);
    }
    const m = /\/api\/regions\/(\d+)(\/delete)?$/.exec(url);
    const i = rows.findIndex((r) => r.id === Number(m?.[1]));
    if (i < 0) return json({ error: "region not found" }, 404);
    if (m![2]) { rows.splice(i, 1); return json({ deleted: true }); }
    if (body.name !== undefined) rows[i]!.name = body.name;
    if (body.shape) rows[i]!.shape = shapeOf(body.shape);
    return json(rows[i]);
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("saved regions", () => {
  it("saves, selects, renames, replaces the shape, duplicates, deletes and undoes", async () => {
    fakeApi();
    const setRegion = vi.fn();
    const { result } = renderHook(() => useSavedRegions(1, setRegion));
    await act(async () => {}); // let the first list request finish
    await act(async () => { await result.current.save("Core", { shape: "rect", x0: 2, y0: 3, x1: 9, y1: 7 }); });
    expect(result.current.saved.map((r) => r.name)).toEqual(["Core"]);
    expect(result.current.selectedId).toBe(1);

    act(() => result.current.select(result.current.saved[0]!));
    expect(setRegion).toHaveBeenLastCalledWith({ shape: "rect", x0: 2, y0: 3, x1: 9, y1: 7 });

    await act(async () => { await result.current.rename(1, "Core (wide)"); });
    expect(result.current.saved[0]!.name).toBe("Core (wide)");
    await act(async () => { await result.current.reshape(1, { shape: "circle", cx: 5, cy: 5, r: 3 }); });
    expect(result.current.saved[0]!.shape).toMatchObject({ shape: "circle", cx: 5, cy: 5, r: 3 });

    await act(async () => { await result.current.duplicate(result.current.saved[0]!); });
    expect(result.current.saved.map((r) => r.name)).toEqual(["Core (wide)", "Core (wide) copy"]);
    expect(setRegion).toHaveBeenLastCalledWith({ shape: "circle", cx: 5, cy: 5, r: 3 });

    await act(async () => { await result.current.remove(result.current.saved[1]!); });
    expect(result.current.saved).toHaveLength(1);
    expect(result.current.undo?.name).toBe("Core (wide) copy");
    await act(async () => { await result.current.undoDelete(); });
    expect(result.current.saved.map((r) => r.name)).toEqual(["Core (wide)", "Core (wide) copy"]);
    expect(result.current.undo).toBeNull();
  });

  it("shows or hides a region on the image without changing it, and reports a failed request", async () => {
    fakeApi();
    const { result } = renderHook(() => useSavedRegions(1, vi.fn()));
    await act(async () => {});
    await act(async () => { await result.current.save("A", { shape: "circle", cx: 1, cy: 1, r: 1 }); });
    act(() => result.current.toggle(1));
    expect(result.current.hidden.has(1)).toBe(true);
    act(() => result.current.toggle(1));
    expect(result.current.hidden.has(1)).toBe(false);
    await act(async () => { await result.current.rename(99, "x"); });
    await waitFor(() => expect(result.current.error).toBe("region not found"));
  });

  it("forgets the regions of the previous dataset", async () => {
    fakeApi();
    const { result, rerender } = renderHook(({ id }) => useSavedRegions(id, vi.fn()), { initialProps: { id: 1 as number | null } });
    await act(async () => {});
    await act(async () => { await result.current.save("A", { shape: "circle", cx: 1, cy: 1, r: 1 }); });
    rerender({ id: null });
    await waitFor(() => expect(result.current.saved).toEqual([]));
    expect(result.current.selectedId).toBeNull();
  });
});
