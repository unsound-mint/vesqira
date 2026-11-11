import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Notification } from "../api/notifications";
import { NotificationBell } from "./NotificationBell";
import { NotifyToggle } from "./NotifyToggle";
import { useNotifications } from "./useNotifications";

const now = () => new Date().toISOString();
const ev = (over: Partial<Notification>): Notification => ({ id: 1, at: now(), kind: "completed", title: "Job vgt-a finished (exit 0:0) — results not yet checked", body: "Slurm reports COMPLETED on Cluster.", jobId: 5, runId: null, read: false, ...over });

/** A server that keeps the list and the read marks, so what the page does is checked against what the API would answer. */
function server(initial: Notification[]) {
  const state = { list: initial, reads: [] as unknown[] };
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (url.startsWith("/api/notifications/read")) {
      const b = JSON.parse(String(init?.body)) as { ids?: number[]; all?: boolean };
      state.reads.push(b);
      state.list = state.list.map((n) => (b.all || b.ids?.includes(n.id) ? { ...n, read: true } : n));
    }
    return new Response(JSON.stringify({ notifications: state.list, unread: state.list.filter((n) => !n.read).length }), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return state;
}

function Harness({ onJob = () => {}, onRun = () => {}, onNew }: { onJob?: (id: number) => void; onRun?: (id: number) => void; onNew?: (n: Notification[]) => void }) {
  const n = useNotifications(onNew);
  return <NotificationBell items={n.items} unread={n.unread} onOpened={n.markAllRead} onOpenJob={onJob} onOpenRun={onRun} />;
}

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("notification bell", () => {
  it("shows the unread count in its name and as text", async () => {
    server([ev({ id: 2, read: false }), ev({ id: 1, read: true }), ev({ id: 3, read: false })]);
    render(<Harness />);
    const bell = await screen.findByRole("button", { name: "Notifications, 2 unread" });
    expect(bell).toHaveTextContent("2");
  });

  it("has no count when everything is read", async () => {
    server([ev({ read: true })]);
    render(<Harness />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(await screen.findByRole("button", { name: "Notifications" })).not.toHaveTextContent(/\d/);
  });

  it("lists the events with the words the server gave, and marks them read when opened", async () => {
    const st = server([ev({ id: 2, kind: "failed", title: "Job vgt-b failed (exit 1:0)", body: "Slurm reports FAILED on Cluster." }), ev({ id: 1 })]);
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(await screen.findByRole("button", { name: "Notifications, 2 unread" }));
    const list = await screen.findByRole("region", { name: "Notifications" });
    expect(list).toHaveTextContent("Job vgt-b failed (exit 1:0)");
    expect(list).toHaveTextContent("results not yet checked");
    expect(list).not.toHaveTextContent(/success/i);
    await waitFor(() => expect(st.reads).toEqual([{ ids: [2, 1] }]));
    // the badge clears, and the two events that were new keep a text mark while the list is open
    expect(await screen.findByRole("button", { name: "Notifications" })).toBeInTheDocument();
    expect(screen.getAllByText("New")).toHaveLength(2);
  });

  it("goes to the job or the run and closes", async () => {
    server([ev({ id: 2, kind: "imported", title: "Results of job vgt-a imported as a run", jobId: 5, runId: 9 })]);
    const onJob = vi.fn();
    const onRun = vi.fn();
    const user = userEvent.setup();
    render(<Harness onJob={onJob} onRun={onRun} />);
    await user.click(await screen.findByRole("button", { name: /Notifications/ }));
    await user.click(await screen.findByRole("button", { name: "Open run" }));
    expect(onRun).toHaveBeenCalledWith(9);
    expect(screen.queryByRole("region", { name: "Notifications" })).toBeNull();
    await user.click(screen.getByRole("button", { name: /Notifications/ }));
    await user.click(await screen.findByRole("button", { name: "Open job" }));
    expect(onJob).toHaveBeenCalledWith(5);
  });

  it("closes on Escape and says plainly when there is nothing", async () => {
    server([]);
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(await screen.findByRole("button", { name: "Notifications" }));
    expect(await screen.findByText(/Nothing yet/)).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("region", { name: "Notifications" })).toBeNull();
  });

  it("asks again every 30 seconds and tells the app what is new, without replaying the backlog on first run", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const st = server([ev({ id: 1 })]);
    const onNew = vi.fn();
    render(<Harness onNew={onNew} />);
    await screen.findByRole("button", { name: /Notifications/ });
    expect(onNew).not.toHaveBeenCalled(); // first run: the existing event is the baseline, not news
    st.list = [ev({ id: 2, title: "Job vgt-b failed" }), ...st.list];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    await waitFor(() => expect(onNew).toHaveBeenCalledTimes(1));
    expect(onNew.mock.calls[0]![0].map((n: Notification) => n.id)).toEqual([2]);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(onNew).toHaveBeenCalledTimes(1); // the same event is not news twice
  });
});

describe("desktop notifications", () => {
  function fakeNotification(permission: NotificationPermission) {
    const shown: { title: string; body?: string }[] = [];
    const request = vi.fn(async () => permission);
    class N {
      static permission: NotificationPermission = "default";
      static requestPermission = request;
      constructor(title: string, opts?: { body?: string }) {
        shown.push({ title, body: opts?.body });
      }
    }
    vi.stubGlobal("Notification", N);
    return { shown, request, N };
  }

  it("never asks for permission until the person clicks the toggle", async () => {
    const { request } = fakeNotification("granted");
    server([ev({ id: 1 })]);
    const user = userEvent.setup();
    render(<><Harness /><NotifyToggle /></>);
    await screen.findByRole("button", { name: /Notifications/ });
    expect(request).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Notify me when jobs finish" }));
    expect(request).toHaveBeenCalledTimes(1);
    expect(await screen.findByRole("button", { name: "Stop desktop notifications" })).toBeInTheDocument();
  });

  it("shows a desktop notice for a new event once it is allowed, and not for events that were already there", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { shown, N } = fakeNotification("granted");
    const st = server([ev({ id: 1, title: "old event" })]);
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<><Harness /><NotifyToggle /></>);
    await screen.findByRole("button", { name: /Notifications/ });
    await user.click(screen.getByRole("button", { name: "Notify me when jobs finish" }));
    N.permission = "granted";
    st.list = [ev({ id: 2, title: "Job vgt-b failed (exit 1:0)", body: "Slurm reports FAILED on Cluster." }), ...st.list];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    await waitFor(() => expect(shown).toEqual([{ title: "Job vgt-b failed (exit 1:0)", body: "Slurm reports FAILED on Cluster." }]));
  });

  it("stays quiet when the person did not choose it, even if the browser would allow it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { shown, N } = fakeNotification("granted");
    N.permission = "granted";
    const st = server([ev({ id: 1 })]);
    render(<Harness />);
    await screen.findByRole("button", { name: /Notifications/ });
    st.list = [ev({ id: 2 }), ...st.list];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(shown).toEqual([]);
  });

  it("says so when the window has no Notification API, and the bell still works", async () => {
    server([ev({ id: 1 })]);
    render(<><Harness /><NotifyToggle /></>);
    expect(screen.getByText(/cannot show desktop notifications/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Notify me when jobs finish" })).toBeNull();
    expect(await screen.findByRole("button", { name: /Notifications/ })).toBeInTheDocument();
  });

  it("explains a blocked permission instead of offering a button that cannot work", () => {
    const { N } = fakeNotification("denied");
    N.permission = "denied";
    render(<NotifyToggle />);
    expect(screen.getByText(/blocked for this window/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Notify me/ })).toBeNull();
  });

  it("makes the blocked notice a hint that goes away when dismissed, and stays away in this browser", async () => {
    const { N } = fakeNotification("denied");
    N.permission = "denied";
    const { unmount } = render(<NotifyToggle />);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss this hint" }));
    expect(screen.queryByText(/blocked for this window/)).toBeNull();
    unmount();
    render(<NotifyToggle />);
    expect(screen.queryByText(/blocked for this window/)).toBeNull();
  });
});
