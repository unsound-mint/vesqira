import { limitNumber, limitText } from "./format";

describe("display limit precision", () => {
  it("writes one number the same way in the limit box and under the colour bar", () => {
    const v = -0.41104213;
    expect(String(limitNumber(v))).toBe("-0.411042");
    expect(limitText(v, "K")).toBe("-0.411042 K");
    expect(limitText(9.612537, "K")).toBe("9.61254 K");
  });
  it("leaves the unit out when the file has none, and never draws a missing value as a number", () => {
    expect(limitText(2, null)).toBe("2");
    expect(limitText(NaN, "K")).toBe("no value");
  });
});
