import { describe, expect, it } from "vitest";

// Every source file as text, keyed like "./review/ReviewPage.tsx" (vite resolves this at build time, so no node types are needed).
const files = import.meta.glob(["./**/*.ts", "./**/*.tsx", "!./**/*.test.*"], { query: "?raw", import: "default", eager: true }) as Record<string, string>;

function join(fromFile: string, rel: string): string {
  const parts = fromFile.split("/").slice(0, -1);
  for (const p of rel.split("/")) {
    if (p === "." || p === "") continue;
    if (p === "..") parts.pop();
    else parts.push(p);
  }
  return parts.join("/");
}

/** The relative modules a file imports at run time (`import type` is erased by the build and does not count). */
function imports(file: string): string[] {
  const text = files[file] ?? "";
  const out: string[] = [];
  for (const m of text.matchAll(/^import\s+(?!type\b)[^;]*?from\s+"(\.[^"]+)"/gms)) {
    const base = join(file, m[1] ?? "");
    const hit = [`${base}.ts`, `${base}.tsx`, `${base}/index.ts`, `${base}/index.tsx`].find((c) => c in files);
    if (hit) out.push(hit);
  }
  return out;
}

function closure(entry: string): Set<string> {
  const seen = new Set<string>();
  const todo = [entry];
  while (todo.length) {
    const f = todo.pop() as string;
    if (seen.has(f)) continue;
    seen.add(f);
    todo.push(...imports(f));
  }
  return seen;
}

// The reviewer page runs in a stranger's browser with a capability to one snapshot. Code reachable from it must not include
// the clients for owner-only features, so a refactor cannot quietly give the reviewer bundle a way to call them.
const OWNER_ONLY = ["./api/jobs", "./api/hosts", "./api/analysis", "./api/regions", "./api/runs", "./api/derived", "./api/fs"];

describe("the reviewer page", () => {
  it("does not reach the clients of owner-only features", () => {
    const reached = [...closure("./review/ReviewPage.tsx")];
    expect(reached.length).toBeGreaterThan(3); // the scan found the page's real dependencies
    expect(reached).toContain("./api/boards.ts");
    expect(reached.filter((f) => OWNER_ONLY.some((o) => f.startsWith(o)))).toEqual([]);
  });
});
