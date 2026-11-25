import { formatDay, formatWhen } from "./dates";

describe("dates", () => {
  it("a day is the UTC date of the timestamp", () => {
    expect(formatDay("2026-11-10T23:30:00Z")).toBe("2026-11-10");
  });

  it("a moment carries both the date and the time", () => {
    expect(formatWhen("2026-11-10T10:15:00Z")).toMatch(/2026/);
    expect(formatWhen("2026-11-10T10:15:00Z")).toMatch(/\d:\d\d/);
  });
});
