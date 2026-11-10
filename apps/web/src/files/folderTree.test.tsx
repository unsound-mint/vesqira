import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FolderTree } from "./FolderTree";
import { iconFor } from "./fileIcon";

type Reply = { status?: number; body: unknown };
function mockFetch(routes: Record<string, (init?: RequestInit) => Reply>) {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? "GET"} ${url}`);
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "nope" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200 });
  }));
  return calls;
}
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); delete (window as unknown as Record<string, unknown>).__TAURI_INTERNALS__; });

describe("icons follow VS Code's conventions", () => {
  it("picks folder and file icons by name and extension", () => {
    expect(iconFor("configs", "dir")).not.toBe(iconFor("misc", "dir"));
    expect(iconFor("configs", "dir", true)).not.toBe(iconFor("configs", "dir"));
    expect(iconFor("run.PY", "file")).toBe(iconFor("x.py", "file"));
    expect(iconFor("cube.fits", "file")).not.toBe(iconFor("notes.unknown", "file"));
  });
});

describe("folder tree", () => {
  it("opens a typed folder in a browser and lists it, folders first; only FITS files can be opened", async () => {
    const calls = mockFetch({
      "/api/projects/open": () => ({ body: { path: "/home/u/vgt", name: "vgt" } }),
      "/api/files/tree?path=%2Fhome%2Fu%2Fvgt": () => ({ body: { path: "/home/u/vgt", truncated: false, entries: [{ name: "configs", kind: "dir" }, { name: "cube.fits", kind: "file", size: 2048 }, { name: "notes.txt", kind: "file", size: 5 }] } }),
    });
    const registered: string[] = [];
    render(<FolderTree onOpenFits={async (p) => { registered.push(p); return null; }} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Open a folder"), "/home/u/vgt");
    await user.click(screen.getByRole("button", { name: "Open Folder" }));
    const tree = await screen.findByRole("tree", { name: "Files in /home/u/vgt" });
    await waitFor(() => expect(screen.getByRole("treeitem", { name: /cube\.fits/ })).toBeInTheDocument());
    const names = Array.from(tree.querySelectorAll('[role="treeitem"]')).map((n) => n.textContent);
    expect(names[0]).toBe("configs");
    expect(screen.getByRole("treeitem", { name: /notes\.txt/ })).toBeDisabled();
    await user.click(screen.getByRole("treeitem", { name: /cube\.fits/ }));
    expect(registered).toEqual(["/home/u/vgt/cube.fits"]);
    expect(calls[0]).toBe("POST /api/projects/open");
  });

  it("shows the server's refusal", async () => {
    mockFetch({ "/api/projects/open": () => ({ status: 400, body: { error: "open a project folder inside your home directory" } }) });
    render(<FolderTree onOpenFits={async () => null} />);
    await userEvent.type(screen.getByLabelText("Open a folder"), "/home/u");
    await userEvent.click(screen.getByRole("button", { name: "Open Folder" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("inside your home directory");
  });

  it("uses the native chooser in the desktop window", async () => {
    (window as unknown as Record<string, unknown>).__TAURI_INTERNALS__ = { invoke: async (cmd: string) => (cmd === "pick_folder" ? "/home/u/vgt" : null) };
    mockFetch({
      "/api/projects/open": () => ({ body: { path: "/home/u/vgt", name: "vgt" } }),
      "/api/files/tree": () => ({ body: { path: "/home/u/vgt", truncated: false, entries: [] } }),
    });
    render(<FolderTree onOpenFits={async () => null} />);
    expect(screen.queryByLabelText("Open a folder")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /Open Folder/ }));
    expect(await screen.findByRole("tree", { name: "Files in /home/u/vgt" })).toBeInTheDocument();
  });
});
