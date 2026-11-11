import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ErrorReference } from "./ErrorReference";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ErrorReference", () => {
  it("shows the reference and copies it, saying Copied for two seconds", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    render(<ErrorReference id="req-12345678" />);
    expect(screen.getByText("req-12345678")).toBeInTheDocument();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await user.click(screen.getByRole("button", { name: "Copy reference req-12345678" }));
    expect(writeText).toHaveBeenCalledWith("req-12345678");
    expect(screen.getByRole("button", { name: "Copy reference req-12345678" })).toHaveTextContent("Copied");
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(screen.getByRole("button", { name: "Copy reference req-12345678" })).not.toHaveTextContent("Copied");
  });

  it("falls back to select and execCommand when the async clipboard refuses", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"));
    const exec = vi.fn(() => true);
    (document as unknown as { execCommand: unknown }).execCommand = exec;
    render(<ErrorReference id="req-abcdefgh" />);
    await user.click(screen.getByRole("button", { name: /Copy reference/ }));
    expect(exec).toHaveBeenCalledWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  });
});
