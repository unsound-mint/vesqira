import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FamilyMapsCleanup } from "./FamilyMapsCleanup";
import { SweepFamilyPanel } from "./SweepFamilyPanel";

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const keys = Object.keys(routes).filter((k) => url.startsWith(k)).sort((a, b) => b.length - a.length);
    const r = keys.length ? routes[keys[0]!]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

const fam = {
  sweepId: 4, name: "kernel sweep", role: "orientation", roles: [{ role: "orientation", jobs: 3 }], referenceId: 11, note: "",
  members: [{ jobId: 1, label: "kernel = 3", datasetId: 11, name: "a.fits" }, { jobId: 2, label: "kernel = 5", datasetId: 12, name: "b.fits" }],
  excluded: [{ jobId: 3, label: "kernel = 7", reason: "on another grid than the first map" }],
};

describe("sweep as a family", () => {
  it("opens the compatible members with their labels and passes the left-out ones on with the reason", async () => {
    mockFetch({ "/api/sweeps/4/family": () => ({ body: fam }) });
    const onOpen = vi.fn();
    render(<SweepFamilyPanel sweepId={4} onOpen={onOpen} />);
    expect(await screen.findByText("on another grid than the first map")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open 2 maps in the family view" }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ ids: [11, 12], labels: { 11: "kernel = 3", 12: "kernel = 5" }, referenceId: 11, excluded: [{ label: "kernel = 7", reason: "on another grid than the first map" }] }));
  });

  it("does not offer a comparison of fewer than two maps", async () => {
    mockFetch({ "/api/sweeps/4/family": () => ({ body: { ...fam, members: fam.members.slice(0, 1) } }) });
    render(<SweepFamilyPanel sweepId={4} onOpen={vi.fn()} />);
    expect(await screen.findByRole("button", { name: "Open 1 maps in the family view" })).toBeDisabled();
  });
});

const map = (id: number, holds: string[]) => ({ datasetId: id, name: `diff${id}.fits`, role: "difference", member: 1, run: "r1", referenceId: 1, hostId: 0, sizeBytes: 4096, createdAt: "2026-11-01T10:00:00Z", holds });

describe("family map clean-up", () => {
  it("cannot choose a held map, says why, and removes only the chosen free ones", async () => {
    let listed = [map(10, []), map(11, ["A figure uses it."])];
    const calls = mockFetch({
      "/api/analysis/family-maps/remove": () => { listed = [map(11, ["A figure uses it."])]; return { body: { results: [{ datasetId: 10, name: "diff10.fits", removed: true, bytes: "removed" }] } }; },
      "/api/analysis/family-maps": () => ({ body: { maps: listed } }),
    });
    vi.stubGlobal("confirm", () => true);
    const changed = vi.fn();
    render(<FamilyMapsCleanup refreshKey={null} onChanged={changed} />);
    const held = await screen.findByLabelText(/diff11.fits/);
    expect(held).toBeDisabled();
    expect(screen.getByText(/A figure uses it/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Choose the maps of this run that can go" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove 1 chosen" }));
    await waitFor(() => expect(changed).toHaveBeenCalledWith([10]));
    const post = calls.find((c) => c.url.endsWith("/remove"))!;
    expect(JSON.parse(String(post.init?.body))).toEqual({ datasetIds: [10] });
    expect(await screen.findByText(/diff10.fits: removed/)).toBeInTheDocument();
  });
});
