/**
 * How a report body splits into blocks: paragraphs, headings, lists, tables, quotes, code and $$ math, and figure or
 * run references alone on their line. It must match services/api/internal/reports/markdown.go exactly, because a
 * comment is anchored to a block on the reader's page and the author edits the same block here. Both sides are tested
 * against packages/contracts/fixtures/report-blocks.json.
 */
export type SourceBlock =
  | { kind: "text" | "code"; text: string }
  | { kind: "figure"; figure: string }
  | { kind: "run"; run: number };

import { splitFront } from "./front";

const FENCE = /^\s{0,3}(```+|~~~+)/;
const FIGURE_LINE = /^\s*!\[\[figure:([0-9a-f]{64})\]\]\s*$/;
const RUN_LINE = /^\s*\[\[run:([0-9]{1,18})\]\]\s*$/;

function isHeading(s: string): boolean {
  let n = 0;
  while (n < s.length && s[n] === "#") n++;
  return n >= 1 && n <= 6 && (n === s.length || s[n] === " ");
}

/** Front matter (see front.ts) is not a block. */
export function splitBlocks(body: string): SourceBlock[] {
  const lines = splitFront(body).rest.split("\n");
  const out: SourceBlock[] = [];
  let cur: string[] = [];
  const flush = () => {
    if (cur.length) {
      const t = cur.join("\n").replace(/^\n+|\n+$/g, "");
      if (t.trim() !== "") out.push({ kind: "text", text: t });
      cur = [];
    }
  };
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i] as string;
    const fence = FENCE.exec(ln);
    if (fence) {
      flush();
      const marker = (fence[1] as string).trim();
      const block = [ln];
      for (i++; i < lines.length; i++) {
        const l = lines[i] as string;
        block.push(l);
        const t = l.trim();
        if (t.startsWith(marker.slice(0, 3)) && t.split(marker[0] as string).join("") === "") break;
      }
      out.push({ kind: "code", text: block.join("\n") });
      continue;
    }
    const t = ln.trim();
    if (t.startsWith("$$") && !t.slice(2).endsWith("$$")) {
      flush();
      const block = [ln];
      for (i++; i < lines.length; i++) {
        const l = lines[i] as string;
        block.push(l);
        if (l.trim().endsWith("$$")) break;
      }
      out.push({ kind: "text", text: block.join("\n") });
      continue;
    }
    const fig = FIGURE_LINE.exec(ln);
    if (fig) {
      flush();
      out.push({ kind: "figure", figure: fig[1] as string });
      continue;
    }
    const run = RUN_LINE.exec(ln);
    if (run) {
      flush();
      out.push({ kind: "run", run: Number(run[1]) });
      continue;
    }
    if (t === "") {
      flush();
      continue;
    }
    const left = ln.replace(/^ +/, "");
    if (left.startsWith("#") && isHeading(left)) {
      flush();
      out.push({ kind: "text", text: ln });
      continue;
    }
    cur.push(ln);
  }
  flush();
  return out;
}

/** The Markdown that reproduces a block, so blocks can be edited one at a time and joined back into a body. */
export function blockSource(b: SourceBlock): string {
  if (b.kind === "figure") return `![[figure:${b.figure}]]`;
  if (b.kind === "run") return `[[run:${b.run}]]`;
  return b.text;
}

export function joinBlocks(sources: string[]): string {
  return sources.map((s) => s.replace(/^\n+|\n+$/g, "")).filter((s) => s.trim() !== "").join("\n\n");
}
