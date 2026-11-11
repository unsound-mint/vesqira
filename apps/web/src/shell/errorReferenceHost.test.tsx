import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { request } from "../api/http";
import { ErrorReferenceHost } from "./ErrorReferenceHost";

afterEach(() => vi.unstubAllGlobals());

const fail = (status: number, body: string, rid: string | null) => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(body, { status, headers: { "Content-Type": "text/plain", ...(rid ? { "X-Request-Id": rid } : {}) } })));
  return act(() => request("/api/x").then(() => null, () => null));
};

describe("ErrorReferenceHost", () => {
  it("shows the last error with its reference as a quiet status, a newer one replaces it, and it can be dismissed", async () => {
    render(<ErrorReferenceHost />);
    expect(screen.queryByRole("status")).toBeNull();
    await fail(500, "boom", "aaaaaaaa1111");
    const line = screen.getByRole("status");
    expect(line).toHaveAttribute("aria-live", "polite");
    expect(line).toHaveTextContent("Last error: boom");
    expect(line).toHaveTextContent("Reference: aaaaaaaa1111");
    await fail(502, "bad gateway", "bbbbbbbb2222");
    expect(screen.getByRole("status")).toHaveTextContent("Reference: bbbbbbbb2222");
    expect(screen.queryByText(/aaaaaaaa1111/)).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("ignores errors without a reference and aborted requests", async () => {
    render(<ErrorReferenceHost />);
    await fail(500, "no id", null);
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new DOMException("aborted", "AbortError"))));
    await act(() => request("/api/x").then(() => null, () => null));
    expect(screen.queryByRole("status")).toBeNull();
  });
});
