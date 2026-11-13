import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Dataset, DatasetSummary } from "../api/catalog";
import { useDatasetDetail } from "./useDatasetDetail";
import { withFacts } from "./testDatasets";

const header = { naxis: 2, bitpix: -32, bunit: "K", bunit_status: "declared", axes: [{ length: 4 }, { length: 3 }] };
const full = (id: number) => withFacts({ id, name: `d${id}.fits`, kind: "fits", header, locations: [] }) as unknown as Dataset;
const row = (id: number) => { const { header: _h, locations: _l, ...rest } = full(id) as unknown as Record<string, unknown>; return rest as unknown as DatasetSummary; };
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

describe("the full dataset behind a list row", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("reads the dataset's own route, not the list, and shows nothing until the row is in the list", async () => {
    const fetchMock = vi.fn(async (url: string) => json(full(Number(url.split("/").pop()))));
    vi.stubGlobal("fetch", fetchMock);
    const { result, rerender } = renderHook(({ rows }) => useDatasetDetail(7, rows), { initialProps: { rows: [] as DatasetSummary[] } });
    expect(result.current.detail).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    rerender({ rows: [row(7)] });
    await waitFor(() => expect(result.current.detail?.id).toBe(7));
    expect(fetchMock.mock.calls[0]![0]).toBe("/api/datasets/7");
    expect((result.current.detail as unknown as { header: unknown }).header).toEqual(header);
  });

  it("reads again when the list replaces the row, and never shows another id's dataset", async () => {
    const fetchMock = vi.fn(async (url: string) => json(full(Number(url.split("/").pop()))));
    vi.stubGlobal("fetch", fetchMock);
    const { result, rerender } = renderHook(({ id, rows }) => useDatasetDetail(id, rows), { initialProps: { id: 7, rows: [row(7), row(8)] } });
    await waitFor(() => expect(result.current.detail?.id).toBe(7));
    rerender({ id: 7, rows: [row(7), row(8)] });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    rerender({ id: 8, rows: [row(7), row(8)] });
    expect(result.current.detail).toBeNull();
    await waitFor(() => expect(result.current.detail?.id).toBe(8));
  });

  it("says why a read failed instead of showing a stale dataset", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "gone" }, 404)));
    const { result } = renderHook(() => useDatasetDetail(7, [row(7)]));
    await waitFor(() => expect(result.current.error).not.toBeNull());
    expect(result.current.detail).toBeNull();
  });
});
