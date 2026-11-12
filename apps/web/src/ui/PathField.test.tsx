import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PathField, getHostOffer, subscribeHostOffer } from ".";

type Entry = { name: string; path: string; dir: boolean };
const e = (base: string, name: string, dir = false): Entry => ({ name, path: `${base}/${name}`, dir });

let calls: string[];
let answer: (url: URL) => Promise<Response> | Response;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const list = (base: string, entries: Entry[], truncated = false) => json({ base, entries, truncated });

beforeEach(() => {
  calls = [];
  answer = () => list("/", []);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const u = new URL(String(input), "http://localhost");
    calls.push(`${init?.method ?? "GET"} ${u.pathname}${u.search}`);
    return answer(u);
  }));
});
afterEach(() => vi.unstubAllGlobals());

function Harness(p: { start?: string; hostId?: number | null; kind?: "dir" | "file" | "any"; onSubmit?: () => void }) {
  const [v, setV] = useState(p.start ?? "");
  return (
    <form onSubmit={(ev) => { ev.preventDefault(); p.onSubmit?.(); }}>
      <PathField label="Folder" value={v} onChange={setV} kind={p.kind ?? "dir"} hostId={p.hostId} />
      <button type="submit">Go</button>
    </form>
  );
}

const box = () => screen.getByRole("combobox", { name: "Folder" }) as HTMLInputElement;

describe("PathField", () => {
  it("lists the names that match what was typed and takes one by click, with a slash for a folder", async () => {
    answer = () => list("/data", [e("/data", "runs", true), e("/data", "cube.fits")]);
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(box(), "/data/");
    const opts = await screen.findAllByRole("option");
    expect(opts.map((o) => o.textContent)).toEqual(["runs/", "cube.fits"]);
    expect(box()).toHaveAttribute("aria-expanded", "true");
    expect(calls.at(-1)).toContain("/api/fs/complete?prefix=%2Fdata%2F&kind=dir");
    await user.click(opts[0] as HTMLElement);
    expect(box().value).toBe("/data/runs/"); // a folder shows what is inside it next
    await waitFor(() => expect(calls.at(-1)).toContain("prefix=%2Fdata%2Fruns%2F"));
  });

  it("waits for a pause in typing and asks nothing for a text that is not a path", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(box(), "relative/x");
    await new Promise((r) => setTimeout(r, 200));
    expect(calls).toEqual([]);
    await user.clear(box());
    await user.type(box(), "/abc");
    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    expect(calls.length).toBe(1); // four keystrokes, one request
  });

  it("moves with the arrow keys, takes the highlighted name with Enter without submitting, and Escape closes", async () => {
    answer = () => list("/data", [e("/data", "a", true), e("/data", "b", true)]);
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<Harness onSubmit={onSubmit} />);
    await user.type(box(), "/data/");
    await screen.findAllByRole("option");
    await user.keyboard("{ArrowDown}{ArrowDown}");
    const [first, second] = screen.getAllByRole("option");
    expect(first).toHaveAttribute("aria-selected", "false");
    expect(second).toHaveAttribute("aria-selected", "true");
    expect(box()).toHaveAttribute("aria-activedescendant", second?.id);
    await user.keyboard("{ArrowUp}{ArrowUp}"); // wraps to the last
    expect(screen.getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    expect(box().value).toBe("/data/b/");
    expect(onSubmit).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
    expect(box()).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{Enter}"); // with the list closed, Enter submits as in any form
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("Tab completes the common prefix like zsh, then a lone folder gets its slash, and with nothing to add the focus moves on", async () => {
    answer = (u) => {
      const p = u.searchParams.get("prefix") ?? "";
      if (p === "/d/vgt_r") return list("/d", [e("/d", "vgt_run_a", true), e("/d", "vgt_run_b", true), e("/d", "vgt_runs.txt")]);
      if (p === "/d/vgt_run_") return list("/d", [e("/d", "vgt_run_a", true), e("/d", "vgt_run_b", true)]);
      if (p === "/d/vgt_run_a") return list("/d", [e("/d", "vgt_run_a", true)]);
      return list("/d", [e("/d", "x", true)]);
    };
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(box(), "/d/vgt_r");
    await screen.findAllByRole("option");
    await user.tab();
    expect(box().value).toBe("/d/vgt_run"); // common prefix of the three
    expect(box()).toHaveFocus();
    await user.type(box(), "_");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
    await user.tab();
    expect(box().value).toBe("/d/vgt_run_"); // already the common prefix: nothing to add, so Tab does not complete...
    expect(screen.getByRole("button", { name: /Browse/ })).toHaveFocus(); // ...and the focus moves on
    await user.type(box(), "a");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    await user.tab();
    expect(box().value).toBe("/d/vgt_run_a/"); // a lone folder is completed with its slash
    expect(box()).toHaveFocus();
  });

  it("Tab moves the focus as usual when there is nothing to complete", async () => {
    answer = () => list("/d", []);
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(box(), "/d/zzz");
    await waitFor(() => expect(calls.length).toBe(1));
    await user.tab();
    expect(screen.getByRole("button", { name: /Browse/ })).toHaveFocus();
  });

  it("ignores an answer that arrives after a newer request", async () => {
    let release: (r: Response) => void = () => undefined;
    answer = (u) => {
      const p = u.searchParams.get("prefix");
      if (p === "/slow") return new Promise<Response>((res) => { release = res; });
      return list("/", [e("", "fast", true)]);
    };
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(box(), "/slow");
    await waitFor(() => expect(calls.length).toBe(1));
    await user.type(box(), "x"); // "/slowx": a newer text; the first answer is now stale
    await screen.findByRole("option", { name: "fast/" });
    release(list("/", [e("", "stale-answer", true)]));
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByRole("option", { name: "stale-answer/" })).toBeNull();
    expect(screen.getByRole("option", { name: "fast/" })).toBeInTheDocument();
  });

  it("with a host, asks that host's route; with no host chosen yet, asks nothing and cannot browse", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<Harness hostId={7} />);
    await user.type(box(), "/home/x");
    await waitFor(() => expect(calls.at(-1)).toMatch(/^GET \/api\/hosts\/7\/complete\?prefix=%2Fhome%2Fx/));
    unmount();
    calls = [];
    render(<Harness hostId={null} />);
    await user.type(box(), "/home/x");
    await new Promise((r) => setTimeout(r, 200));
    expect(calls).toEqual([]);
    expect(screen.getByRole("button", { name: /Browse/ })).toBeDisabled();
  });

  it("Browse fills the field from the chooser, and says so when the folder belongs to a host the field does not look at", async () => {
    answer = (u) => {
      if (u.pathname === "/api/fs/choose") return json({ path: "/home/me/vgt", hostId: null, remotePath: null, gvfs: null });
      return list("/", []);
    };
    const user = userEvent.setup();
    const { unmount } = render(<Harness />);
    await user.click(screen.getByRole("button", { name: /Browse/ }));
    await waitFor(() => expect(box().value).toBe("/home/me/vgt"));
    expect(calls).toContain("POST /api/fs/choose");
    unmount();

    answer = () => json({ path: "/run/user/1/gvfs/sftp:host=h/x", hostId: null, remotePath: null, gvfs: { host: "h", user: "me" } });
    render(<Harness hostId={3} />);
    await user.click(screen.getByRole("button", { name: /Browse/ }));
    expect(await screen.findByRole("status")).toHaveTextContent("me@h, which is not added as a host yet");
    expect(screen.getByRole("button", { name: "Add this host…" })).toBeInTheDocument();
    expect(box().value).toBe("");
  });

  it("a folder inside a mount of an unregistered host offers it to the add-host form, and adds nothing itself", async () => {
    answer = () => json({ path: "/run/user/1/gvfs/sftp:host=login.hpc.example,user=me,port=2222/data", hostId: null, remotePath: null, gvfs: { host: "login.hpc.example", user: "me", port: "2222" } });
    const seen: unknown[] = [];
    const off = subscribeHostOffer(() => seen.push(getHostOffer()?.offer));
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: /Browse/ }));
    await user.click(await screen.findByRole("button", { name: "Add this host…" }));
    off();
    expect(seen).toEqual([{ host: "login.hpc.example", user: "me", port: "2222" }]);
    expect(calls.filter((c) => c.startsWith("POST") && c !== "POST /api/fs/choose")).toEqual([]); // nothing was created
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("a completed path inside a mount of an unregistered host offers it too", async () => {
    answer = () => json({ base: "/run/user/1/gvfs/sftp:host=h,user=me/data", entries: [], truncated: false, gvfs: { host: "h", user: "me" } });
    const user = userEvent.setup();
    render(<Harness />);
    await user.type(box(), "/run/user/1/gvfs/sftp:host=h,user=me/data/");
    expect(await screen.findByRole("button", { name: "Add this host…" })).toBeInTheDocument();
    await user.type(box(), "x"); // typing on clears the offer until the next answer
    expect(screen.queryByRole("button", { name: "Add this host…" })).toBeNull();
  });

  it("says when a path came through the document portal", async () => {
    answer = () => json({ path: "/run/user/1000/doc/ab12/cubes", hostId: null, remotePath: null, gvfs: null, documentPortal: true });
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: /Browse/ }));
    expect(await screen.findByRole("status")).toHaveTextContent("document portal");
    expect(box().value).toBe("/run/user/1000/doc/ab12/cubes");
  });

  it("a folder of another registered host is only taken after the person agrees to switch", async () => {
    answer = () => json({ path: "/run/user/1/gvfs/sftp:host=h/shared", hostId: 9, remotePath: "/shared", gvfs: null });
    const onHost = vi.fn();
    function H() {
      const [v, setV] = useState("");
      return <PathField label="Folder" value={v} onChange={setV} kind="dir" hostId={3} hosts={[{ id: 3, name: "Laptop" }, { id: 9, name: "Cluster" }]} onHost={onHost} />;
    }
    const user = userEvent.setup();
    render(<H />);
    await user.click(screen.getByRole("button", { name: /Browse/ }));
    expect(await screen.findByRole("status")).toHaveTextContent("That folder is on Cluster.");
    expect(box().value).toBe("");
    await user.click(screen.getByRole("button", { name: "Use Cluster" }));
    expect(onHost).toHaveBeenCalledWith(9);
    expect(box().value).toBe("/shared");
  });

  it("hides Browse for the rest of the session after the server says there is no chooser (last: it remembers)", async () => {
    answer = (u) => (u.pathname === "/api/fs/choose" ? json({ error: "no file chooser on this desktop" }, 501) : list("/", []));
    const user = userEvent.setup();
    const { unmount } = render(<Harness />);
    await user.click(screen.getByRole("button", { name: /Browse/ }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Browse/ })).toBeNull());
    unmount();
    render(<Harness />); // a new field does not offer it again
    expect(screen.queryByRole("button", { name: /Browse/ })).toBeNull();
    expect(calls.filter((c) => c.includes("/api/fs/choose"))).toHaveLength(1);
  });
});
