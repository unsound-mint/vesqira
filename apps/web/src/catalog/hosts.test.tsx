import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { offerHost } from "../ui";
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

describe("hosts panel and your ssh config", () => {
  it("offers the aliases from your ssh config in a dropdown, and fills the name from the chosen one", async () => {
    mockFetch({ "/api/hosts/ssh-config": () => ({ body: { found: true, hosts: [{ alias: "cluster", hostName: "login.example.org", user: "user" }] } }) });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    const select = await screen.findByRole("combobox", { name: "SSH destination" });
    expect(screen.getByRole("option", { name: "cluster — user@login.example.org" })).toBeInTheDocument();
    const user = userEvent.setup();
    await user.selectOptions(select, "cluster");
    expect(screen.getByLabelText("Host name")).toHaveValue("cluster");
    await user.clear(screen.getByLabelText("Host name"));
    await user.type(screen.getByLabelText("Host name"), "Cluster");
    await user.selectOptions(select, "cluster");
    expect(screen.getByLabelText("Host name")).toHaveValue("Cluster"); // a name you wrote is not overwritten
  });

  it("lets you type a destination that is not in the config", async () => {
    mockFetch({ "/api/hosts/ssh-config": () => ({ body: { found: true, hosts: [{ alias: "cluster" }] } }) });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByRole("combobox", { name: "SSH destination" }), "Another destination…");
    await user.type(screen.getByRole("textbox", { name: "SSH destination" }), "me@10.0.0.5");
    expect(screen.getByRole("textbox", { name: "SSH destination" })).toHaveValue("me@10.0.0.5");
  });

  it("works without any ssh config", async () => {
    mockFetch({ "/api/hosts/ssh-config": () => ({ body: { found: false, hosts: [] } }) });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    await waitFor(() => expect(screen.getByLabelText("SSH destination")).toBeInTheDocument());
    expect(screen.queryByRole("combobox", { name: "SSH destination" })).toBeNull();
  });
});

describe("hosts panel", () => {
  it("explains the connection model and never asks for a key or password", () => {
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    expect(screen.getByText(/Keys and passwords are never read by Vesqira/)).toBeInTheDocument();
    expect(screen.getByText(/bring the VPN up first/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/password|key/i)).toBeNull();
  });

  it("adds a host by choosing its source folders on the host, never typing a path (the Python command is optional)", async () => {
    const calls = mockFetch({
      "/api/hosts/browse?dest=laptop2&path=%2Fhome%2Fu%2Fcubes": () => ({ body: { path: "/home/u/cubes", entries: [] } }),
      "/api/hosts/browse": () => ({ body: { path: "/home/u", entries: [{ name: "cubes" }] } }),
      "/api/hosts": () => ({ status: 201, body: host }),
    });
    let changed = 0;
    render(<HostsPanel hosts={[]} onChange={async () => { changed++; }} onRegistered={async () => {}} />);
    const user = userEvent.setup();
    const add = screen.getByRole("button", { name: "Add host" });
    expect(add).toBeDisabled();
    expect(screen.getByLabelText("Python command")).toHaveValue(""); // collapsed and optional: the default python3 is sent when empty
    expect(screen.getByRole("button", { name: "Choose folder…" })).toBeDisabled(); // needs a destination first
    await user.type(screen.getByLabelText("Host name"), "Second laptop");
    await user.type(screen.getByLabelText("SSH destination"), "laptop2");
    await user.click(screen.getByRole("button", { name: "Choose folder…" }));
    await user.click(await screen.findByRole("button", { name: /cubes/ })); // starts at the host's home
    expect(calls.some((c) => c.url.includes("path=%2Fhome%2Fu%2Fcubes"))).toBe(true);
    await user.click(await screen.findByRole("button", { name: "Use this folder" }));
    expect(add).toBeEnabled();
    await user.click(add);
    await waitFor(() => expect(changed).toBe(1));
    expect(JSON.parse(String(calls.find((c) => c.init?.method === "POST")!.init!.body))).toEqual({ name: "Second laptop", dest: "laptop2", roots: ["/home/u/cubes"] });
  });

  it("fills the form from a file-manager mount offered by a path field, and adds nothing until the person confirms", async () => {
    const calls = mockFetch({
      "/api/hosts/ssh-config": () => ({ body: { found: true, hosts: [{ alias: "cluster" }] } }),
      "/api/hosts/browse": () => ({ body: { path: "/home/u", entries: [] } }),
    });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    await screen.findByRole("combobox", { name: "SSH destination" });
    act(() => offerHost({ host: "login.hpc.example", user: "me", port: "2222" }));
    expect(await screen.findByRole("textbox", { name: "SSH destination" })).toHaveValue("me@login.hpc.example");
    expect(screen.getByLabelText("Host name")).toHaveValue("login.hpc.example");
    expect(screen.getByRole("status")).toHaveTextContent(/port 2222/);
    expect(screen.getByRole("button", { name: "Add host" })).toBeDisabled(); // a source folder is still needed
    expect(calls.some((c) => c.init?.method === "POST")).toBe(false);
    expect(screen.queryByLabelText(/password|key/i)).toBeNull();
  });

  it("prefers the ssh config alias that stands for the mounted host", async () => {
    mockFetch({ "/api/hosts/ssh-config": () => ({ body: { found: true, hosts: [{ alias: "cluster", hostName: "Login.HPC.example", user: "me" }] } }) });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    await screen.findByRole("combobox", { name: "SSH destination" });
    act(() => offerHost({ host: "login.hpc.example", user: "me", port: "2222" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "SSH destination" })).toHaveValue("cluster"));
    expect(screen.getByLabelText("Host name")).toHaveValue("cluster");
  });

  it("does not put an address that cannot be an ssh destination in the form", async () => {
    mockFetch({ "/api/hosts/ssh-config": () => ({ body: { found: false, hosts: [] } }) });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    act(() => offerHost({ host: "fe80::1", user: "me" }));
    expect(await screen.findByRole("status")).toHaveTextContent(/cannot be written as an ssh destination/);
    expect(screen.getByLabelText("SSH destination")).toHaveValue("");
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
