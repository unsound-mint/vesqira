import { renderHook, waitFor } from "@testing-library/react";
import { DEFAULT_DISPLAY, newScene, sceneReducer } from "../scene/scene";
import type { SceneOverlay } from "../scene/scene";
import { useOverlayGeometry } from "./useOverlayGeometry";

/** A geometry frame with an empty beam path list, as the API sends it. */
function frame(): ArrayBuffer {
  let json = JSON.stringify({ kind: "beam", dtype: "<f4", coords: "", placement: { method: "m" }, paths: 0, vertices: 0, beam: {} });
  while ((4 + json.length) % 8) json += " ";
  const buf = new ArrayBuffer(4 + json.length);
  new DataView(buf).setUint32(0, json.length, true);
  new Uint8Array(buf, 4).set(new TextEncoder().encode(json));
  return buf;
}

const beam: SceneOverlay = { id: "beam", kind: "beam", datasetId: 2, visible: true };
const auto: SceneOverlay = { id: "vgt", kind: "orientation", datasetId: 3, visible: true, sampling: { mode: "auto", gapPx: 16 }, length: "fixed", opacity: "uniform" };
const scene = [beam, auto].reduce((s, o) => sceneReducer(s, { type: "addOverlay", overlay: o }), newScene(1, DEFAULT_DISPLAY, false, true));

describe("overlay geometry in the viewer", () => {
  it("asks a busy worker again, keeps a refusal with its reason, and waits for the scale before automatic spacing", async () => {
    const replies = [new Response("busy", { status: 504 }), new Response(frame(), { status: 200 })];
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init?: RequestInit) => {
      calls.push(String(init?.body));
      return replies.shift() ?? new Response("{}", { status: 500 });
    }));
    const { result, rerender } = renderHook(({ scale }) => useOverlayGeometry(scene, scale), { initialProps: { scale: 0 } });
    await waitFor(() => expect(result.current.beam?.status).toBe("ready"), { timeout: 4000 });
    expect(calls).toHaveLength(2); // the 504, then the answer
    expect(result.current.vgt?.status).toBe("loading"); // no scale yet: not asked with no spacing
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "the orientation map does not overlap the base map on the sky" }), { status: 422, headers: { "Content-Type": "application/json" } })));
    rerender({ scale: 4 });
    await waitFor(() => expect(result.current.vgt).toMatchObject({ status: "refused", reason: "the orientation map does not overlap the base map on the sky" }));
    vi.unstubAllGlobals();
  });
});
