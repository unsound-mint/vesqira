import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import { useViewer } from "./useViewer";

const W = 1024;
const H = 1024; // large enough that a remote plane gets a preview first

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

const baseHead = { channels: 3, nan_meaning: "", stats: { finite_count: 1, nonfinite_count: 0, min: 0, max: 1, p01: 0, p99: 1, overflow_count: 0 }, spectral_value: null, spectral_unit: null, flagged: null, source_bitpix: -32, scaled: false };

function fakeApi(o: { previewDelay?: number; previewFails?: boolean; fullFails?: boolean } = {}) {
  const calls: string[] = [];
  const impl = (input: RequestInfo | URL): Promise<Response> => {
    const url = String(input);
    calls.push(url);
    const json = (b: unknown) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } }));
    if (url.endsWith("/view")) return json({ width: W, height: H, channels: 3, bunit: null, bunit_status: "unresolved", fits_axes: { x: 1, y: 2, channel: 3 }, sky: { kind: "pixel", note: "" }, spectral: null, flags: { declared: false, channels: [] } });
    const m = /\/plane\?channel=(\d+)(?:&step=(\d+))?/.exec(url);
    if (m) {
      const channel = Number(m[1]);
      const step = m[2] ? Number(m[2]) : 1;
      if (step > 1 && o.previewFails) return new Promise((_, rej) => setTimeout(() => rej(new Error("down")), o.previewDelay ?? 0));
      if (step === 1 && o.fullFails) return Promise.resolve(new Response(JSON.stringify({ error: "cannot read" }), { status: 422, headers: { "Content-Type": "application/json" } }));
      const w = Math.ceil(W / step);
      const h = Math.ceil(H / step);
      const head = { ...baseHead, channel, width: w, height: h, preview: step > 1 ? { step, source_width: W, source_height: H, method: "m" } : null };
      const res = new Response(frame(head, new Float32Array(w * h)), { status: 200 });
      return step > 1 && o.previewDelay ? new Promise((ok) => setTimeout(() => ok(res), o.previewDelay)) : Promise.resolve(res);
    }
    return json([]);
  };
  return { calls, impl };
}

const dataset = (hostId: number) => ({ id: 7, hostId, interpretation: null } as unknown as Dataset);

afterEach(() => vi.unstubAllGlobals());

describe("plane loading", () => {
  it("on a remote host shows a labelled preview first, then the full plane, then warms the neighbouring channels", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(dataset(2)));
    await waitFor(() => expect(result.current.plane?.head.preview?.step).toBeGreaterThan(1));
    expect(result.current.plane?.head.width).toBe(W); // drawn on the full grid, so pixel indices keep their meaning
    await waitFor(() => expect(result.current.plane?.head.preview).toBeNull(), { timeout: 2000 });
    expect(result.current.plane?.head.width).toBe(W);
    const planes = api.calls.filter((u) => u.includes("/plane"));
    expect(planes[0]).toMatch(/channel=0&step=\d+/);
    expect(planes).toContain("/api/datasets/7/plane?channel=0");
    await waitFor(() => expect(api.calls).toContain("/api/datasets/7/plane?channel=1")); // prefetch of the next channel
  });

  it("on a local dataset asks for the full plane only", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(dataset(0)));
    await waitFor(() => expect(result.current.plane).not.toBeNull());
    expect(result.current.plane?.head.preview).toBeNull();
    expect(api.calls.filter((u) => u.includes("/plane") && u.includes("step"))).toEqual([]);
  });

  it("a preview that fails after the full plane arrived leaves the full plane alone", async () => {
    const api = fakeApi({ previewFails: true, previewDelay: 400 });
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(dataset(2)));
    await waitFor(() => expect(result.current.plane?.head.preview).toBeNull(), { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 600)); // the failing preview answers now
    expect(result.current.plane?.head.preview).toBeNull();
    expect(result.current.planeError).toBeNull();
  });

  it("a preview that succeeds after the full plane failed does not hide the error", async () => {
    const api = fakeApi({ fullFails: true, previewDelay: 400 });
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(dataset(2)));
    await waitFor(() => expect(result.current.planeError).not.toBeNull(), { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 600));
    expect(result.current.plane).toBeNull();
    expect(result.current.planeError).not.toBeNull();
  });

  it("a preview gives no pixel value", async () => {
    const api = fakeApi({ previewDelay: 600 });
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result } = renderHook(() => useViewer(dataset(2)));
    act(() => result.current.setPinned({ x: 3, y: 3 }));
    await waitFor(() => expect(result.current.plane).not.toBeNull(), { timeout: 3000 });
    if (result.current.plane?.head.preview) expect(result.current.pixelValue).toBeNull();
    await waitFor(() => expect(result.current.plane?.head.preview).toBeNull(), { timeout: 3000 });
    expect(result.current.pixelValue).toBe(0);
  });
});
