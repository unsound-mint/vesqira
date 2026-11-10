import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ReaderPage } from "./ReaderPage";
import { listTitle } from "../reports/front";

const TOKEN = "t".repeat(43);
const iso = (minAgo: number) => new Date(Date.now() - minAgo * 60000).toISOString();

type Opts = { lastSync?: string; latest?: number; comments?: unknown[]; notFound?: "space" | "report" };
let posts: { url: string; body: Record<string, unknown> }[] = [];
let comments: Record<string, unknown>[] = [];
let deletes: number[] = [];

function mock(o: Opts = {}) {
  const latest = o.latest ?? 1;
  comments = (o.comments ?? []) as Record<string, unknown>[];
  posts = [];
  deletes = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    const path = new URL(url, "http://x").pathname;
    if (init?.method === "DELETE") {
      const id = Number(path.split("/").pop());
      deletes.push(id);
      comments = comments.filter((c) => c.id !== id);
      return new Response(null, { status: 204 });
    }
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      posts.push({ url: path, body });
      const c = { id: 100 + posts.length, parentId: 0, author: "Supervisor", fromOwner: false, createdAt: iso(0), ...body };
      comments.push(c);
      return json(c, 201);
    }
    if (o.notFound === "space") return json({}, 404);
    if (path === `/read-api/${TOKEN}`) {
      return json({ reader: "Supervisor", title: "LMC reports", comments: 0, lastSync: o.lastSync, reports: [{ key: "aaaa", latest, title: "Weekly results", publishedAt: "2026-11-09T10:00:00Z" }, { key: "bbbb", latest: 1, title: "Methods", publishedAt: "2026-11-02T10:00:00Z" }] });
    }
    if (o.notFound === "report") return json({}, 404);
    const m = /reports\/(\w+)$/.exec(path);
    if (m) {
      const rev = Number(new URL(url, "http://x").searchParams.get("rev")) || latest;
      return json({
        revision: {
          id: `rev-${m[1]}-${rev}`, report: m[1], number: rev, title: m[1] === "aaaa" ? "Weekly results" : "Methods", publishedAt: "2026-11-09T10:00:00Z", authors: "Denis",
          blocks: m[1] === "aaaa"
            ? [{ id: "b1", kind: "text", text: "Compare with [[report:bbbb|the methods]] and [[report:cccc|a private note]].", shared: ["bbbb"] }, rev === 1 ? { id: "b2", kind: "text", text: "Second paragraph." } : { id: "b3", kind: "text", text: "Second paragraph, revised." }]
            : [{ id: "m1", kind: "text", text: "How we did it." }],
        },
        revisions: Array.from({ length: latest }, (_, i) => ({ number: i + 1, publishedAt: iso(0) })),
        comments: comments.filter((c) => c.report === m[1]),
      });
    }
    return json({}, 404);
  }));
}

beforeEach(() => { location.hash = ""; localStorage.clear(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("the reader page", () => {
  it("lists the shared reports and shows the latest one with its meta", async () => {
    mock();
    render(<ReaderPage token={TOKEN} />);
    expect(await screen.findByRole("heading", { level: 1, name: "Weekly results" })).toBeTruthy();
    const nav = screen.getByRole("navigation", { name: "Reports" });
    expect(within(nav).getByText("Methods")).toBeTruthy();
    expect(screen.getByText(/Reading as Supervisor/)).toBeTruthy();
    expect(screen.getByText("Revision 1")).toBeTruthy();
    expect(screen.getByText(/Denis/)).toBeTruthy();
  });

  it("shows an unshared report link as text and navigates a shared one", async () => {
    mock();
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    expect(screen.getByText("a private note").closest("a")).toBeNull();
    fireEvent.click(screen.getByText("the methods"));
    await waitFor(() => expect(location.hash).toBe("#r=bbbb"));
    expect(await screen.findByRole("heading", { level: 1, name: "Methods" })).toBeTruthy();
  });

  it("answers a 404 for the space and for a report with the same neutral page", async () => {
    mock({ notFound: "space" });
    const a = render(<ReaderPage token={TOKEN} />);
    await screen.findByText("This link does not open anything.");
    const first = a.container.textContent;
    cleanup();
    mock({ notFound: "report" });
    const b = render(<ReaderPage token={TOKEN} />);
    await screen.findByText("This link does not open anything.");
    expect(b.container.textContent).toBe(first);
  });

  it("posts a comment without an author and shows it", async () => {
    mock();
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    fireEvent.click(screen.getAllByRole("button", { name: "Comment on this part" })[1]!);
    const panel = screen.getByRole("complementary", { name: "Comments" });
    fireEvent.change(within(panel).getByLabelText(/Comment on: Second paragraph/), { target: { value: "Is this the final fit?" } });
    fireEvent.click(within(panel).getByRole("button", { name: "Comment" }));
    await within(panel).findByText("Is this the final fit?");
    expect(posts[0]!.body).toEqual({ report: "aaaa", revision: "rev-aaaa-1", block: "b2", body: "Is this the final fit?" });
    expect("author" in posts[0]!.body).toBe(false);
  });

  it("sets parentId when replying to a thread", async () => {
    mock({ comments: [{ id: 7, report: "aaaa", revision: "rev-aaaa-1", block: "b1", parentId: 0, author: "Supervisor", fromOwner: false, body: "Why?", createdAt: iso(5) }] });
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    fireEvent.click(screen.getByRole("button", { name: "Comments 1" }));
    const panel = screen.getByRole("complementary", { name: "Comments" });
    fireEvent.change(within(panel).getByLabelText("Reply"), { target: { value: "Because." } });
    fireEvent.click(within(panel).getByRole("button", { name: "Reply" }));
    await waitFor(() => expect(posts.length).toBe(1));
    expect(posts[0]!.body).toMatchObject({ parentId: 7, block: "b1", body: "Because." });
  });

  it("notes that an older revision is being read", async () => {
    mock({ latest: 4 });
    location.hash = "#r=aaaa&rev=2";
    render(<ReaderPage token={TOKEN} />);
    expect(await screen.findByText(/You are reading revision 2 of 4/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Read the latest" }));
    await waitFor(() => expect(location.hash).toBe("#r=aaaa"));
    await waitFor(() => expect(screen.queryByText(/You are reading revision/)).toBeNull());
  });

  it("mentions the last sync only when it is older than 15 minutes", async () => {
    mock({ lastSync: iso(120) });
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    fireEvent.click(screen.getAllByRole("button", { name: "Comment on this part" })[0]!);
    expect(screen.getByText(/last synced 2 h ago; your comment will reach them at the next sync/)).toBeTruthy();
    cleanup();
    mock({ lastSync: iso(3) });
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    fireEvent.click(screen.getAllByRole("button", { name: "Comment on this part" })[0]!);
    expect(screen.queryByText(/last synced/)).toBeNull();
  });

  it("opens the comments panel from a block's gutter button and closes it with Escape", async () => {
    mock();
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    expect(screen.queryByRole("complementary", { name: "Comments" })).toBeNull();
    fireEvent.click(screen.getAllByRole("button", { name: "Comment on this part" })[0]!);
    const heading = await screen.findByRole("heading", { name: "Comments" });
    expect(document.activeElement).toBe(heading);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("complementary", { name: "Comments" })).toBeNull();
  });
});

// The reader runs in a stranger's browser with a capability to one space; it must not be able to reach owner clients.
const raw = import.meta.glob(["../**/*.ts", "../**/*.tsx", "!../**/*.test.*"], { query: "?raw", import: "default", eager: true }) as Record<string, string>;
// vite keys files in this folder as "./x"; give every file one spelling relative to this folder's parent
const files: Record<string, string> = Object.fromEntries(Object.entries(raw).map(([k, v]) => [k.startsWith("./") ? `../read/${k.slice(2)}` : k, v]));

function resolveImports(file: string): string[] {
  const out: string[] = [];
  for (const m of (files[file] ?? "").matchAll(/^import\s+(?!type\b)[^;]*?from\s+"(\.[^"]+)"/gms)) {
    const parts = file.split("/").slice(0, -1);
    for (const p of (m[1] ?? "").split("/")) {
      if (p === "..") parts.pop();
      else if (p !== ".") parts.push(p);
    }
    const base = parts.join("/");
    const hit = [`${base}.ts`, `${base}.tsx`, `${base}/index.ts`, `${base}/index.tsx`].find((c) => c in files);
    if (hit) out.push(hit);
  }
  return out;
}

describe("the reader page boundary", () => {
  it("does not reach any owner API client", () => {
    const seen = new Set<string>();
    const todo = ["../read/ReaderPage.tsx"];
    while (todo.length) {
      const f = todo.pop() as string;
      if (seen.has(f)) continue;
      seen.add(f);
      todo.push(...resolveImports(f));
    }
    expect(seen.size).toBeGreaterThan(3);
    // ../api/metrics.ts is a pure formatter the shared Document uses; every other client in api/ is an owner call
    expect([...seen].filter((f) => f.startsWith("../api/") && f !== "../api/metrics.ts")).toEqual([]);
  });

  it("marks what changed since the revision this browser last read, and can hide the marks", async () => {
    localStorage.setItem("vesqira.read.seen", JSON.stringify({ aaaa: 1, bbbb: 1 }));
    mock({ latest: 2 });
    const { container } = render(<ReaderPage token={TOKEN} />);
    expect(await screen.findByText(/Since revision 1: 1 part is new or changed, 1 removed/)).toBeTruthy();
    expect(container.querySelector('[data-block="b3"]')?.getAttribute("data-changed")).toBe("true");
    expect(container.querySelector('[data-block="b1"]')?.getAttribute("data-changed")).toBeNull();
    expect(JSON.parse(localStorage.getItem("vesqira.read.seen") ?? "{}")).toMatchObject({ aaaa: 2 });
    fireEvent.click(screen.getByRole("button", { name: "Hide marks" }));
    expect(container.querySelector('[data-changed="true"]')).toBeNull();
    expect(screen.getByRole("button", { name: "Show what changed since revision 1" })).toBeTruthy();
  });

  it("lets a reader delete their own unanswered comment, and nobody else's", async () => {
    mock({ comments: [
      { id: 7, report: "aaaa", revision: "rev-aaaa-1", block: "b1", parentId: 0, author: "Supervisor", fromOwner: false, body: "Mine", createdAt: iso(5), mine: true },
      { id: 8, report: "aaaa", revision: "rev-aaaa-1", block: "b2", parentId: 0, author: "Co-supervisor", fromOwner: false, body: "Theirs", createdAt: iso(5) },
    ] });
    render(<ReaderPage token={TOKEN} />);
    await screen.findByRole("heading", { level: 1, name: "Weekly results" });
    fireEvent.click(screen.getByRole("button", { name: "Comments 2" }));
    const panel = screen.getByRole("complementary", { name: "Comments" });
    const del = within(panel).getAllByRole("button", { name: "Delete" });
    expect(del.length).toBe(1);
    fireEvent.click(del[0]!);
    fireEvent.click(within(panel).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(deletes).toEqual([7]));
    await waitFor(() => expect(within(panel).queryByText("Mine")).toBeNull());
  });

  it("drops the date prefix a dated list already shows", () => {
    expect(listTitle("Report 10.11.2026 — What our cold-gas result means")).toBe("What our cold-gas result means");
    expect(listTitle("Methods")).toBe("Methods");
    expect(listTitle("Report 10.11.2026")).toBe("Report 10.11.2026");
  });
});
