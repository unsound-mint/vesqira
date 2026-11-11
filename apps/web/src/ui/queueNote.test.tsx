import { act, render, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueueNote, useQueueReason } from "./QueueNote";

const answer = (request: unknown) => new Response(JSON.stringify({ classes: [], ...(request ? { request } : {}) }), { status: 200, headers: { "Content-Type": "application/json" } });

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("useQueueReason", () => {
  it("polls only after 700 ms, shows the reason while waiting and clears it when the request ends", async () => {
    let state: "waiting" | "running" = "waiting";
    const fetchMock = vi.fn(async () => answer({ state, class: "heavy", ...(state === "waiting" ? { reason: "waiting: 1 heavy analysis is running" } : {}) }));
    vi.stubGlobal("fetch", fetchMock);
    const { result, rerender } = renderHook(({ active }) => useQueueReason(active, "abcdefgh1234"), { initialProps: { active: true } });
    await act(() => vi.advanceTimersByTimeAsync(699));
    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current).toBeNull();
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect((fetchMock.mock.calls[0] as unknown as [string])[0]).toBe("/api/workload?request=abcdefgh1234");
    expect(result.current).toBe("waiting: 1 heavy analysis is running");
    state = "running";
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(result.current).toBeNull();
    rerender({ active: false });
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("never polls for a request that finished within 700 ms, and stops on unmount", async () => {
    const fetchMock = vi.fn(async () => answer({ state: "waiting", reason: "waiting: x" }));
    vi.stubGlobal("fetch", fetchMock);
    const quick = renderHook(({ active }) => useQueueReason(active, "abcdefgh1234"), { initialProps: { active: true } });
    await act(() => vi.advanceTimersByTimeAsync(300));
    quick.rerender({ active: false });
    await act(() => vi.advanceTimersByTimeAsync(3000));
    expect(fetchMock).not.toHaveBeenCalled();
    const long = renderHook(() => useQueueReason(true, "abcdefgh1234"));
    await act(() => vi.advanceTimersByTimeAsync(700));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    long.unmount();
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("swallows poll errors and keeps polling", async () => {
    const fetchMock = vi.fn(async () => { throw new TypeError("down"); });
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useQueueReason(true, "abcdefgh1234"));
    await act(() => vi.advanceTimersByTimeAsync(2800));
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(result.current).toBeNull();
  });
});

describe("QueueNote", () => {
  it("is a status line with the reason, and nothing without one", () => {
    const { container, rerender, getByRole } = render(<QueueNote reason="waiting: busy" />);
    expect(getByRole("status")).toHaveTextContent("waiting: busy");
    rerender(<QueueNote reason={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});
