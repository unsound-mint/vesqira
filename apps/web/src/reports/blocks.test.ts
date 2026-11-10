import { describe, expect, it } from "vitest";
import fixture from "../../../../packages/contracts/fixtures/report-blocks.json";
import { blockSource, joinBlocks, splitBlocks } from "./blocks";

// The server splits bodies the same way (services/api/internal/reports/blocks_fixture_test.go reads this file), so a
// reader's comment on a block points at the block the author edits here.
describe("report blocks", () => {
  for (const c of fixture as { body: string; blocks: object[] }[]) {
    it(`split like the server: ${JSON.stringify(c.body.slice(0, 40))}`, () => {
      expect(splitBlocks(c.body)).toEqual(c.blocks);
    });
  }
  it("editing one block and joining keeps the others", () => {
    const blocks = splitBlocks("# A\n\nfirst\n\n[[run:3]]\n\nlast").map(blockSource);
    blocks[1] = "first, edited";
    expect(joinBlocks(blocks)).toBe("# A\n\nfirst, edited\n\n[[run:3]]\n\nlast");
    blocks[1] = "";
    expect(splitBlocks(joinBlocks(blocks)).length).toBe(3);
  });
});
