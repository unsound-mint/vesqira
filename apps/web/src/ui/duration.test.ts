import { formatDuration, parseTime, slurmLimitGloss } from "./duration";

describe("formatDuration", () => {
  it("is seconds under two minutes, then minutes, hours and days", () => {
    expect(formatDuration(0)).toBe("0 s");
    expect(formatDuration(45)).toBe("45 s");
    expect(formatDuration(119)).toBe("119 s");
    expect(formatDuration(120)).toBe("2 min");
    expect(formatDuration(720)).toBe("12 min");
    expect(formatDuration(3600)).toBe("1 h");
    expect(formatDuration(6360)).toBe("1 h 46 min");
    expect(formatDuration(6 * 3600 + 12 * 60)).toBe("6 h 12 min");
    expect(formatDuration(86400)).toBe("1 day");
    expect(formatDuration(86400 + 7200)).toBe("1 day 2 h");
    expect(formatDuration(3 * 86400)).toBe("3 days");
  });

  it("does not round up into an impossible unit", () => {
    expect(formatDuration(3 * 3600 + 59 * 60 + 40)).toBe("4 h");
    expect(formatDuration(86400 + 23 * 3600 + 40 * 60)).toBe("2 days");
  });

  it("never prints a negative span", () => {
    expect(formatDuration(-5)).toBe("0 s");
  });
});

describe("Slurm time limits", () => {
  it("reads the forms sbatch accepts and refuses the rest", () => {
    expect(parseTime("1-00:00:00")).toBe(86400);
    expect(parseTime("02:30:00")).toBe(9000);
    expect(parseTime("90")).toBe(5400);
    expect(parseTime("2-12")).toBe(2 * 86400 + 12 * 3600);
    expect(parseTime("soon")).toBeNull();
  });

  it("glosses a limit in words, and says nothing for an empty or unreadable one", () => {
    expect(slurmLimitGloss("1-00:00:00")).toBe("1 day");
    expect(slurmLimitGloss("12:00:00")).toBe("12 h");
    expect(slurmLimitGloss("")).toBe("");
    expect(slurmLimitGloss("soon")).toBe("");
  });
});
