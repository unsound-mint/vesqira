import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../api/hosts";
import type { Transfer } from "../api/transfers";
import { ACTIVE_MS, IDLE_MS, Transfers } from "./Transfers";
import { etaText, progressText, stateText, verificationText } from "./transferText";

const GiB = 2 ** 30;
const host: Host = { id: 4, name: "Cluster", dest: "login1", python: "python3", roots: ["/data"], createdAt: "" };

const tr = (over: Partial<Transfer> = {}): Transfer => ({
  id: 1, hostId: 4, direction: "download", kind: "file", sourcePath: "/data/cubes/cube.fits", destDir: "/home/me/data", name: "cube.fits", finalName: "cube.fits",
  destPath: "/home/me/data/cube.fits", policy: "ask", state: "queued", detail: "", totalBytes: 0, doneBytes: 0, rateBps: 0, etaSeconds: -1, bwlimitKBps: 0, compress: true,
  checksum: false, attempts: 0, fails: 0, nextAttemptAt: null, warning: "", datasetId: null, createdAt: "2026-11-11T10:00:00Z", startedAt: null, finishedAt: null,
  verification: { status: "", method: "", note: "", sourceSha256: "", destSha256: "", bytes: 0 },
  ...over,
});
const verified = (over: Partial<Transfer> = {}) => tr({ state: "done", totalBytes: 4 * GiB, doneBytes: 4 * GiB, verification: { status: "verified", method: "sha256", note: "same", sourceSha256: "ab".repeat(32), destSha256: "ab".repeat(32), bytes: 4 * GiB }, ...over });

function mockFetch(initial: Transfer[]) {
  let list = initial;
  const calls: { method: string; url: string; body?: any }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const ok = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });
    if (method === "GET" && url === "/api/transfers") return ok({ transfers: list });
    if (url === "/api/transfers" && method === "POST") {
      const t = tr({ id: 9, direction: body.direction, sourcePath: body.sourcePath, destDir: body.destDir, name: body.sourcePath.split("/").pop(), warning: "This is 5.0 GiB and the file system reports 1.0 GiB available." });
      list = [t, ...list];
      return ok({ transfer: t }, 201);
    }
    const m = /^\/api\/transfers\/(\d+)\/(\w+)$/.exec(url);
    if (m) {
      const id = Number(m[1]);
      const cur = list.find((x) => x.id === id)!;
      if (m[2] === "resolve") list = list.map((x) => (x.id === id ? { ...x, state: "queued", policy: body.policy, detail: "" } : x));
      if (m[2] === "cancel") list = list.map((x) => (x.id === id ? { ...x, state: "cancelled" } : x));
      if (m[2] === "retry") list = list.map((x) => (x.id === id ? { ...x, state: "queued", detail: "" } : x));
      if (m[2] === "remove") list = list.filter((x) => x.id !== id);
      if (m[2] === "register") {
        list = list.map((x) => (x.id === id ? { ...x, datasetId: 77 } : x));
        return ok({ dataset: { id: 77 }, transfer: cur }, 201);
      }
      return ok({ transfer: cur, removed: true });
    }
    return new Response("{}", { status: 404 });
  }));
  return calls;
}
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

describe("the transfer words", () => {
  it("never says Done without saying what was checked", () => {
    expect(stateText(verified())).toBe("Done, verified");
    expect(stateText(tr({ state: "done", verification: { status: "unverified", method: "sha256", note: "x", sourceSha256: "", destSha256: "", bytes: 0 } }))).toBe("Arrived, NOT verified");
    expect(verificationText(verified())).toMatch(/size and SHA-256 are the same on both machines/);
    expect(verificationText(verified({ verification: { status: "verified", method: "rsync-checksum", note: "", sourceSha256: "", destSha256: "", bytes: 0 } }))).toMatch(/not SHA-256/);
    expect(verificationText(tr({ state: "done", verification: { status: "unverified", method: "sha256", note: "the sizes differ", sourceSha256: "", destSha256: "", bytes: 0 } }))).toBe("Not verified: the sizes differ");
    expect(verificationText(tr())).toBeNull();
  });
  it("shows an unknown total as so far, and no rate for a transfer that is not running", () => {
    expect(progressText(tr({ state: "running", doneBytes: 3 * GiB }))).toBe("3.00 GiB so far");
    expect(progressText(tr({ state: "running", totalBytes: 4 * GiB, doneBytes: GiB, rateBps: 10 * 2 ** 20, etaSeconds: 300 }))).toBe("1.00 GiB of 4.00 GiB · 10.0 MiB/s · about 5 min left");
    expect(progressText(tr({ state: "waiting", totalBytes: 4 * GiB, doneBytes: GiB, rateBps: 10 }))).toBe("1.00 GiB of 4.00 GiB");
    expect(etaText(20)).toBe("under a minute left");
    expect(etaText(7500)).toBe("about 2 h 5 min left");
    expect(etaText(-1)).toBe("");
  });
});

describe("the transfers panel", () => {
  it("shows a running transfer with bytes, rate, estimate and a progress bar a screen reader can read", async () => {
    mockFetch([tr({ state: "running", totalBytes: 4 * GiB, doneBytes: GiB, rateBps: 10 * 2 ** 20, etaSeconds: 300 })]);
    render(<Transfers hosts={[host]} />);
    const item = await screen.findByRole("listitem", { name: "Download cube.fits" });
    expect(within(item).getByText("Transferring")).toBeInTheDocument();
    expect(within(item).getByText(/1.00 GiB of 4.00 GiB · 10.0 MiB\/s · about 5 min left/)).toBeInTheDocument();
    expect(within(item).getByRole("progressbar", { name: "cube.fits: 25 %" })).toHaveAttribute("max", String(4 * GiB));
    expect(within(item).getByText("/data/cubes/cube.fits")).toBeInTheDocument();
    expect(within(item).getByText("from Cluster")).toBeInTheDocument();
    expect(within(item).getByRole("button", { name: "Cancel and delete the partial file" })).toBeInTheDocument();
  });

  it("explains a retry in words and does not call it a failure", async () => {
    mockFetch([tr({ state: "waiting", totalBytes: 4 * GiB, doneBytes: 2 * GiB, fails: 2, detail: "rsync exited with code 12. Trying again in 40s, resuming from the partial file." })]);
    render(<Transfers hosts={[host]} />);
    const item = await screen.findByRole("listitem", { name: "Download cube.fits" });
    expect(within(item).getByText("Waiting to retry")).toBeInTheDocument();
    expect(within(item).getByText(/Trying again in 40s, resuming from the partial file/)).toBeInTheDocument();
    expect(within(item).getByText(/2.00 GiB of 4.00 GiB/)).toBeInTheDocument();
    expect(within(item).queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });

  it("shows a verified download with its hash, and registers it as a dataset once", async () => {
    const calls = mockFetch([verified()]);
    const onRegistered = vi.fn(async () => undefined);
    render(<Transfers hosts={[host]} onRegistered={onRegistered} />);
    const item = await screen.findByRole("listitem", { name: "Download cube.fits" });
    expect(within(item).getByText("Done, verified")).toBeInTheDocument();
    expect(within(item).getByText("ab".repeat(32))).toBeInTheDocument();
    await userEvent.click(within(item).getByRole("button", { name: "Register as a dataset" }));
    await waitFor(() => expect(onRegistered).toHaveBeenCalled());
    expect(calls.filter((c) => c.url.endsWith("/register"))).toHaveLength(1);
    expect(await screen.findByText("Registered as a dataset.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Register as a dataset" })).not.toBeInTheDocument();
  });

  it("does not offer to register an unverified copy or an upload, and offers a content comparison instead", async () => {
    const calls = mockFetch([
      tr({ id: 2, state: "done", verification: { status: "unverified", method: "sha256", note: "the SHA-256 of the copy differs from the source's", sourceSha256: "a", destSha256: "b", bytes: 5 } }),
      verified({ id: 3, direction: "upload", name: "maps.fits", finalName: "maps.fits" }),
    ]);
    render(<Transfers hosts={[host]} />);
    const bad = await screen.findByRole("listitem", { name: "Download cube.fits" });
    expect(within(bad).getByText("Arrived, NOT verified")).toBeInTheDocument();
    expect(within(bad).getByText(/Not verified: the SHA-256 of the copy differs/)).toBeInTheDocument();
    expect(within(bad).queryByRole("button", { name: /Register/ })).not.toBeInTheDocument();
    await userEvent.click(within(bad).getByRole("button", { name: "Copy again and compare contents" }));
    expect(calls.some((c) => c.url === "/api/transfers/2/retry")).toBe(true);
    const up = screen.getByRole("listitem", { name: "Upload maps.fits" });
    expect(within(up).queryByRole("button", { name: /Register/ })).not.toBeInTheDocument();
    expect(within(up).getByText("to Cluster")).toBeInTheDocument();
  });

  it("says that copying a folder again overwrites differing files, and only does it after a second click", async () => {
    const calls = mockFetch([
      tr({ id: 4, name: "run1", finalName: "run1", kind: "dir", state: "done", verification: { status: "unverified", method: "rsync-checksum", note: "rsync's content comparison found 1 other difference(s): >fc.t...... a.fits", sourceSha256: "", destSha256: "", bytes: 0 } }),
    ]);
    render(<Transfers hosts={[host]} />);
    const item = await screen.findByRole("listitem", { name: "Download run1" });
    expect(within(item).queryByRole("button", { name: "Copy again and compare contents" })).not.toBeInTheDocument();
    await userEvent.click(within(item).getByRole("button", { name: "Copy again, overwriting differing files…" }));
    expect(calls.some((c) => c.url.endsWith("/retry"))).toBe(false);
    expect(within(item).getByText(/overwrites each one/)).toHaveTextContent("including a file you edited there");
    await userEvent.click(within(item).getByRole("button", { name: "Overwrite differing files" }));
    expect(calls.filter((c) => c.url === "/api/transfers/4/retry")).toHaveLength(1);
  });

  it("asks for a decision on a conflict, and Replace needs a second, explicit click", async () => {
    const calls = mockFetch([tr({ state: "conflict", detail: "cube.fits is already on this computer (a file of 7.0 KiB) and differs from the source. Nothing was changed; choose what to do." })]);
    render(<Transfers hosts={[host]} />);
    const item = await screen.findByRole("listitem", { name: "Download cube.fits" });
    expect(within(item).getByText("Needs your decision")).toBeInTheDocument();
    expect(within(item).getByText(/Nothing was changed/)).toBeInTheDocument();
    await userEvent.click(within(item).getByRole("button", { name: "Replace…" }));
    expect(calls.filter((c) => c.url.endsWith("/resolve"))).toHaveLength(0);
    expect(within(item).getByText(/Replace overwrites/)).toHaveTextContent("/home/me/data/cube.fits");
    await userEvent.click(within(item).getByRole("button", { name: "Keep it" }));
    expect(within(item).getByRole("button", { name: "Skip" })).toBeInTheDocument();
    await userEvent.click(within(item).getByRole("button", { name: "Keep both (new name)" }));
    await waitFor(() => expect(calls.find((c) => c.url.endsWith("/resolve"))).toMatchObject({ body: { policy: "keep_both" } }));
  });

  it("queues a download with the chosen policy and limit, shows the server's warning on the row, and sends nothing hidden", async () => {
    const calls = mockFetch([]);
    render(<Transfers hosts={[host]} />);
    expect(await screen.findByText(/No transfers yet/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "New transfer…" }));
    await userEvent.type(screen.getByRole("combobox", { name: "File or folder on the host" }), "/data/cubes/cube.fits");
    await userEvent.type(screen.getByRole("combobox", { name: "Into this folder on this computer" }), "/home/me/data");
    await userEvent.selectOptions(screen.getByLabelText("If the name is already there"), "keep_both");
    await userEvent.type(screen.getByLabelText(/Speed limit in KiB\/s/), "2000");
    await userEvent.click(screen.getByRole("button", { name: "Queue transfer" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")).toBeDefined());
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ direction: "download", hostId: 4, sourcePath: "/data/cubes/cube.fits", destDir: "/home/me/data", policy: "keep_both", bwlimitKBps: 2000, compress: true });
    expect(await screen.findByText(/reports 1.0 GiB available/)).toBeInTheDocument();
  });

  it("refuses a speed limit that is not a number before anything is sent", async () => {
    const calls = mockFetch([]);
    render(<Transfers hosts={[host]} />);
    await userEvent.click(await screen.findByRole("button", { name: "New transfer…" }));
    await userEvent.type(screen.getByRole("combobox", { name: "File or folder on the host" }), "/data/a.fits");
    await userEvent.type(screen.getByRole("combobox", { name: "Into this folder on this computer" }), "/home/me");
    await userEvent.type(screen.getByLabelText(/Speed limit in KiB\/s/), "fast");
    expect(screen.getByRole("button", { name: "Queue transfer" })).toBeDisabled();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows the server's refusal on the row it came from", async () => {
    mockFetch([verified()]);
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init?: RequestInit) => {
      if ((init?.method ?? "GET") === "GET") return new Response(JSON.stringify({ transfers: [verified()] }), { status: 200 });
      return new Response(JSON.stringify({ error: "the file on this computer changed since it was verified; copy it again before registering it" }), { status: 409, headers: { "content-type": "application/json" } });
    }));
    render(<Transfers hosts={[host]} />);
    await userEvent.click(await screen.findByRole("button", { name: "Register as a dataset" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("changed since it was verified");
  });

  it("reads the list every few seconds while something is live and rarely when nothing is", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = mockFetch([tr({ state: "running", totalBytes: GiB, doneBytes: 1 })]);
    const reads = () => calls.filter((c) => c.method === "GET").length;
    const { unmount } = render(<Transfers hosts={[host]} />);
    await screen.findByRole("listitem", { name: "Download cube.fits" });
    const first = reads();
    await act(async () => { await vi.advanceTimersByTimeAsync(ACTIVE_MS * 3 + 100); });
    expect(reads()).toBeGreaterThanOrEqual(first + 3);
    unmount();
    const idleCalls = mockFetch([verified()]);
    render(<Transfers hosts={[host]} />);
    await screen.findByRole("listitem", { name: "Download cube.fits" });
    const n = idleCalls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(ACTIVE_MS * 3 + 100); });
    expect(idleCalls.length).toBe(n);
    await act(async () => { await vi.advanceTimersByTimeAsync(IDLE_MS); });
    expect(idleCalls.length).toBeGreaterThan(n);
  });

  it("removes a finished transfer from the list without touching files", async () => {
    const calls = mockFetch([tr({ state: "cancelled" })]);
    render(<Transfers hosts={[host]} />);
    const item = await screen.findByRole("listitem", { name: "Download cube.fits" });
    expect(within(item).getByRole("button", { name: "Try again" })).toBeInTheDocument();
    await userEvent.click(within(item).getByRole("button", { name: "Remove from the list" }));
    await waitFor(() => expect(screen.queryByRole("listitem", { name: "Download cube.fits" })).not.toBeInTheDocument());
    expect(calls.some((c) => c.url === "/api/transfers/1/remove")).toBe(true);
  });

  it("shows nothing at all when this server has no transfer queue", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "not found" }), { status: 404, headers: { "content-type": "application/json" } })));
    const { container } = render(<Transfers hosts={[host]} />);
    await waitFor(() => expect(container).toBeEmptyDOMElement());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
