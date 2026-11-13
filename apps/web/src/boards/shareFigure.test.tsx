import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Figure } from "../api/boards";
import ShareFigureDialog from "./ShareFigureDialog";

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
const figure = { id: 5, title: "Moment map" } as Figure;
const made = { snapshot: { id: 9 }, token: "tok", reviewPath: "/#/review/tok", note: "" };

type Call = { url: string; body: unknown };
function stub(handler: (url: string, n: number) => Response) {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : null });
    return Promise.resolve(handler(String(url), calls.length));
  }));
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("Share this view", () => {
  it("makes the link with the defaults, copies it, and can revoke it", async () => {
    const calls = stub((url) => json(url.endsWith("/revoke") ? { revoked: true } : made, url.endsWith("/revoke") ? 200 : 201));
    const getFigure = vi.fn(() => Promise.resolve(figure));
    const user = userEvent.setup();
    render(<ShareFigureDialog title="Moment map" getFigure={getFigure} onClose={() => undefined} />);
    await user.click(screen.getByRole("button", { name: "Create link" }));
    const box = await screen.findByLabelText("Review link");
    expect(box).toHaveValue("http://127.0.0.1:8788/#/review/tok");
    expect(await navigator.clipboard.readText()).toBe("http://127.0.0.1:8788/#/review/tok");
    expect(getFigure).toHaveBeenCalledTimes(1);
    expect(calls).toEqual([{ url: "/api/figures/5/share", body: { label: "", hours: 168 } }]);
    expect(screen.getByText(/copied to the clipboard/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Revoke link" }));
    expect(await screen.findByText(/revoked and no longer opens/)).toBeInTheDocument();
    expect(calls[1]).toEqual({ url: "/api/snapshots/9/revoke", body: {} });
  });

  it("asks once more for labelled data and shares the same figure, without saving it again", async () => {
    const calls = stub((_url, n) =>
      n === 1 ? json({ error: "This figure uses data labelled Unreleased: cube.fits.", code: "access_confirmation_required", label: "unreleased" }, 409) : json(made, 201));
    const getFigure = vi.fn(() => Promise.resolve(figure));
    const user = userEvent.setup();
    render(<ShareFigureDialog title="Moment map" getFigure={getFigure} onClose={() => undefined} />);
    await user.click(screen.getByRole("button", { name: "Create link" }));
    expect(await screen.findByRole("alert", { name: "Labelled data" })).toHaveTextContent("cube.fits");
    expect(screen.queryByRole("button", { name: "Create link" })).toBeNull();
    await user.click(screen.getByRole("button", { name: /I confirm/ }));
    expect(await screen.findByLabelText("Review link")).toBeInTheDocument();
    expect(getFigure).toHaveBeenCalledTimes(1);
    expect(calls.map((c) => c.body)).toEqual([{ label: "", hours: 168 }, { label: "", hours: 168, confirmAccess: true }]);
  });

  it("offers nothing to confirm when the workspace blocks the label", async () => {
    stub(() => json({ error: "Publishing is blocked in this workspace for data labelled Unreleased.", code: "publishing_blocked", label: "unreleased" }, 403));
    const user = userEvent.setup();
    render(<ShareFigureDialog title="Moment map" getFigure={() => Promise.resolve(figure)} onClose={() => undefined} />);
    await user.click(screen.getByRole("button", { name: "Create link" }));
    expect(await screen.findByRole("alert", { name: "Labelled data" })).toHaveTextContent("blocked");
    expect(screen.queryByRole("button", { name: /confirm/ })).toBeNull();
    expect(screen.queryByLabelText("Review link")).toBeNull();
  });

  it("says why the view could not be saved and makes no link", async () => {
    const calls = stub(() => json(made, 201));
    const user = userEvent.setup();
    render(<ShareFigureDialog title="Moment map" getFigure={() => Promise.reject(new Error("The image is not drawn yet."))} onClose={() => undefined} />);
    await user.click(screen.getByRole("button", { name: "Create link" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The image is not drawn yet.");
    expect(calls).toEqual([]);
  });
});
