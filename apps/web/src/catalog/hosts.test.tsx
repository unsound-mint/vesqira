import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HostsPanel } from "./HostsPanel";
import type { Host } from "../api/hosts";

const host: Host = { id: 1, name: "Second laptop", dest: "laptop2", python: "python3", roots: ["/data"], createdAt: "" };

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("hosts panel", () => {
  it("explains the connection model and never asks for a key or password", () => {
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    expect(screen.getByText(/Keys and passwords are never read by Vesqira/)).toBeInTheDocument();
    expect(screen.getByText(/bring the VPN up first/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/password|key/i)).toBeNull();
  });

  it("adds a host with its source folders, one per line", async () => {
    const calls = mockFetch({ "/api/hosts": () => ({ status: 201, body: host }) });
    let changed = 0;
    render(<HostsPanel hosts={[]} onChange={async () => { changed++; }} onRegistered={async () => {}} />);
    const add = screen.getByRole("button", { name: "Add host" });
    expect(add).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Host name"), "Second laptop");
    await userEvent.type(screen.getByLabelText("SSH destination"), "laptop2");
    await userEvent.type(screen.getByLabelText(/Source folders/), "/data{Enter}/more");
    await userEvent.click(add);
    await waitFor(() => expect(changed).toBe(1));
    expect(JSON.parse(String(calls[0]!.init!.body))).toEqual({ name: "Second laptop", dest: "laptop2", python: "python3", roots: ["/data", "/more"] });
  });

  it("checks the connection, browses, and registers a FITS file that stays on the host", async () => {
    const calls = mockFetch({
      "/api/hosts/1/check": () => ({ body: { python: "3.12.1", astropy: "7.0", numpy: "2.0", roots: ["/data"], enforces_roots: true } }),
      "/api/hosts/1/list": () => ({ body: { path: "/data", entries: [{ name: "sub", kind: "dir" }, { name: "cube.fits", kind: "file", size: 2048 }, { name: "notes.txt", kind: "file", size: 5 }] } }),
      "/api/hosts/1/register": () => ({ status: 201, body: {} }),
    });
    let registered = 0;
    render(<HostsPanel hosts={[host]} onChange={async () => {}} onRegistered={async () => { registered++; }} />);
    await userEvent.click(screen.getByRole("button", { name: "Check connection" }));
    expect(await screen.findByText(/Python 3.12.1, astropy 7.0. The host enforces its source folders itself/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Browse" }));
    expect(await screen.findByText("notes.txt")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /notes.txt/ })).toBeNull(); // only FITS files can be registered
    await userEvent.click(screen.getByRole("button", { name: /cube.fits/ }));
    await waitFor(() => expect(registered).toBe(1));
    expect(JSON.parse(String(calls.find((c) => c.url.endsWith("/register"))!.init!.body))).toEqual({ path: "/data/cube.fits" });
  });

  it("says plainly when the host is not reachable", async () => {
    mockFetch({ "/api/hosts/1/check": () => ({ status: 502, body: { error: "could not run the worker on the host: check that ssh works" } }) });
    render(<HostsPanel hosts={[host]} onChange={async () => {}} onRegistered={async () => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Check connection" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("check that ssh works");
  });
});
