import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { resetConvertibility, useConvertibility } from "./useConvertibility";

const conv = vi.hoisted(() => ({ probeRunConversions: vi.fn() }));
vi.mock("../api/runConvert", () => conv);

const RAD = { unit: "rad", to: "deg" };
const JY = { unit: "Jy/beam", to: "K" };

beforeEach(() => {
  resetConvertibility();
  conv.probeRunConversions.mockReset();
});

describe("useConvertibility", () => {
  it("settles a failed request as unknown even when the plot changed while it was in flight", async () => {
    let fail: (e: Error) => void = () => {};
    conv.probeRunConversions.mockReturnValue(new Promise((_, reject) => { fail = reject; }));
    const { result, rerender } = renderHook(({ offers }) => useConvertibility(offers), { initialProps: { offers: [RAD] } });
    expect(result.current(RAD).state).toBe("pending");
    // another plot that still offers the same pair: it is already being asked, so it is not asked twice
    rerender({ offers: [RAD, JY] });
    await act(async () => { fail(new Error("worker down")); });
    expect(result.current(RAD).state).toBe("unknown");
    expect(conv.probeRunConversions).toHaveBeenCalledTimes(2);
  });

  it("does not carry a failure into the next plot, which asks again", async () => {
    conv.probeRunConversions.mockRejectedValueOnce(new Error("worker down"));
    const { result, rerender } = renderHook(({ offers }) => useConvertibility(offers), { initialProps: { offers: [RAD] } });
    await act(async () => {});
    expect(result.current(RAD).state).toBe("unknown");
    conv.probeRunConversions.mockResolvedValueOnce([{ from: "rad", to: "deg", refused: false, factor: 57.3 }]);
    rerender({ offers: [JY, RAD] });
    expect(result.current(RAD).state).toBe("pending");
    await act(async () => {});
    expect(result.current(RAD).state).toBe("yes");
  });
});
