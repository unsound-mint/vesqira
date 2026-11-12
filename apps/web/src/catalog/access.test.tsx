import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { accessRefusal } from "../api/access";
import { ApiError } from "../api/http";
import { PermissionsContext } from "../account/permissions";
import { AccessBadge } from "../ui";
import { AccessPanel } from "./AccessPanel";

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
const at = "2026-11-12T10:00:00Z";
const acc = (label: string, canLower: boolean, extra: object = {}) => ({
  datasetId: 7, label, effective: { label }, history: [], blockedLabels: [], canLower, ...extra,
});

afterEach(() => vi.unstubAllGlobals());

describe("the access badge", () => {
  it("says the label in words, with an icon, and shows nothing for released data", () => {
    const { container, rerender } = render(<AccessBadge label="unreleased" />);
    expect(container).toHaveTextContent("Unreleased");
    expect(container.querySelector("svg")).not.toBeNull();
    rerender(<AccessBadge label="collaboration" />);
    expect(container).toHaveTextContent("Collaboration only");
    rerender(<AccessBadge label="none" />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("a publication refused for its labels", () => {
  it("is told apart by its code: 409 asks to confirm, 403 is a block, anything else is not an access refusal", () => {
    const asked = new ApiError("confirm", [], 409, { error: "confirm", code: "access_confirmation_required", label: "unreleased" });
    const blocked = new ApiError("blocked", [], 403, { error: "blocked", code: "publishing_blocked", label: "unreleased" });
    expect(accessRefusal(asked)?.status).toBe(409);
    expect(accessRefusal(blocked)?.status).toBe(403);
    expect(accessRefusal(new ApiError("stale", [], 409, { error: "stale" }))).toBeNull();
    expect(accessRefusal(new Error("x"))).toBeNull();
  });
});

describe("the access label of a dataset", () => {
  it("sets a label with a reason and shows what it inherits and the history", async () => {
    const calls: { url: string; body: unknown }[] = [];
    let state = acc("none", false, { effective: { label: "collaboration", via: [{ datasetId: 3, name: "parent.fits", label: "collaboration" }] } });
    vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        calls.push({ url: String(url), body: JSON.parse(String(init.body)) });
        state = acc("unreleased", false, { history: [{ id: 1, datasetId: 7, name: "cube.fits", label: "unreleased", previous: "none", reason: "not validated", author: "a", at }] });
      }
      return Promise.resolve(json(state));
    }));
    const user = userEvent.setup();
    render(<AccessPanel id={7} />);
    expect(await screen.findByText(/also carries/)).toHaveTextContent("Collaboration only from parent.fits");
    await user.selectOptions(screen.getByLabelText("Access label"), "unreleased");
    await user.type(screen.getByLabelText(/Why/), "not validated");
    await user.click(screen.getByRole("button", { name: "Set label" }));
    expect(calls).toEqual([{ url: "/api/datasets/7/access", body: { label: "unreleased", reason: "not validated" } }]);
    expect(await screen.findByText(/History \(1\)/)).toBeInTheDocument();
  });

  it("does not offer a less restrictive label to someone who may not lower it, and says why", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json(acc("unreleased", false)))));
    const user = userEvent.setup();
    render(<AccessPanel id={7} />);
    await user.selectOptions(await screen.findByLabelText("Access label"), "none");
    expect(screen.getByText(/needs the Maintainer role/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Set label" })).toBeDisabled();
  });

  it("says when the workspace blocks the chosen label, and hides the control from a Viewer", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json(acc("none", true, { blockedLabels: ["unreleased"] })))));
    const user = userEvent.setup();
    const { unmount } = render(<AccessPanel id={7} />);
    await user.selectOptions(await screen.findByLabelText("Access label"), "unreleased");
    expect(screen.getByText(/block publishing/)).toBeInTheDocument();
    unmount();
    render(<PermissionsContext.Provider value={(p) => p !== "dataset.write"}><AccessPanel id={7} /></PermissionsContext.Provider>);
    expect(await screen.findByText(/read this label but not change it/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Access label")).toBeNull();
  });
});
