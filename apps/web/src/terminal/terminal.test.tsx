import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { clampSize, parseExit } from "../api/terminal";
import TerminalScreen from "./TerminalScreen";

// xterm.js draws on a canvas, which jsdom lacks; the screen's own behaviour is what is tested, against a stand-in terminal.
const written: Uint8Array[] = [];
let dataHandler: (d: string) => void = () => {};
let selection = "";
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 100;
    rows = 30;
    options: Record<string, unknown> = {};
    loadAddon() {}
    open() {}
    focus() {}
    reset() { written.length = 0; }
    write(b: Uint8Array) { written.push(b); }
    paste() {}
    dispose() {}
    hasSelection() { return selection !== ""; }
    getSelection() { return selection; }
    attachCustomKeyEventHandler() {}
    onData(f: (d: string) => void) { dataHandler = f; return { dispose() {} }; }
    onBinary() { return { dispose() {} }; }
    onResize() { return { dispose() {} }; }
    onSelectionChange() { return { dispose() {} }; }
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));

class FakeSocket {
  static last: FakeSocket;
  static OPEN = 1;
  readyState = 0;
  binaryType = "";
  sent: (string | Uint8Array)[] = [];
  closed: number | null = null;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(public url: string, public protocols: string[]) { FakeSocket.last = this; }
  send(d: string | Uint8Array) { this.sent.push(d); }
  close(code?: number) { this.closed = code ?? 1005; this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  say(data: unknown) { this.onmessage?.({ data }); }
}

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
const SESSION = { token: "t".repeat(64), protocol: "vesqira.terminal.v1", expiresInSeconds: 60, idleSeconds: 1800, lifetimeSeconds: 28800 };
const HOSTS = [{ id: 7, name: "cluster", dest: "login.example", python: "", roots: ["/d"], createdAt: "" }];

function setup(reply: () => Promise<Response> = () => json(SESSION)) {
  const posts: unknown[] = [];
  vi.stubGlobal("fetch", (_u: string, init?: RequestInit) => {
    posts.push(JSON.parse(String(init?.body)));
    return reply();
  });
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  render(<TerminalScreen hosts={HOSTS} onClose={() => {}} />);
  return posts;
}

describe("the terminal screen", () => {
  afterEach(() => { vi.unstubAllGlobals(); selection = ""; });

  it("starts nothing until asked, then opens a session on this computer with the token as a subprotocol", async () => {
    const posts = setup();
    expect(posts).toHaveLength(0);
    expect(screen.getByText("No session")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    await waitFor(() => expect(FakeSocket.last).toBeTruthy());
    expect(posts[0]).toMatchObject({ hostId: 0 });
    expect((posts[0] as { tab: string }).tab).toMatch(/^[0-9a-f]{32}$/);
    const ws = FakeSocket.last;
    expect(ws.url).not.toContain(SESSION.token); // never in the URL
    expect(ws.protocols).toEqual(["vesqira.terminal.v1", `vesqira-token.${SESSION.token}`]);
    expect(screen.getByText("Connecting…")).toBeTruthy();
    act(() => ws.open());
    expect(screen.getByText("Live")).toBeTruthy();
    expect(JSON.parse(String(ws.sent[0]))).toEqual({ type: "resize", cols: 100, rows: 30 });
  });

  it("sends typed input as bytes, shows output, and ends with a reason that stays on screen", async () => {
    setup();
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    await waitFor(() => expect(FakeSocket.last).toBeTruthy());
    const ws = FakeSocket.last;
    act(() => ws.open());
    act(() => dataHandler("ls\r"));
    expect(Array.from(ws.sent.at(-1) as Uint8Array)).toEqual([108, 115, 13]);
    act(() => dataHandler("é".repeat(40000))); // larger than one frame: split, none above 32 KiB
    const frames = ws.sent.slice(2) as Uint8Array[];
    expect(frames.length).toBeGreaterThan(1);
    expect(Math.max(...frames.map((f) => f.length))).toBeLessThanOrEqual(32 << 10);
    act(() => ws.say(new TextEncoder().encode("hello").buffer));
    expect(new TextDecoder().decode(written.at(-1))).toBe("hello");
    act(() => ws.say(JSON.stringify({ type: "exit", reason: "idle timeout", code: null })));
    expect(screen.getByText("Ended")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("Session ended: idle timeout");
    expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
  });

  it("never reconnects by itself: a new session is a new request and a new token", async () => {
    const posts = setup();
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    await waitFor(() => expect(FakeSocket.last).toBeTruthy());
    const first = FakeSocket.last;
    act(() => first.open());
    act(() => first.onclose?.()); // the connection drops with no message from the server
    expect(screen.getByRole("status").textContent).toContain("the connection closed");
    await new Promise((r) => setTimeout(r, 50));
    expect(posts).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "New session" }));
    await waitFor(() => expect(posts).toHaveLength(2));
    await waitFor(() => expect(FakeSocket.last).not.toBe(first));
  });

  it("opens a session on the chosen host by id", async () => {
    const posts = setup();
    await userEvent.selectOptions(screen.getByLabelText("Where"), "7");
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    await waitFor(() => expect(posts[0]).toMatchObject({ hostId: 7 }));
  });

  it("says plainly when a session could not be started, and when the socket was refused", async () => {
    setup(() => json({ error: "host not found" }, 404));
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("host not found");
    expect(screen.getByText("Not started")).toBeTruthy();
  });

  it("reports a refused handshake as not started", async () => {
    setup();
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    await waitFor(() => expect(FakeSocket.last).toBeTruthy());
    act(() => FakeSocket.last.onclose?.());
    expect((await screen.findByRole("alert")).textContent).toContain("Nothing was started");
  });

  it("asks before closing a live session, and ending a session closes the socket", async () => {
    setup();
    await userEvent.click(screen.getByRole("button", { name: "Open session" }));
    await waitFor(() => expect(FakeSocket.last).toBeTruthy());
    const ws = FakeSocket.last;
    act(() => ws.open());
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.getByRole("alert").textContent).toContain("ends the shell");
    await userEvent.click(screen.getByRole("button", { name: "Keep it open" }));
    await userEvent.click(screen.getByRole("button", { name: "End session" }));
    expect(ws.closed).toBe(1000);
    expect(screen.getByRole("status").textContent).toContain("you ended the session");
  });

  it("copies only when something is selected", async () => {
    setup();
    expect((screen.getByRole("button", { name: "Copy" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("terminal helpers", () => {
  it("reads only the server's exit message", () => {
    expect(parseExit('{"type":"exit","reason":"shell exited","code":3}')).toEqual({ reason: "shell exited", code: 3 });
    expect(parseExit('{"type":"exit","reason":"x","code":null}')?.code).toBeNull();
    expect(parseExit('{"type":"other"}')).toBeNull();
    expect(parseExit("nope")).toBeNull();
  });
  it("keeps sizes inside what the server accepts", () => {
    expect(clampSize(0, 0)).toEqual({ cols: 1, rows: 1 });
    expect(clampSize(9999, 9999)).toEqual({ cols: 500, rows: 200 });
    expect(clampSize(80.7, 24.2)).toEqual({ cols: 80, rows: 24 });
  });
});
