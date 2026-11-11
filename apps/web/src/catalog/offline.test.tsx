import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { OfflinePlan, OfflinePrepareState } from "../api/offline";
import { getView } from "../api/planes";
import { resetOfflineState } from "../api/offlineState";
import { OfflineBanner } from "../viewer/OfflineBanner";
import { DatasetOffline } from "./OfflineAction";

const plan = (over: Partial<OfflinePlan> = {}): OfflinePlan => ({
  items: [
    { kind: "dataset", id: 7, name: "lmc_hi.fits", status: "partial", reason: "Kept: view; header; planes 120–180 (1/4 preview). Needs the host: full-resolution planes and analyses.", estimateBytes: 4_000_000, keptBytes: 0 },
    { kind: "figure", id: 2, name: "Peak", status: "offline", reason: "The rendered picture is stored by Vesqira, so nothing is copied.", estimateBytes: 0, keptBytes: 0 },
    { kind: "dataset", id: 9, name: "lost.fits", status: "remote", reason: "the host is not reachable (is the VPN up?). Prepare again when the host is reachable.", estimateBytes: 0, keptBytes: 0 },
  ],
  estimateBytes: 4_000_000, budgetBytes: 256 << 20, storeCapBytes: 2 << 30, storeUsedBytes: 0, fits: true, notes: [],
  ...over,
});
const idle: OfflinePrepareState = { state: "idle", total: 0, done: 0, bytes: 0, current: "", items: [], error: "" };

type Call = { url: string; method: string; body: unknown };
function fakeApi(routes: Record<string, (body: unknown) => { status?: number; json: unknown; headers?: Record<string, string> }>) {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url, method, body });
    const h = routes[`${method} ${url}`] ?? routes[`${method} ${url.split("?")[0]}`];
    if (!h) return new Response(JSON.stringify({ error: `unexpected ${method} ${url}` }), { status: 500 });
    const r = h(body);
    return new Response(JSON.stringify(r.json), { status: r.status ?? 200, headers: { "Content-Type": "application/json", ...(r.headers ?? {}) } });
  }));
  return calls;
}
afterEach(() => { vi.unstubAllGlobals(); resetOfflineState(); });

describe("the preparation sheet", () => {
  it("lists each item as works offline, partially or needs the remote, with the estimate against the budget, and prepares what was planned", async () => {
    let state: OfflinePrepareState = idle;
    const calls = fakeApi({
      "GET /api/offline": () => ({ json: { capBytes: 2 << 30, usedBytes: 0, defaultBudgetBytes: 256 << 20, datasets: [] } }),
      "GET /api/datasets/7/view": () => ({ json: { width: 10, height: 10, channels: 400 } }),
      "POST /api/offline/plan": () => ({ json: plan() }),
      "GET /api/offline/prepare": () => ({ json: state }),
      "POST /api/offline/prepare": () => {
        state = { state: "done", total: 1, done: 1, bytes: 3_900_000, current: "", error: "", items: [{ ...plan().items[0]!, keptBytes: 3_900_000 }] };
        return { status: 202, json: { ...state, state: "running", done: 0 } };
      },
    });
    const user = userEvent.setup();
    render(<DatasetOffline id={7} name="lmc_hi.fits" hostId={3} />);
    await user.click(await screen.findByRole("button", { name: "Prepare for offline…" }));
    const dialog = await screen.findByRole("dialog", {}, { timeout: 8000 }); // pays for loading the lazy sheet
    expect(dialog).toHaveTextContent("never copies the cube");
    // the three statuses are words, not only colours
    await waitFor(() => expect(dialog).toHaveTextContent("Works partially"), { timeout: 4000 });
    expect(dialog).toHaveTextContent("Works offline");
    expect(dialog).toHaveTextContent("Needs the remote");
    expect(dialog).toHaveTextContent("planes 120–180 (1/4 preview)");
    expect(dialog).toHaveTextContent(/About 3\.8\d MiB of your 256\.0 MiB budget/);
    expect(dialog).toHaveTextContent("It fits.");
    // the range starts at the middle of the cube the host reported
    expect(screen.getByLabelText("First channel")).toHaveValue("190");
    await user.click(screen.getByRole("button", { name: "Prepare" }));
    const sent = calls.find((c) => c.method === "POST" && c.url === "/api/offline/prepare");
    expect(sent?.body).toMatchObject({ datasetIds: [7], channels: { from: 190, to: 209 }, detail: "preview", budgetBytes: 256 << 20 });
    await waitFor(() => expect(screen.getByRole("dialog")).toHaveTextContent(/Done\. 3\.7\d MiB kept/), { timeout: 4000 });
  });

  it("does not offer to prepare what does not fit, and says why", async () => {
    fakeApi({
      "GET /api/offline": () => ({ json: { capBytes: 2 << 30, usedBytes: 0, defaultBudgetBytes: 256 << 20, datasets: [] } }),
      "GET /api/datasets/7/view": () => ({ json: { width: 10, height: 10, channels: 4 } }),
      "POST /api/offline/plan": () => ({ json: plan({ fits: false, estimateBytes: 900 << 20, notes: ["The estimate is over the budget: narrow the channel range, choose preview detail, or raise the budget."] }) }),
      "GET /api/offline/prepare": () => ({ json: idle }),
    });
    const user = userEvent.setup();
    render(<DatasetOffline id={7} name="lmc_hi.fits" hostId={3} />);
    await user.click(await screen.findByRole("button", { name: "Prepare for offline…" }));
    const dialog = await screen.findByRole("dialog", {}, { timeout: 8000 });
    await waitFor(() => expect(dialog).toHaveTextContent("It does not fit."), { timeout: 4000 });
    expect(dialog).toHaveTextContent("over the budget");
    expect(screen.getByRole("button", { name: "Prepare" })).toBeDisabled();
  });
});

describe("the dataset's offline section", () => {
  it("says a file on this computer needs no copy", async () => {
    fakeApi({});
    render(<DatasetOffline id={1} name="a.fits" hostId={0} />);
    expect(screen.getByText(/needs no offline copy/)).toBeInTheDocument();
  });

  it("shows what is kept and that a changed file made it stale", async () => {
    fakeApi({ "GET /api/offline": () => ({ json: { capBytes: 1 << 30, usedBytes: 5000, defaultBudgetBytes: 1, datasets: [{ id: 7, name: "a.fits", hostId: 3, copies: 4, bytes: 5000, takenAt: "2026-11-12T10:30:00Z", stale: true, coverage: "view; header; planes 1–2 (1/2 preview)" }] } }) });
    render(<DatasetOffline id={7} name="a.fits" hostId={3} />);
    expect(await screen.findByText("Stale: the file changed")).toBeInTheDocument();
    expect(screen.getByText(/planes 1–2/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Forget offline copy" })).toBeInTheDocument();
  });
});

describe("the viewer's offline banner", () => {
  const mark = JSON.stringify({ copy: true, takenAt: "2026-11-12T10:30:00Z", signature: { hostId: 3, size: 1234, mtimeNs: 5, identity: "sha256:abcdef0123456789" } });

  it("appears when an answer came from a copy, says when and from which file, and goes when the host answers live", async () => {
    fakeApi({ "GET /api/datasets/7/view": (() => {
      let n = 0;
      return () => (++n === 1 ? { json: { width: 1, height: 1, channels: 1 }, headers: { "X-Vesqira-Offline": mark } } : { json: { width: 1, height: 1, channels: 1 } });
    })() });
    render(<OfflineBanner datasetId={7} />);
    expect(screen.queryByTestId("offline-banner")).toBeNull();
    await act(async () => { await getView(7); });
    const banner = await screen.findByRole("status");
    expect(banner).toHaveTextContent("Offline copy");
    expect(banner).toHaveTextContent("1,234 bytes");
    expect(banner).toHaveTextContent("SHA-256 abcdef012345");
    expect(banner).toHaveTextContent("It cannot show anything newer");
    await act(async () => { await getView(7); });
    expect(screen.queryByTestId("offline-banner")).toBeNull();
  });

  it("names what the copy does not hold, in the server's own words", async () => {
    fakeApi({
      "GET /api/datasets/7/view": () => ({ json: { width: 1, height: 1, channels: 1 }, headers: { "X-Vesqira-Offline": mark } }),
      "GET /api/datasets/7/plane": () => ({ status: 503, json: { error: "channel 30 at full resolution is not in the offline copy of a.fits. Kept: view; planes 10–12 (full resolution).", code: "offline_missing" } }),
    });
    render(<OfflineBanner datasetId={7} />);
    await act(async () => { await getView(7); });
    const { getPlane } = await import("../api/planes");
    await act(async () => { await getPlane(7, 30).catch(() => undefined); });
    expect(await screen.findByRole("status")).toHaveTextContent("channel 30 at full resolution is not in the offline copy");
  });

  it("shows nothing for another dataset", async () => {
    fakeApi({ "GET /api/datasets/7/view": () => ({ json: { width: 1, height: 1, channels: 1 }, headers: { "X-Vesqira-Offline": mark } }) });
    render(<OfflineBanner datasetId={8} />);
    await act(async () => { await getView(7); });
    expect(screen.queryByTestId("offline-banner")).toBeNull();
  });
});
