import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { ChannelGridPlan } from "../api/channelmaps";
import { ChannelGrid } from "./ChannelGrid";
import type { Viewer } from "./useViewer";

vi.mock("./channelGrid", async (orig) => ({ ...(await orig<typeof import("./channelGrid")>()), composeGridFigure: vi.fn(async () => new Blob(["png"], { type: "image/png" })) }));

afterEach(() => vi.unstubAllGlobals());

const ds = { id: 5, name: "hi.fits" } as Dataset;

const plan = (over: Partial<ChannelGridPlan> = {}): ChannelGridPlan => ({
  channels: 30, width: 4, height: 3, panel_width: 4, panel_height: 3, step: 1, panel_count: 4, pixels_per_panel: 12, pixels_total: 48,
  caps: { panels: 64, pixels_per_panel: 1048576, pixels_total: 16777216 },
  panels: [0, 1, 2, 3].map((i) => ({ index: i, first: i * 3, last: i * 3, channels: 1, mean: false, spectral: { first: -3000 + i * 1000, last: -3000 + i * 1000 }, flagged: null })),
  preview: null, averaging: null,
  scale: { lo: 0.5, hi: 8.25, source: "1st to 99th percentile of the finite pixels pooled over panels 1, 4 of 4 (evenly spaced, at the preview resolution); one scale for every panel", sampled_panels: [0, 3] },
  spectral: { axis: "radio velocity", ctype: "VRAD", unit: "m/s", unit_status: "declared", native_unit: "m/s", convention: null, convention_source: null, specsys: "LSRK", rest: null, basis: null, refused: null, reference_frame_note: "x" },
  nan_meaning: "no value", ...over,
}) as ChannelGridPlan;

function planeFrame(channel: number): ArrayBuffer {
  const head = JSON.stringify({ width: 4, height: 3, channel, channels: 30, nan_meaning: "x", stats: { finite_count: 12, nonfinite_count: 0, min: 0, max: 9, p01: 0, p99: 9, overflow_count: 0 }, spectral_value: null, spectral_unit: null, flagged: null, source_bitpix: -32, scaled: false });
  const json = head + " ".repeat((8 - ((4 + head.length) % 8)) % 8);
  const buf = new ArrayBuffer(4 + json.length + 48);
  new DataView(buf).setUint32(0, json.length, true);
  new Uint8Array(buf, 4, json.length).set(new TextEncoder().encode(json));
  return buf;
}

function api(p: ChannelGridPlan = plan()) {
  const urls: string[] = [];
  const posts: { url: string; body: Record<string, unknown> }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    urls.push(url);
    if (init?.method === "POST") {
      posts.push({ url, body: JSON.parse(String(init.body)) });
      return new Response(JSON.stringify({ id: 9, title: "saved", datasetId: 5 }), { status: 201, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/channel-grid")) return new Response(JSON.stringify(p), { status: 200, headers: { "Content-Type": "application/json" } });
    const m = /channel=(\d+)/.exec(url);
    return new Response(planeFrame(Number(m?.[1] ?? 0)), { status: 200, headers: { "Content-Type": "application/vnd.vesqira.plane" } });
  }));
  return { urls, posts };
}

const viewer = (over: Partial<Record<string, unknown>> = {}) => ({
  view: { channels: 30, bunit: "K", spectral: { unit: "m/s" } }, interp: null, conv: { spectral: "", convention: "radio" },
  offers: { spectral: { native_unit: "m/s", targets: [{ unit: "km/s", enabled: true, reason: null, needs_convention: false }, { unit: "GHz", enabled: false, reason: "needs a rest frequency", needs_convention: false }] } },
  scene: { overlays: [{ id: "regions", visible: true }, { id: "co", visible: true }] },
  ...over,
}) as unknown as Viewer;

describe("ChannelGrid", () => {
  it("plans the panels, labels each with its channel and spectral coordinate, and states the one shared scale", async () => {
    const { urls } = api();
    render(<ChannelGrid v={viewer()} dataset={ds} />);
    expect(await screen.findByText("Channel 1 · -3000 m/s")).toBeInTheDocument();
    expect(screen.getByText("Channel 4 · -2000 m/s")).toBeInTheDocument();
    expect(screen.getByText("Channel 10 · 0 m/s")).toBeInTheDocument();
    const plan = urls.find((u) => u.includes("/channel-grid"))!;
    expect(plan).toBe("/api/datasets/5/channel-grid?first=0&last=11&stride=1&average=1");
    expect(screen.getByText(/All 4 panels share one colour scale and the linear stretch: 0.5 to 8.25 K/)).toBeInTheDocument();
    expect(screen.getByText(/The map's 1 overlay is not drawn on the panels/)).toBeInTheDocument();
    expect(screen.getByLabelText("Colour scale shared by every panel")).toBeInTheDocument();
    // each panel read through the plane path, one request per channel
    await waitFor(() => expect(urls.filter((u) => u.includes("/plane?channel=")).sort()).toEqual(["/api/datasets/5/plane?channel=0", "/api/datasets/5/plane?channel=3", "/api/datasets/5/plane?channel=6", "/api/datasets/5/plane?channel=9"]));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("4 of 4 panels loaded"));
  });

  it("reads a panel only when it comes into view, and all of them on request", async () => {
    const seen: ((e: unknown[]) => void)[] = [];
    vi.stubGlobal("IntersectionObserver", class { constructor(cb: (e: unknown[]) => void) { seen.push(cb); } observe() {} disconnect() {} });
    const { urls } = api();
    render(<ChannelGrid v={viewer()} dataset={ds} />);
    await screen.findByText("Channel 1 · -3000 m/s");
    expect(urls.filter((u) => u.includes("/plane?"))).toEqual([]);
    expect(screen.getByRole("status")).toHaveTextContent("0 of 4 panels loaded");
    await waitFor(() => expect(seen.length).toBeGreaterThan(0)); // the observer is made in an effect, after the first paint
    seen[0]!([{ isIntersecting: true }]);
    await waitFor(() => expect(urls.filter((u) => u.includes("/plane?"))).toHaveLength(1));
    await userEvent.click(screen.getByRole("button", { name: "Load all" }));
    await waitFor(() => expect(urls.filter((u) => u.includes("/plane?"))).toHaveLength(4));
  });

  it("asks for the mean of several channels, a converted unit with its convention, and custom limits, and sends them as typed", async () => {
    const { urls } = api(plan({ panels: [{ index: 0, first: 0, last: 2, channels: 3, mean: true, spectral: { first: -3, last: -1 }, flagged: null }], panel_count: 1, averaging: { channels: 3, order: "per-pixel mean of the finite values of the panel's channels at full resolution, then the block mean" } }));
    const v = viewer({ offers: { spectral: { native_unit: "Hz", targets: [{ unit: "km/s", enabled: true, reason: null, needs_convention: true }] } } });
    render(<ChannelGrid v={v} dataset={ds} />);
    await screen.findByTestId("channel-grid");
    await userEvent.selectOptions(screen.getByLabelText("Spectral labels"), "km/s");
    await userEvent.selectOptions(screen.getByLabelText("Velocity convention"), "relativistic");
    await userEvent.clear(screen.getByLabelText("Every k-th channel"));
    await userEvent.type(screen.getByLabelText("Every k-th channel"), "3");
    await userEvent.clear(screen.getByLabelText("Channels averaged per panel"));
    await userEvent.type(screen.getByLabelText("Channels averaged per panel"), "3");
    await userEvent.type(screen.getByLabelText(/Scale from/), "1");
    await userEvent.type(screen.getByLabelText("Scale to"), "6");
    await userEvent.click(screen.getByRole("button", { name: "Set the scale" }));
    await waitFor(() => expect(urls.some((u) => u.includes("limits=1%2C6"))).toBe(true));
    const last = [...urls].reverse().find((u) => u.includes("/channel-grid"))!;
    expect(last).toContain("stride=3");
    expect(last).toContain("average=3");
    expect(last).toContain("unit=km%2Fs");
    expect(last).toContain("convention=relativistic");
    expect(last).toContain("limits=1%2C6");
    expect(await screen.findByText("Mean of channels 1–3 · -3 to -1 m/s")).toBeInTheDocument();
    expect(screen.getByText(/Each panel is the mean of 3 channels/)).toBeInTheDocument();
  });

  it("refuses bad channel entries before asking, and shows the server's refusal", async () => {
    const { urls } = api();
    render(<ChannelGrid v={viewer()} dataset={ds} />);
    await screen.findByTestId("channel-grid");
    const n = urls.length;
    await userEvent.clear(screen.getByLabelText("Channels averaged per panel"));
    await userEvent.type(screen.getByLabelText("Channels averaged per panel"), "2");
    expect(screen.getByRole("alert")).toHaveTextContent("not more than the stride");
    expect(urls.filter((u) => u.includes("/channel-grid"))).toHaveLength(urls.slice(0, n).filter((u) => u.includes("/channel-grid")).length);
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "70 panels are more than the 64 a grid holds; raise the stride or narrow the range" }), { status: 422, headers: { "Content-Type": "application/json" } })));
    await userEvent.clear(screen.getByLabelText("Every k-th channel"));
    await userEvent.type(screen.getByLabelText("Every k-th channel"), "2");
    expect((await screen.findAllByRole("alert")).map((a) => a.textContent).join(" ")).toContain("more than the 64");
  });

  it("saves the figure with its channel-grid description once every panel is loaded", async () => {
    const { posts } = api();
    render(<ChannelGrid v={viewer()} dataset={ds} />);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("4 of 4 panels loaded"));
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    const b = posts[0]!.body as { datasetId: number; view: Record<string, unknown>; channelGrid: Record<string, unknown>; scene?: unknown };
    expect(posts[0]!.url).toBe("/api/figures");
    expect(b.scene).toBeUndefined();
    expect(b.view).toMatchObject({ kind: "channelgrid", channel: 0, range: "custom", limits: [0.5, 8.25], limitsUnit: "K" });
    expect(b.channelGrid).toMatchObject({ schema: "channel-grid/1", datasetId: 5, first: 0, last: 11, stride: 1, average: 1, panels: 4, display: { limits: [0.5, 8.25], limitsUnit: "K", colormap: "viridis", stretch: "linear" }, preview: { step: 1, panelWidth: 4, panelHeight: 3 } });
    expect(await screen.findByText(/Saved: “saved”/)).toBeInTheDocument();
  });

  it("will not save before every panel is read", async () => {
    vi.stubGlobal("IntersectionObserver", class { observe() {} disconnect() {} });
    const { posts } = api();
    render(<ChannelGrid v={viewer()} dataset={ds} />);
    await screen.findByTestId("channel-grid");
    await userEvent.click(screen.getByRole("button", { name: "Save as figure" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Load every panel before saving");
    expect(posts).toHaveLength(0);
  });

  it("refuses an orientation map and a file with one channel, with the reason", () => {
    api();
    const { unmount } = render(<ChannelGrid v={viewer({ interp: { kind: "axial_orientation" } })} dataset={ds} />);
    expect(screen.getByText(/not defined for the angles of an axial orientation map/)).toBeInTheDocument();
    unmount();
    render(<ChannelGrid v={viewer({ view: { channels: 1, bunit: "K", spectral: null } })} dataset={ds} />);
    expect(screen.getByText(/no channel axis of more than one channel/)).toBeInTheDocument();
  });
});
