import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HostsPanel } from "../catalog/HostsPanel";
import type { Host } from "../api/hosts";

const host: Host = { id: 1, name: "Irgetas", dest: "irgetas", python: "python3", roots: ["/data"], createdAt: "" };

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

const candidates = [
  { path: "/shared/Anaconda3/bin/python3", python: "3.13.5", astropy: "7.0.1", numpy: "2.1.3", suitable: true },
  { path: "/home/u/vgt/.venv/bin/python", python: "3.9.18", astropy: "6.0", numpy: "1.26", suitable: false, problem: "Python 3.9.18 is older than 3.11, which the worker needs" },
];

describe("host Python command", () => {
  it("names the Python problem, finds a Python with astropy, and switches to the one you pick", async () => {
    const calls = mockFetch({
      "/api/hosts/1/check": () => ({ status: 422, body: { error: "The host's Python is 3.9.5; the worker needs 3.11 or newer (it starts with python -P).", code: "python_old", pythonVersion: "3.9.5" } }),
      "/api/hosts/1/python/find": () => ({ body: { candidates: candidates, minPython: "3.11" } }),
      "/api/hosts/1/python": () => ({ body: { python: "3.13.5", astropy: "7.0.1", numpy: "2.1.3", roots: ["/data"], enforces_roots: true } }),
    });
    render(<HostsPanel hosts={[host]} onChange={async () => {}} onRegistered={async () => {}} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Check connection" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("needs 3.11 or newer");
    expect(screen.getByRole("alert")).not.toHaveTextContent(/ssh works|VPN/); // the failure is not blamed on ssh
    expect(within(screen.getByRole("region", { name: "Host Irgetas" })).getByText(/Python 3.11 or newer with numpy and astropy/i)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Find a Python with astropy" }));
    const list = await screen.findByRole("list", { name: "Pythons found on Irgetas" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("/shared/Anaconda3/bin/python3");
    expect(rows[0]).toHaveTextContent("Python 3.13.5, astropy 7.0.1, numpy 2.1.3");
    expect(within(rows[1]!).queryByRole("button")).toBeNull(); // too old: listed with its reason, not selectable
    expect(rows[1]).toHaveTextContent("Not usable: Python 3.9.18 is older than 3.11");

    await user.click(within(rows[0]!).getByRole("button", { name: "Use this one" }));
    expect(await screen.findByText(/Connected. Python 3.13.5, astropy 7.0.1/)).toBeInTheDocument();
    const set = calls.find((c) => c.url === "/api/hosts/1/python")!;
    expect(set.init?.method).toBe("POST");
    expect(JSON.parse(String(set.init!.body))).toEqual({ python: "/shared/Anaconda3/bin/python3" });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("saves a typed Python command and says when nothing was found", async () => {
    const calls = mockFetch({
      "/api/hosts/1/python/find": () => ({ body: { candidates: [], minPython: "3.11" } }),
      "/api/hosts/1/python": () => ({ status: 422, body: { error: "Python 3.12.1 on the host cannot import astropy.", code: "python_import", missingModule: "astropy" } }),
    });
    render(<HostsPanel hosts={[host]} onChange={async () => {}} onRegistered={async () => {}} />);
    const user = userEvent.setup();
    const form = screen.getByRole("form", { name: "Python command for Irgetas" });
    const save = within(form).getByRole("button", { name: "Save and check" });
    expect(save).toBeDisabled(); // unchanged
    const input = within(form).getByLabelText("Python command");
    await user.clear(input);
    await user.type(input, "~/vgt/.venv/bin/python");
    await user.click(save);
    expect(await screen.findByRole("alert")).toHaveTextContent("cannot import astropy");
    expect(JSON.parse(String(calls.find((c) => c.url === "/api/hosts/1/python")!.init!.body))).toEqual({ python: "~/vgt/.venv/bin/python" });
    await user.click(screen.getByRole("button", { name: "Find a Python with astropy" }));
    expect(await screen.findByText(/No Python was found in the usual places/)).toBeInTheDocument();
  });

  it("sends the Python command of the add form only when one is written", async () => {
    const calls = mockFetch({
      "/api/hosts/ssh-config": () => ({ body: { found: false, hosts: [] } }),
      "/api/hosts/browse": () => ({ body: { path: "/home/u", entries: [] } }),
      "/api/hosts": () => ({ status: 201, body: host }),
    });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Host name"), "Irgetas");
    await user.type(screen.getByLabelText("SSH destination"), "irgetas");
    await user.type(screen.getByLabelText("Python command"), "/shared/Anaconda3/bin/python3");
    await user.click(screen.getByRole("button", { name: "Choose folder…" }));
    await user.click(await screen.findByRole("button", { name: "Use this folder" }));
    await user.click(screen.getByRole("button", { name: "Add host" }));
    await waitFor(() => expect(calls.some((c) => c.init?.method === "POST" && c.url === "/api/hosts")).toBe(true));
    expect(JSON.parse(String(calls.find((c) => c.url === "/api/hosts" && c.init?.method === "POST")!.init!.body))).toEqual({
      name: "Irgetas", dest: "irgetas", roots: ["/home/u"], python: "/shared/Anaconda3/bin/python3",
    });
  });
});
