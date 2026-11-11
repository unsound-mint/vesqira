import { pct, quotaTone } from "./quotaTone";

describe("quota meter colour", () => {
  it("turns to warning at 90 % of the limit, not before", () => {
    expect(quotaTone(89, 100)).toBe("ok");
    expect(quotaTone(90, 100)).toBe("warn");
    expect(quotaTone(150, 100)).toBe("warn");
  });
  it("has no tone to give without a limit", () => {
    expect(quotaTone(5, 0)).toBe("ok");
    expect(pct(5, 0)).toBe(0);
  });
});
