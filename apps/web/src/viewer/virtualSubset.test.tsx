import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import { resetOfflineState } from "../api/offlineState";
import { virtualId } from "../api/planes";
import { useViewer } from "./useViewer";

const frame = (head: object, pixels: Float32Array) => {
  let json = JSON.stringify(head);
  json += " ".repeat((4 - ((4 + json.length) % 4)) % 4);
  const buf = new ArrayBuffer(4 + json.length + pixels.length * 4);
  const dv = new DataView(buf);
  dv.setUint32(0, json.length, true);
  new Uint8Array(buf, 4, json.length).set(new TextEncoder().encode(json));
  pixels.forEach((p, i) => dv.setFloat32(4 + json.length + i * 4, p, true));
  return buf;
};
const stats = { finite_count: 1, nonfinite_count: 0, min: 0, max: 1, p01: 0, p99: 1, overflow_count: 0 };

function fakeApi() {
  const calls: string[] = [];
  const impl = (input: RequestInfo | URL): Promise<Response> => {
    const url = String(input);
    calls.push(url);
    const json = (b: unknown) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } }));
    if (url.endsWith("/view")) return json({ width: 4, height: 3, channels: 5, bunit: "K", bunit_status: "declared", fits_axes: { x: 1, y: 2, channel: 3 }, sky: { kind: "pixel", note: "" }, spectral: null, flags: { declared: false, channels: [] }, window: { x0: 3, y0: 2, step: 2, c0: 10 } });
    const m = /\/plane\?channel=(\d+)/.exec(url);
    if (m) return Promise.resolve(new Response(frame({ channel: Number(m[1]), channels: 5, width: 4, height: 3, nan_meaning: "", stats, spectral_value: null, spectral_unit: null, flagged: null, preview: null }, new Float32Array(12)), { status: 200 }));
    if (url.includes("/coord")) return json({ x: 1, y: 2, channel: 0, sky: { lon: 150, lat: 2 }, spectral: null });
    if (url.includes("/spectrum")) return Promise.resolve(new Response(new ArrayBuffer(0), { status: 422, headers: { "Content-Type": "application/json" } }));
    return json([]);
  };
  return { calls, impl };
}

// A virtual subset (ADR 0063) is opened in the viewer under the negative of its id. It is read through /api/subsets/{sid} by the
// view, plane and coordinate routes and by nothing else: no request may go to a dataset route that would answer for the source's pixels
// or to a route that has no window (masks, saved regions, conversions, orientation statistics).
const subset = (over: object = {}) => ({ id: virtualId(3), hostId: 0, interpretation: null, modifiedAt: "t#v1", ...over }) as unknown as Dataset;

afterEach(() => { vi.unstubAllGlobals(); resetOfflineState(); });

describe("a virtual subset in the viewer", () => {
  it("reads the layout, the plane and the coordinates through the subset's routes and asks nothing else", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(subset()));
    expect(result.current.virtual).toBe(true);
    await waitFor(() => expect(result.current.plane).not.toBeNull());
    expect(result.current.view?.window).toMatchObject({ x0: 3, step: 2, c0: 10 });
    act(() => result.current.setPinned({ x: 1, y: 2 }));
    await waitFor(() => expect(result.current.coord?.sky?.lon).toBe(150));
    expect(api.calls).toContain("/api/subsets/3/view");
    expect(api.calls).toContain("/api/subsets/3/plane?channel=0");
    expect(api.calls).toContain("/api/subsets/3/coord?x=1&y=2&channel=0");
    expect(api.calls.filter((u) => !u.startsWith("/api/subsets/3/"))).toEqual([]);
    expect(result.current.offers).toBeNull();
    expect(result.current.savedRegions.saved).toEqual([]);
  });

  it("takes the spectrum of a region through the subset's route, without masks", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(subset()));
    await waitFor(() => expect(result.current.view).not.toBeNull());
    act(() => result.current.setRegion({ shape: "rect", x0: 0, y0: 0, x1: 1, y1: 1 }));
    await waitFor(() => expect(api.calls.some((u) => u.startsWith("/api/subsets/3/spectrum?shape=rect"))).toBe(true));
    expect(api.calls.some((u) => u.includes("mask="))).toBe(false);
    expect(api.calls.filter((u) => !u.startsWith("/api/subsets/3/"))).toEqual([]);
  });

  it("a dataset still reads through its own routes", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    renderHook(() => useViewer({ id: 7, hostId: 0, interpretation: null } as unknown as Dataset));
    await waitFor(() => expect(api.calls).toContain("/api/datasets/7/plane?channel=0"));
    expect(api.calls.some((u) => u.startsWith("/api/subsets/"))).toBe(false);
  });
});
