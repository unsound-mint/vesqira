import { describe, expect, it } from "vitest";
import type { ReportSummary } from "../api/reports";
import { ancestors, firstPage, subtree, visibleRows } from "./tree";

const r = (id: number, title: string, parent = 0, date?: string): ReportSummary => ({ id, key: String(id), title, parent, date, revision: 1, updatedAt: "", lastAuthor: "owner", agentUnseen: false, published: 0, publishedFrom: 0 });
const list = [r(1, "Welcome"), r(2, "Meetings", 1), r(3, "Report 30.10", 2, "2026-10-30"), r(4, "Report 10.11", 2, "2026-11-10"), r(5, "Task 10", 1), r(6, "Task 9", 1), r(7, "Orphan", 99)];

describe("report tree", () => {
  it("shows open pages' children, newest dated first, then titles in number order", () => {
    expect(visibleRows(list, new Set([1, 2])).map((x) => `${x.depth}:${x.r.title}`)).toEqual(["0:Orphan", "0:Welcome", "1:Meetings", "2:Report 10.11", "2:Report 30.10", "1:Task 9", "1:Task 10"]);
    expect(visibleRows(list, new Set()).map((x) => x.r.title)).toEqual(["Orphan", "Welcome"]);
  });
  it("finds ancestors, a subtree and the first page", () => {
    expect(ancestors(list, 4)).toEqual([2, 1]);
    expect([...subtree(list, 1)].sort()).toEqual([1, 2, 3, 4, 5, 6]);
    expect(firstPage(list)).toBe(4);
  });
  it("survives a loop in bad data", () => {
    const loop = [r(1, "a", 2), r(2, "b", 1)];
    expect(ancestors(loop, 1)).toEqual([2]);
    expect(visibleRows(loop, new Set([1, 2])).length).toBe(2);
  });
});
