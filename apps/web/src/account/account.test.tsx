import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountGate } from "./AccountGate";

const json = (status: number, b: unknown) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
const none = { stage: "none", setupNeeded: false, account: null, recent: false, recoveryCodesLeft: 0, recentMinutes: 15 };
const ada = { id: "acct-1", name: "Ada", username: "ada", admin: true, createdAt: "", disabled: false, secondFactor: true };

afterEach(() => {
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

const workbench = (account: React.ReactNode) => <div data-testid="workbench">{account}</div>;

describe("the account gate", () => {
  it("renders the workbench unchanged in local mode, with no account control", async () => {
    const fetch = vi.fn(async () => json(200, { features: { planes: true } }));
    vi.stubGlobal("fetch", fetch);
    render(<AccountGate>{workbench}</AccountGate>);
    expect(await screen.findByTestId("workbench")).toBeEmptyDOMElement();
    expect(fetch).toHaveBeenCalledTimes(1); // only the capabilities call; nothing account-related is asked of a local server
  });

  it("shows the sign-in steps when the group server asks for a sign-in, then the code step after the password", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${url}`);
      if (url === "/api/capabilities") return json(401, { error: "sign in to use this", code: "signin_required" });
      if (url === "/api/auth/state") return json(200, none);
      if (url === "/api/auth/sign-in") return json(200, { ...none, stage: "second_factor", account: ada });
      return json(404, { error: "not found" });
    }));
    render(<AccountGate>{workbench}</AccountGate>);
    await userEvent.type(await screen.findByLabelText("Username"), "ada");
    await userEvent.type(screen.getByLabelText("Password"), "a long passphrase here");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText("Code from your authenticator app")).toBeInTheDocument();
    expect(screen.queryByTestId("workbench")).toBeNull();
    expect(calls).toContain("POST /api/auth/sign-in");
  });

  it("says a wrong password in the server's words", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      if (url === "/api/capabilities") return json(401, { error: "sign in to use this", code: "signin_required" });
      if (url === "/api/auth/state") return json(200, { ...none, setupNeeded: true });
      return json(401, { error: "wrong username or password" });
    }));
    render(<AccountGate>{workbench}</AccountGate>);
    expect(await screen.findByText(/admin bootstrap/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Username"), "ada");
    await userEvent.type(screen.getByLabelText("Password"), "nope nope nope");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("wrong username or password");
  });

  it("puts the account's name and a sign-out in the workbench on the group server", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      if (url === "/api/capabilities") return json(200, { features: { accounts: true } });
      if (url === "/api/auth/state") return json(200, { ...none, stage: "full", account: ada, recent: true });
      return json(404, { error: "not found" });
    }));
    render(<AccountGate>{workbench}</AccountGate>);
    await waitFor(() => expect(screen.getByRole("link", { name: "Account: Ada" })).toHaveAttribute("href", "#/account"));
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
  });

  it("opens the setup page from a bootstrap link, keeping the secret out of every request but the setup", async () => {
    history.replaceState(null, "", "/#/setup/secret-from-the-link");
    const fetch = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/auth/setup") return json(201, { ...none, stage: "enrol", account: ada });
      if (url === "/api/auth/enrolment") return json(200, { secret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", uri: "otpauth://totp/x?secret=GEZD" });
      void init;
      return json(401, { error: "sign in to use this", code: "signin_required" });
    });
    vi.stubGlobal("fetch", fetch);
    render(<AccountGate>{workbench}</AccountGate>);
    await userEvent.type(await screen.findByLabelText("Your name"), "Ada");
    await userEvent.type(screen.getByLabelText("Username"), "ada");
    await userEvent.type(screen.getByLabelText("Password"), "a long passphrase here");
    await userEvent.type(screen.getByLabelText("Password again"), "a long passphrase here");
    await userEvent.click(screen.getByRole("button", { name: "Create account" }));
    expect(await screen.findByLabelText("Setup key")).toHaveTextContent("GEZD GNBV");
    const setupCall = fetch.mock.calls.find(([u]) => u === "/api/auth/setup");
    expect(JSON.parse(String(setupCall?.[1]?.body))).toMatchObject({ token: "secret-from-the-link", username: "ada" });
    for (const [u] of fetch.mock.calls) expect(String(u)).not.toContain("secret-from-the-link");
    expect(location.hash).toBe(""); // the spent link left the address bar
    expect(fetch.mock.calls.filter(([u]) => u === "/api/auth/enrolment")).toHaveLength(1);
  });
});
