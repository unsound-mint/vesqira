import type { Run } from "../api/runs";
import { cellOf, FIXED_COLUMNS, metricOf, paramOf } from "./runTable";
import type { ColumnId } from "./runTable";
import type { RunContext } from "./runQuery";

const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();

/** A text cell that a spreadsheet would read as a formula gets a leading quote (numbers such as -0.3 are not text). */
function guard(s: string): string {
  return /^[=+@\t\r]/.test(s) || /^-(?!\d|\.\d)/.test(s) ? `'${s}` : s;
}

function field(s: string): string {
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

export type CsvOptions = {
  query: string;
  /** How many runs the whole list holds, for the header's "n of m". */
  total: number;
  /** When it was made; passed in so the output is reproducible. */
  at: string;
  /** Runs added to the table although a word of the query asks about something they did not record. */
  notRecorded?: number;
};

const kindLabel = (id: ColumnId) => FIXED_COLUMNS.find((c) => c.id === id)?.label ?? id;

/** The header names a column contributes; a result has its unit, uncertainty and the kind of uncertainty beside the value. */
export function csvHeader(columns: ColumnId[]): string[] {
  const names = ["id", "name"];
  for (const c of columns) {
    if (c.startsWith("m:")) {
      const k = c.slice(2);
      names.push(k, `${k}_unit`, `${k}_uncertainty`, `${k}_uncertainty_kind`, `${k}_n`, `${k}_status`);
    } else names.push(c.startsWith("p:") ? c.slice(2) : kindLabel(c).toLowerCase());
  }
  return names;
}

/**
 * The table as CSV: a comment header with the query, then one row per run. An empty cell means "not recorded", never 0;
 * a result carries its unit, its uncertainty and the kind of uncertainty ("kind not stated" is an empty kind).
 */
export function runsCsv(runs: Run[], columns: ColumnId[], ctxOf: (r: Run) => RunContext, o: CsvOptions): string {
  const head = [
    "# Vesqira runs export",
    `# query: ${oneLine(o.query) || "(none)"}`,
    `# runs: ${runs.length} of ${o.total}${o.notRecorded ? `, of which ${o.notRecorded} did not record something the query asks about` : ""}`,
    `# exported: ${o.at}`,
    "# an empty cell means not recorded; a result's uncertainty_kind is empty when the run did not state it",
  ];
  const rows = [csvHeader(columns).map(field).join(",")];
  for (const r of runs) {
    const ctx = ctxOf(r);
    const cells = [String(r.id), guard(r.name)];
    for (const c of columns) {
      if (c.startsWith("m:")) {
        const m = metricOf(r, c.slice(2));
        const have = m !== undefined && m.value !== null && Number.isFinite(m.value);
        cells.push(have ? String(m.value) : "", have ? guard(m.unit) : "", have && m.uncertainty !== null ? String(m.uncertainty) : "", have ? m.uncertaintyKind : "", have && m.n !== null ? String(m.n) : "", have ? m.status : "");
      } else if (c.startsWith("p:")) {
        cells.push(guard(paramOf(r, c.slice(2))?.value ?? ""));
      } else {
        const cell = cellOf(r, c, ctx);
        cells.push(cell.kind === "missing" ? "" : guard(cell.text));
      }
    }
    rows.push(cells.map(field).join(","));
  }
  return `${head.join("\n")}\n${rows.join("\n")}\n`;
}

export type PlotProvenance = {
  tool: "Vesqira cross-run plot";
  query: string;
  runIds: number[];
  y: string;
  x: string;
  by: string;
  exportedAt: string;
};

const xml = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/** Replaces `var(--token)` with the value the page resolves it to, so an exported figure does not depend on the page's stylesheet. */
export function resolveVars(svg: string, value: (name: string) => string): string {
  return svg.replace(/var\((--[a-z0-9-]+)\)/gi, (_, n: string) => value(n) || "currentColor");
}

/** Adds the provenance as machine-readable metadata inside the SVG, after its opening tag. */
export function svgWithProvenance(svg: string, p: PlotProvenance): string {
  const meta = `<metadata id="vesqira-provenance">${xml(JSON.stringify(p))}</metadata>`;
  const i = svg.indexOf(">");
  return i < 0 ? svg : `${svg.slice(0, i + 1)}${meta}${svg.slice(i + 1)}`;
}

let table: Uint32Array | null = null;
function crc32(bytes: Uint8Array): number {
  if (!table) {
    table = new Uint32Array(256);
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      table[n] = c >>> 0;
    }
  }
  let c = 0xffffffff;
  for (const b of bytes) c = table[(c ^ b) & 0xff]! ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

/** A PNG with an `iTXt` chunk (UTF-8 text under `keyword`) right after its header. Throws if the bytes are not a PNG. */
export function pngWithText(png: Uint8Array, keyword: string, text: string): Uint8Array {
  const sig = [137, 80, 78, 71, 13, 10, 26, 10];
  if (png.length < 33 || sig.some((b, i) => png[i] !== b)) throw new Error("not a PNG");
  const enc = new TextEncoder();
  const data = new Uint8Array([...enc.encode(keyword), 0, 0, 0, 0, 0, ...enc.encode(text)]);
  const chunk = new Uint8Array(12 + data.length);
  const view = new DataView(chunk.buffer);
  view.setUint32(0, data.length);
  chunk.set(enc.encode("iTXt"), 4);
  chunk.set(data, 8);
  view.setUint32(8 + data.length, crc32(chunk.subarray(4, 8 + data.length)));
  const at = 8 + 25; // signature, then IHDR (length, type, 13 bytes, crc)
  const out = new Uint8Array(png.length + chunk.length);
  out.set(png.subarray(0, at));
  out.set(chunk, at);
  out.set(png.subarray(at), at + chunk.length);
  return out;
}

export function download(name: string, blob: Blob) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
