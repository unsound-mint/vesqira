import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host, HostStorage as Answer, RunFolder } from "../api/hosts";
import { HostStorage, REASK_MS } from "./HostStorage";

const GiB = 2 ** 30;
const host: Host = { id: 4, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };
const report = (over: object = {}): Answer => ({
  error: "", cached: false, checkedAt: "2026-11-11T10:00:00Z",
  report: {
    checkedAt: "2026-11-11T10:00:00Z", quotaNote: "",
    quota: { tool: "beegfs-ctl", command: "beegfs-ctl --getquota --uid $(id -u)", entries: [{ scope: "storage pool Default", usedBytes: 395 * GiB, softBytes: null, hardBytes: 400 * GiB, raw: "spectre| 1000|| 395.00 GiB| 400.00 GiB|| 12|unlimited" }] },
    filesystems: [{ root: "/data", filesystem: "beegfs_nodev", mount: "/home", totalBytes: 1000 * GiB, usedBytes: 600 * GiB, availBytes: 400 * GiB, raw: "x" }],
    ...over,
  },
});
const folder = (path: string, name: string, bytes: number | null): RunFolder => ({ path, name, kind: "job", jobId: 1, bytes, partial: false, measuredAt: bytes === null ? null : "2026-11-11T10:00:00Z" });

function mockFetch(opts: { storage?: Answer; folders?: RunFolder[]; sizes?: Record<string, number>; sizeFails?: string[] }) {
  const calls: { method: string; url: string; body?: any }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const ok = (b: unknown) => new Response(JSON.stringify(b), { status: 200 });
    if (url.endsWith("/storage") || url.endsWith("/storage/refresh")) return ok(opts.storage ?? report());
    if (url.endsWith("/folders")) return ok({ folders: opts.folders ?? [] });
    if (url.endsWith("/folders/size")) {
      if (opts.sizeFails?.includes(body.path)) return new Response(JSON.stringify({ error: "du did not finish in 80 s on the host" }), { status: 502 });
      return ok({ path: body.path, kib: (opts.sizes?.[body.path] ?? 1) / 1024, bytes: opts.sizes?.[body.path] ?? 1, partial: false, measuredAt: "2026-11-11T10:05:00Z" });
    }
    return new Response("{}", { status: 404 });
  }));
  return calls;
}
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });
beforeAll(() => import("./HostStorageDialog")); // the dialog is loaded on demand; warm it so a cold transform does not eat a test's wait

describe("host storage", () => {
  it("asks the host nothing until the section is opened", () => {
    const calls = mockFetch({});
    render(<HostStorage host={host} />);
    expect(calls).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Disk and quota" })).toHaveAttribute("aria-expanded", "false");
  });

  it("shows which tool answered with the raw numbers and units, and the free space as the file system's, not the allowance", async () => {
    mockFetch({});
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    expect(await screen.findByText(/Answered by/)).toHaveTextContent("beegfs-ctl");
    const quota = screen.getByText(/395.0 GiB/, { selector: "strong" }).closest("li")!;
    expect(quota).toHaveTextContent("of 400.0 GiB hard limit (99 %)");
    expect(quota).toHaveTextContent("close to the limit");
    expect(within(quota).getByRole("meter", { name: /99 % of the hard limit/ })).toBeInTheDocument();
    expect(quota).toHaveTextContent("as printed: spectre| 1000|| 395.00 GiB| 400.00 GiB|| 12|unlimited");
    expect(screen.getByText(/This is the file system, not your allowance/)).toBeInTheDocument();
    expect(screen.getByText(/400.0 GiB free/)).toBeInTheDocument();
  });

  it("says the quota tool is not available instead of guessing, and still shows free space", async () => {
    mockFetch({ storage: report({ quota: null, quotaNote: "This site's quota tool is not available (none answered), so no quota is shown." }) });
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    expect(await screen.findByText(/quota tool is not available/)).toBeInTheDocument();
    expect(screen.queryByText(/Answered by/)).not.toBeInTheDocument();
    expect(screen.getByText(/400.0 GiB free/)).toBeInTheDocument();
  });

  it("reports an unreachable host and keeps the last look", async () => {
    mockFetch({ storage: { ...report(), error: "the command failed on the host (is the VPN up, and does ssh work for it without a prompt?)" } });
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/Could not look at Cluster: the command failed on the host.*Showing the last look/);
    expect(screen.getByText(/Answered by/)).toBeInTheDocument();
  });

  it("asks again only through Refresh, and by itself no more often than every ten minutes while open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = mockFetch({});
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    await screen.findByText(/Answered by/);
    const looks = () => calls.filter((c) => c.url.endsWith("/storage") || c.url.endsWith("/storage/refresh"));
    expect(looks()).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(REASK_MS - 1000); });
    expect(looks()).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(looks()).toHaveLength(2);
    expect(looks()[1]!.method).toBe("GET"); // the server decides whether that is a new look
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(looks()).toHaveLength(3));
    expect(looks()[2]).toMatchObject({ method: "POST", url: "/api/hosts/4/storage/refresh" });
    // closing the dialog stops the asking
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(REASK_MS * 2); });
    expect(looks()).toHaveLength(3);
  });
});

describe("sizes of my runs", () => {
  it("lists folders unmeasured, measures one on demand, and keeps the largest first", async () => {
    const calls = mockFetch({ folders: [folder("/data/runs/a", "run-a", null), folder("/data/runs/b", "run-b", 3 * GiB), folder("/data/runs/c", "run-c", null)], sizes: { "/data/runs/a": 88 * GiB, "/data/runs/c": 23 * GiB } });
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    const list = await screen.findByRole("list", { name: /Run folders on Cluster, largest first/ });
    // unmeasured is not a size: those folders are one summary line, not rows of dashes
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText(/2 folders not measured yet/)).toBeInTheDocument();
    expect(calls.filter((c) => c.url.endsWith("/folders/size"))).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: "Measure run-a" }));
    await waitFor(() => expect(within(list).getByText("88.0 GiB")).toBeInTheDocument());
    const names = within(list).getAllByRole("listitem").map((li) => /run-[abc]/.exec(li.textContent ?? "")![0]);
    expect(names).toEqual(["run-a", "run-b"]);
    expect(calls.filter((c) => c.url.endsWith("/folders/size"))).toEqual([{ method: "POST", url: "/api/hosts/4/folders/size", body: { path: "/data/runs/a" } }]);
  });

  it("measures all one at a time and carries on after a failure, showing it on its row", async () => {
    const calls = mockFetch({ folders: [folder("/data/runs/a", "run-a", null), folder("/data/runs/b", "run-b", null), folder("/data/runs/c", "run-c", null)], sizes: { "/data/runs/a": 5 * GiB, "/data/runs/c": 7 * GiB }, sizeFails: ["/data/runs/b"] });
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    await userEvent.click(await screen.findByRole("button", { name: /Measure all \(3 not yet measured\)/ }));
    await waitFor(() => expect(screen.getByText("7.00 GiB")).toBeInTheDocument());
    expect(calls.filter((c) => c.url.endsWith("/folders/size")).map((c) => c.body.path)).toEqual(["/data/runs/a", "/data/runs/b", "/data/runs/c"]);
    expect(screen.getByRole("alert")).toHaveTextContent("du did not finish in 80 s on the host");
    expect(screen.getByText("5.00 GiB")).toBeInTheDocument();
  });

  it("offers no way to delete anything", async () => {
    mockFetch({ folders: [folder("/data/runs/a", "run-a", 1 * GiB)] });
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    await screen.findByRole("list", { name: /Run folders/ });
    expect(screen.queryByRole("button", { name: /delete|remove|archive|clean/i })).not.toBeInTheDocument();
  });
});

describe("the dialog", () => {
  it("shows the quota bar in the warning colour from 90 % and in the normal one below", async () => {
    mockFetch({});
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    expect(await screen.findByRole("meter", { name: /99 % of the hard limit/ })).toHaveAttribute("data-tone", "warn");
    expect(screen.getByRole("meter", { name: /60 % of the file system used/ })).toHaveAttribute("data-tone", "ok");
  });

  it("keeps the raw tool output one click away, with the sentence in view", async () => {
    mockFetch({});
    render(<HostStorage host={host} />);
    await userEvent.click(screen.getByRole("button", { name: "Disk and quota" }));
    const raw = await screen.findByText(/spectre\| 1000\|\| 395.00 GiB/);
    expect(raw.closest("details")).not.toHaveAttribute("open");
  });
});
