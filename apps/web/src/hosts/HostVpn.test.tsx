import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HostsPanel } from "../catalog/HostsPanel";
import { parseArgv } from "./HostVpn";
import type { Host } from "../api/hosts";

const base: Host = { id: 1, name: "Cluster", dest: "cluster", python: "python3", roots: ["/data"], createdAt: "" };
const withVpn: Host = { ...base, vpnCommand: ["gpclient", "connect", "vpn.example.edu"], vpnDisconnectCommand: ["gpclient", "disconnect"] };

type Route = (init?: RequestInit) => { status?: number; body: unknown };
function mockFetch(routes: Record<string, Route>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
const caps = (vpn: boolean): Route => () => ({ body: { features: { vpn } } });
const noAliases: Route = () => ({ body: { found: false, hosts: [] } });
afterEach(() => vi.unstubAllGlobals());

const panel = (h: Host, onChange: () => Promise<void> = async () => {}) => render(<HostsPanel hosts={[h]} onChange={onChange} onRegistered={async () => {}} />);

describe("the VPN companion on a host", () => {
  it("shows nothing about a VPN when the server was started without --enable-vpn", async () => {
    mockFetch({ "/api/capabilities": caps(false), "/api/hosts/ssh-config": noAliases });
    panel({ ...withVpn, reachable: "no" });
    await screen.findByLabelText("Host Cluster");
    await waitFor(() => expect(screen.queryByText(/VPN: /)).toBeNull());
    expect(screen.queryByRole("button", { name: /Connect VPN/ })).toBeNull();
    expect(screen.queryByLabelText(/VPN connect command/)).toBeNull();
  });

  it("offers Connect on an unreachable host, runs it with only the action, shows the output and the reachability result", async () => {
    const onChange = vi.fn(async () => {});
    const calls = mockFetch({
      "/api/capabilities": caps(true), "/api/hosts/ssh-config": noAliases,
      "/api/hosts/1/vpn": () => ({ body: { action: "connect", exitCode: 0, timedOut: false, durationMs: 1200, output: "Connected\n", outputTruncated: false, reachable: true, message: "VPN command finished; the host is reachable" } }),
    });
    panel({ ...withVpn, reachable: "no" }, onChange);
    const group = await screen.findByRole("group", { name: "VPN for Cluster" });
    expect(await within(group).findByText("VPN: not reachable")).toBeInTheDocument();
    // the stored command is shown verbatim, with the statement of who runs it
    expect(within(group).getByText('["gpclient","connect","vpn.example.edu"]')).toBeInTheDocument();
    expect(within(group).getByText(/as you, without a shell/)).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(within(group).getByRole("button", { name: "Connect VPN" }));
    expect(await within(group).findByText("VPN command finished; the host is reachable")).toBeInTheDocument();
    expect(within(group).getByLabelText("VPN command output")).toHaveTextContent("Connected");
    const post = calls.find((c) => c.url === "/api/hosts/1/vpn");
    expect(JSON.parse(String(post?.init?.body))).toEqual({ action: "connect" }); // nothing but the action leaves the page
    expect(post?.init?.method).toBe("POST");
    expect(onChange).toHaveBeenCalled(); // the list is reloaded so the state follows what the server learned
  });

  it("reports a command that left the host unreachable, and offers Disconnect when one is stored", async () => {
    mockFetch({
      "/api/capabilities": caps(true), "/api/hosts/ssh-config": noAliases,
      "/api/hosts/1/vpn": () => ({ body: { action: "connect", exitCode: 3, timedOut: false, durationMs: 90, output: "auth failed\n", outputTruncated: false, reachable: false, message: "VPN command failed (exit 3); the host is still not reachable" } }),
    });
    panel({ ...withVpn, reachable: "no" });
    const group = await screen.findByRole("group", { name: "VPN for Cluster" });
    expect(await within(group).findByRole("button", { name: "Disconnect VPN" })).toBeInTheDocument();
    await userEvent.setup().click(within(group).getByRole("button", { name: "Connect VPN" }));
    expect(await within(group).findByText(/still not reachable/)).toBeInTheDocument();
    expect(within(group).getByLabelText("VPN command output")).toHaveTextContent("auth failed");
  });

  it("does not push Connect on a reachable host, and has no Disconnect without a stored one", async () => {
    mockFetch({ "/api/capabilities": caps(true), "/api/hosts/ssh-config": noAliases });
    panel({ ...base, vpnCommand: ["gpclient", "connect", "vpn.example.edu"], reachable: "yes" });
    const group = await screen.findByRole("group", { name: "VPN for Cluster" });
    expect(await within(group).findByText("VPN: reachable")).toBeInTheDocument();
    expect(within(group).queryByRole("button", { name: "Connect VPN" })).toBeNull(); // only the quiet "again" form
    expect(within(group).getByRole("button", { name: "Connect VPN again" })).toBeInTheDocument();
    expect(within(group).queryByRole("button", { name: "Disconnect VPN" })).toBeNull();
  });

  it("a host without a command only offers to set one", async () => {
    mockFetch({ "/api/capabilities": caps(true), "/api/hosts/ssh-config": noAliases });
    panel({ ...base, reachable: "no" });
    const group = await screen.findByRole("group", { name: "VPN for Cluster" });
    expect(within(group).queryByText(/VPN: /)).toBeNull();
    expect(within(group).queryByRole("button", { name: /Connect VPN/ })).toBeNull();
    expect(within(group).getByRole("button", { name: "Set a VPN command…" })).toBeInTheDocument();
  });

  it("saves the typed commands as JSON lists of strings and refuses text that is not one before any request", async () => {
    const calls = mockFetch({
      "/api/capabilities": caps(true), "/api/hosts/ssh-config": noAliases,
      "/api/hosts/1/vpn-config": () => ({ body: { ...base } }),
    });
    panel(base);
    const group = await screen.findByRole("group", { name: "VPN for Cluster" });
    const user = userEvent.setup();
    await user.click(within(group).getByRole("button", { name: "Set a VPN command…" }));
    const field = within(group).getByLabelText("VPN connect command (optional)");
    expect(within(group).getByText(/gpclient/, { selector: "span" })).toBeInTheDocument(); // the example in the help text
    await user.click(field);
    await user.paste("gpclient connect vpn.example.edu");
    await user.click(within(group).getByRole("button", { name: "Save VPN command" }));
    expect(await within(group).findByRole("alert")).toHaveTextContent(/JSON list of strings/);
    expect(calls.some((c) => c.url.endsWith("/vpn-config"))).toBe(false);
    await user.clear(field);
    await user.click(field);
    await user.paste('["gpclient","connect","vpn.example.edu;x"]');
    await user.click(within(group).getByRole("button", { name: "Save VPN command" }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/vpn-config"))).toBe(true));
    const post = calls.find((c) => c.url.endsWith("/vpn-config"));
    expect(JSON.parse(String(post?.init?.body))).toEqual({ vpnCommand: ["gpclient", "connect", "vpn.example.edu;x"] });
  });

  it("the add-host form carries the commands only when the feature is on", async () => {
    mockFetch({ "/api/capabilities": caps(false), "/api/hosts/ssh-config": noAliases });
    const { unmount } = render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    await waitFor(() => expect(screen.getByLabelText("SSH destination")).toBeInTheDocument());
    expect(screen.queryByLabelText(/VPN connect command/)).toBeNull();
    unmount();
    vi.unstubAllGlobals();
    mockFetch({ "/api/capabilities": caps(true), "/api/hosts/ssh-config": noAliases });
    render(<HostsPanel hosts={[]} onChange={async () => {}} onRegistered={async () => {}} />);
    expect(await screen.findByLabelText("VPN connect command (optional)")).toBeInTheDocument();
    expect(screen.getByLabelText("VPN disconnect command (optional)")).toBeInTheDocument();
    expect(screen.getByText(/never asks for or sees it/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/password/i)).toBeNull();
  });
});

describe("parseArgv", () => {
  it("accepts a JSON list of strings untouched and nothing else", () => {
    expect(parseArgv("", "x")).toBeUndefined();
    expect(parseArgv('  ["a b", "c;d", " e "]  ', "x")).toEqual(["a b", "c;d", " e "]); // no trimming or splitting of elements
    for (const bad of ["gpclient connect", "[]", "[1]", '["a", null]', '{"a":1}', '"a"', "[", '["a"] extra']) {
      expect(() => parseArgv(bad, "x"), bad).toThrow(/JSON list of strings/);
    }
  });
});
