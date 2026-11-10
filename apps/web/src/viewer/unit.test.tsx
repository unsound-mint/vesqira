import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import { captionLines, type FigureFacts } from "./figure";
import { unitLabel } from "./format";
import { useViewer } from "./useViewer";

afterEach(() => vi.unstubAllGlobals());

const facts = (over: Partial<FigureFacts> = {}): FigureFacts => ({ dataset: "a.fits", kind: "plane", channelLine: "", displayLine: "", limits: [0, 1], limitsUnit: "Jy/beam", ...over });
const opts = { colormap: "viridis", stretch: "linear", range: "percentile", axial: false, region: null };

describe("a figure says whose the unit is", () => {
  it("adds a line that the unit is the author's when the file has none, and none when the file states it", () => {
    const declared = captionLines(facts({ unitDeclared: true }), null, null, opts);
    expect(declared.join("\n")).toContain("Brightness unit (Jy/beam) declared by the author; the file has no BUNIT.");
    const stated = captionLines(facts({ limitsUnit: "K" }), null, null, opts);
    expect(stated.join("\n")).not.toContain("declared by the author; the file has no BUNIT");
  });
  it("keeps the statement that header values are the file's, and never adds a unit line to an orientation map", () => {
    expect(captionLines(facts({ unitDeclared: true }), null, null, opts).join("\n")).toContain("Header values are declared by the file, not verified.");
    const axial = captionLines(facts({ kind: "orientation", unitDeclared: true }), null, null, { ...opts, axial: true });
    expect(axial.join("\n")).not.toContain("Brightness unit");
  });
  it("labels the unit on screen as the file's, yours, or unresolved", () => {
    expect(unitLabel("K", "declared")).toBe("K");
    expect(unitLabel("Jy/beam", "user")).toBe("Jy/beam (declared by you)");
    expect(unitLabel(null, "unresolved")).toBe("brightness unit unresolved");
  });
});

// Declaring or withdrawing a unit changes every head. The viewer reads its layout again without starting over.
function fakeApi() {
  const views: string[] = [];
  let unit: string | null = null;
  const impl = (input: RequestInfo | URL): Promise<Response> => {
    const url = String(input);
    const json = (b: unknown) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { "Content-Type": "application/json" } }));
    if (url.endsWith("/view")) {
      views.push(url);
      return json({ width: 8, height: 8, channels: 3, bunit: unit, bunit_status: unit ? "user" : "unresolved", fits_axes: { x: 1, y: 2, channel: 3 }, sky: { kind: "pixel", note: "" }, spectral: null, flags: { declared: false, channels: [] } });
    }
    return json([]);
  };
  return { views, impl, setUnit: (u: string | null) => { unit = u; } };
}

const withUnit = (u: { unit: string; stale?: boolean } | null) => ({ id: 7, hostId: 0, interpretation: null, modifiedAt: "t", unitDeclaration: u ? { unit: u.unit, declaredBy: "user", declaredAt: "2026-11-10T10:00:00Z", stale: u.stale ?? false } : null }) as unknown as Dataset;

describe("the viewer follows a declared unit", () => {
  it("reads the layout again when the unit is declared, replaced and withdrawn, and shows each state", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result, rerender } = renderHook(({ d }) => useViewer(d), { initialProps: { d: withUnit(null) } });
    await waitFor(() => expect(result.current.view).not.toBeNull());
    expect(result.current.view?.bunit).toBeNull();
    expect(api.views).toHaveLength(1);

    api.setUnit("Jy/beam");
    rerender({ d: withUnit({ unit: "Jy/beam" }) });
    await waitFor(() => expect(result.current.view?.bunit).toBe("Jy/beam"));
    expect(result.current.view?.bunit_status).toBe("user");

    api.setUnit("mJy/beam");
    rerender({ d: withUnit({ unit: "mJy/beam" }) });
    await waitFor(() => expect(result.current.view?.bunit).toBe("mJy/beam"));

    api.setUnit(null);
    rerender({ d: withUnit(null) });
    await waitFor(() => expect(result.current.view?.bunit).toBeNull());
    expect(result.current.view?.bunit_status).toBe("unresolved");
    expect(api.views).toHaveLength(4);
  });

  it("does not start over (the channel stays) and does not refetch for an unrelated change", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result, rerender } = renderHook(({ d }) => useViewer(d), { initialProps: { d: withUnit(null) } });
    await waitFor(() => expect(result.current.view).not.toBeNull());
    result.current.setChannel(2);
    await waitFor(() => expect(result.current.channel).toBe(2));
    api.setUnit("Jy/beam");
    rerender({ d: withUnit({ unit: "Jy/beam" }) });
    await waitFor(() => expect(result.current.view?.bunit).toBe("Jy/beam"));
    expect(result.current.channel).toBe(2);
    const n = api.views.length;
    rerender({ d: { ...withUnit({ unit: "Jy/beam" }), name: "renamed" } as Dataset });
    expect(api.views).toHaveLength(n);
  });

  it("a stale declaration is not applied, so it does not trigger a refetch to a unit the server will not send", async () => {
    const api = fakeApi();
    vi.stubGlobal("fetch", vi.fn(api.impl));
    const { result, rerender } = renderHook(({ d }) => useViewer(d), { initialProps: { d: withUnit(null) } });
    await waitFor(() => expect(result.current.view).not.toBeNull());
    rerender({ d: withUnit({ unit: "Jy/beam", stale: true }) });
    expect(api.views).toHaveLength(1);
  });
});
