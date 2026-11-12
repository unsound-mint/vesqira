import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { matchText } from "../api/push";

const json = (status: number, b: unknown) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe("push to the group server", () => {
  it("shows no push button on a computer that is not linked", async () => {
    const fetch = vi.fn(async () => json(200, { linked: false }));
    vi.stubGlobal("fetch", fetch);
    const { PushButton } = await import("./PushButton");
    const { container } = render(<PushButton kind="board" id={3} />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the preflight and queues only on confirmation", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${url}`);
      if (url === "/api/group-link") return json(200, { linked: true, origin: "https://lab.example.org", workspace: "Lab" });
      if (url === "/api/outbox/preflight") return json(200, { kind: "figure", title: "Peak", workspace: "Lab", origin: "https://lab.example.org", pictures: 1, bytes: 2048, warnings: ["1 run setting(s) ... left out"], references: [{ role: "source", name: "cube.fits", sizeBytes: 9, fingerprint: "sampled" }] });
      if (url === "/api/outbox") return json(202, { id: 1, kind: "figure", localId: 3, title: "Peak", hash: "h", state: "waiting", message: "queued", attempts: 0, createdAt: "", status: "waiting: queued" });
      return json(404, { error: "not found" });
    }));
    const confirm = vi.fn(() => false);
    vi.stubGlobal("confirm", confirm);
    const { PushButton } = await import("./PushButton");
    render(<PushButton kind="figure" id={3} />);
    const button = await screen.findByRole("button", { name: "Push to Lab" });
    await userEvent.click(button);
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    const shown = (confirm.mock.calls[0] as unknown as string[])[0];
    expect(shown).toContain("cube.fits");
    expect(shown).toContain("probably on this server");
    expect(calls).not.toContain("POST /api/outbox");
    confirm.mockReturnValue(true);
    await userEvent.click(button);
    expect(await screen.findByRole("status")).toHaveTextContent("queued");
    expect(calls).toContain("POST /api/outbox");
  });

  it("says what a reference established and never links by name", () => {
    expect(matchText({ match: "verified", name: "a.fits", datasetName: "server.fits" })).toBe("on this server: server.fits (verified same bytes)");
    expect(matchText({ match: "probable", name: "a.fits", datasetName: "server.fits" })).toBe("probably on this server as server.fits (sampled; not verified)");
    expect(matchText({ match: "none", name: "same-name.fits" })).toBe("not on this server");
  });
});
