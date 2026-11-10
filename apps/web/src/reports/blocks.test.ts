import { describe, expect, it } from "vitest";
import fixture from "../../../../packages/contracts/fixtures/report-blocks.json";
import { blockSource, joinBlocks, splitBlocks } from "./blocks";
import { reportDate, reportSummary, setFrontValue, withFront } from "./front";

// The server splits bodies the same way (services/api/internal/reports/blocks_fixture_test.go reads this file), so a
// reader's comment on a block points at the block the author edits here.
describe("report blocks", () => {
  for (const c of fixture as { body: string; blocks: object[]; date?: string; summary?: string }[]) {
    it(`split like the server: ${JSON.stringify(c.body.slice(0, 40))}`, () => {
      expect(splitBlocks(c.body)).toEqual(c.blocks);
      if (c.date !== undefined) expect(reportDate(c.body)).toBe(c.date);
      if (c.summary !== undefined) expect(reportSummary(c.body)).toBe(c.summary);
    });
  }
  it("editing blocks keeps the front matter, and setting a key keeps the others", () => {
    const body = "---\ndate: 2026-11-10\nstatus: Draft\n---\n\nfirst\n\nsecond";
    const parts = splitBlocks(body).map(blockSource);
    parts[0] = "first, edited";
    expect(withFront(body, joinBlocks(parts))).toBe("---\ndate: 2026-11-10\nstatus: Draft\n---\n\nfirst, edited\n\nsecond");
    const s = setFrontValue(body, "summary", "  One   line. ");
    expect(reportSummary(s)).toBe("One line.");
    expect(s).toContain("status: Draft");
    expect(setFrontValue(setFrontValue("text", "date", "2026-01-02"), "date", "")).toBe("text");
  });
  it("editing one block and joining keeps the others", () => {
    const blocks = splitBlocks("# A\n\nfirst\n\n[[run:3]]\n\nlast").map(blockSource);
    blocks[1] = "first, edited";
    expect(joinBlocks(blocks)).toBe("# A\n\nfirst, edited\n\n[[run:3]]\n\nlast");
    blocks[1] = "";
    expect(splitBlocks(joinBlocks(blocks)).length).toBe(3);
  });
});
