import { describe, expect, it } from "vitest";
import { csvHeader, pngWithText, resolveVars, runsCsv, svgWithProvenance } from "./runExport";
import { metric, run } from "./runFixtures";

const ctx = (r: { id: number }) => (r.id === 1 ? { status: "completed", host: "cluster" } : { status: "", host: "" });
const at = "2026-11-12T00:00:00.000Z";

describe("runs CSV", () => {
  const runs = [
    run(1, { block_size: "32", gradient_method: "central" }, { metrics: [metric("am", 0.344, { unit: "rad", uncertainty: 0.02, uncertaintyKind: "standard_error", n: 2695 })] }),
    run(2, { block_size: "16" }, { name: "has, comma", metrics: [metric("am", 0, { uncertainty: 0.1 })] }), // a recorded zero, an uncertainty with no kind
    run(3, {}, { name: "=cmd|calc", metrics: [metric("am", null)] }),
  ];
  const lines = runsCsv(runs, ["status", "p:block_size", "m:am", "generated"], ctx, { query: "block_size:16..64\n am>0", total: 5, at, notRecorded: 1 }).split("\n");

  it("starts with the query and the counts as comment lines", () => {
    expect(lines.slice(0, 5)).toEqual([
      "# Vesqira runs export",
      "# query: block_size:16..64 am>0",
      "# runs: 3 of 5, of which 1 did not record something the query asks about",
      `# exported: ${at}`,
      "# an empty cell means not recorded; a result's uncertainty_kind is empty when the run did not state it",
    ]);
  });
  it("names a result's unit, uncertainty, kind and sample count beside its value", () => {
    expect(lines[5]).toBe("id,name,status,block_size,am,am_unit,am_uncertainty,am_uncertainty_kind,am_n,am_status,generated");
    expect(csvHeader(["m:am"])).toContain("am_uncertainty_kind");
    expect(lines[6]).toBe("1,lmc_1,completed,32,0.344,rad,0.02,standard_error,2695,declared,2026-07-01 09:00Z");
  });
  it("leaves what was not recorded empty, keeps a recorded zero, and leaves the kind empty when it was not stated", () => {
    expect(lines[7]).toBe('2,"has, comma",,16,0,,0.1,,,declared,2026-07-02 09:00Z');
    expect(lines[8]).toBe("3,'=cmd|calc,,,,,,,,,2026-07-03 09:00Z");
  });
  it("ends with a newline", () => {
    expect(lines[lines.length - 1]).toBe("");
  });
});

describe("plot export", () => {
  it("writes the provenance into the SVG after its opening tag, escaped", () => {
    const out = svgWithProvenance('<svg xmlns="http://www.w3.org/2000/svg"><g/></svg>', { tool: "Vesqira cross-run plot", query: "am>0.3 & <x", runIds: [1, 4], y: "am", x: "param:block_size", by: "", exportedAt: at });
    expect(out.startsWith('<svg xmlns="http://www.w3.org/2000/svg"><metadata id="vesqira-provenance">')).toBe(true);
    expect(out).toContain("am&gt;0.3 &amp; &lt;x");
    expect(out).toContain('"runIds":[1,4]');
    const doc = new DOMParser().parseFromString(out, "image/svg+xml");
    expect(doc.querySelector("parsererror")).toBeNull();
    expect(JSON.parse(doc.querySelector("metadata")!.textContent!).runIds).toEqual([1, 4]);
  });
  it("replaces the page's colour tokens by their values", () => {
    expect(resolveVars('<g fill="var(--accent)" style="font-family:var(--font-ui)"/>', (n) => ({ "--accent": "#0a6f8f" })[n] ?? "")).toBe('<g fill="#0a6f8f" style="font-family:currentColor"/>');
  });
  it("escapes a resolved value for an attribute, so a font stack with quotes leaves valid XML", () => {
    const out = resolveVars('<svg style="font-family: var(--font-ui)"><g fill="var(--accent)"/></svg>', (n) => (n === "--font-ui" ? `"Geist Variable","Geist",sans-serif` : "#0a6f8f"));
    expect(out).toBe('<svg style="font-family: &quot;Geist Variable&quot;,&quot;Geist&quot;,sans-serif"><g fill="#0a6f8f"/></svg>');
    expect(new DOMParser().parseFromString(out, "image/svg+xml").querySelector("parsererror")).toBeNull();
  });
  it("adds a text chunk to a PNG right after its header and keeps the rest", () => {
    const png = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 31, 21, 196, 137, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130]);
    const out = pngWithText(png, "vesqira-provenance", '{"query":"am>0.3"}');
    expect(Array.from(out.subarray(0, 33))).toEqual(Array.from(png.subarray(0, 33)));
    expect(new TextDecoder().decode(out.subarray(37, 41))).toBe("iTXt");
    expect(new TextDecoder().decode(out)).toContain('{"query":"am>0.3"}');
    expect(Array.from(out.subarray(out.length - 12))).toEqual(Array.from(png.subarray(png.length - 12))); // IEND intact
    const len = new DataView(out.buffer, out.byteOffset + 33, 4).getUint32(0);
    expect(out.length).toBe(png.length + 12 + len);
  });
  it("refuses bytes that are not a PNG", () => {
    expect(() => pngWithText(new Uint8Array(40), "k", "v")).toThrow("not a PNG");
  });
});
