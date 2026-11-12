import { useRef, useState } from "react";
import { Button } from "../ui";
import type { RelOrientSummary } from "../api/analysis";
import { csvField, csvGuard, download, pngWithText, resolveVars, svgWithProvenance } from "./runExport";
import { EM } from "./format";
import s from "./RelativeOrientation.module.css";

/* Relative orientation inside bins of a third map (ADR 0047, amendment). Every statistic is the comparison's own, computed by the
   worker on the bin's pixels with axial angles; this file only draws and exports what the result record holds. */

type Strata = NonNullable<RelOrientSummary["stratified"]>;
type Bin = Strata["bins"][number];

const FONT = "var(--font-ui)";
const NUM = "var(--font-data)";
// Drawn at the inspector's width (like the histogram beside it), so text is at its stated size on screen and in the export.
const W = 340;
const L = 44;
const R = 12;
const T = 52; // title, then up to two rows of per-bin counts above the plot
const PH = 170;
const PW = W - L - R;
const LINE = 14;

/** Words of `text` in lines of at most `max` characters (a word longer than that keeps its own line). */
export function wrapWords(text: string, max: number): string[] {
  const out: string[] = [];
  let cur = "";
  for (const w of text.split(/\s+/).filter(Boolean)) {
    if (cur && cur.length + 1 + w.length > max) {
      out.push(cur);
      cur = w;
    } else cur = cur ? `${cur} ${w}` : w;
  }
  if (cur) out.push(cur);
  return out;
}

const sig = (v: number, digits = 3) => {
  if (!Number.isFinite(v)) return EM;
  const r = Number(v.toPrecision(digits));
  return r !== 0 && (Math.abs(r) >= 1e6 || Math.abs(r) < 1e-3) ? r.toExponential() : String(r);   // column densities are written 4e+20, not as twenty digits
};
const fmt = (v: number | null | undefined, digits = 3) => (v === null || v === undefined || !Number.isFinite(v) ? EM : v.toFixed(digits));

/** Bin edges typed as numbers separated by commas, spaces or semicolons: the list, or the reason it cannot be used. */
export function parseEdges(text: string): { edges: number[] } | { error: string } {
  const parts = text.split(/[\s,;]+/).filter((x) => x !== "");
  if (parts.length < 2) return { error: "Give at least two edges, such as 1e20, 5e20, 1e21." };
  const edges = parts.map(Number);
  if (edges.some((e) => !Number.isFinite(e))) return { error: "Every edge must be a number." };
  if (edges.some((e, i) => i > 0 && e <= edges[i - 1]!)) return { error: "The edges must increase." };
  if (edges.length > 51) return { error: "At most 50 bins." };
  return { edges };
}

/** The uncertainty drawn as the reference band: independent beams where the maps declare a beam, else pixels. */
const referenceSigma = (b: Bin) => b.uniform_reference?.sigma_beams ?? b.uniform_reference?.sigma_pixels ?? null;

const niceCeil = (v: number) => [0.05, 0.1, 0.2, 0.25, 0.4, 0.5, 0.75, 1].find((x) => x >= v) ?? 1;

/** The plot's numbers: the vertical range from what is drawn, never from the mean alone. */
export function plotRange(bins: Bin[]): number {
  let m = 0.05;
  for (const b of bins) {
    if (b.am === null || b.am === undefined) continue;
    const [lo, hi] = b.bootstrap_am_16_84 ?? [b.am, b.am];
    m = Math.max(m, Math.abs(b.am), Math.abs(lo!), Math.abs(hi!), referenceSigma(b) ?? 0);
  }
  return niceCeil(m * 1.08);
}

/** The table as CSV: a comment header with the result's facts, then one row per bin. An empty cell means not available, never 0. */
export function strataCsv(st: Strata, names: string[], at: string): string {
  const u = st.third_map.unit;
  const head = [
    "# Vesqira relative orientation by bins of a third map (agreement statistic, not a validation of either tracer)",
    `# maps: ${names[0] ?? EM} against ${names[1] ?? EM}; binned by ${names[2] ?? EM}`,
    `# third map unit: ${u} (${st.third_map.unit_source === "file" ? "the file's BUNIT" : st.third_map.unit_source === "user" ? "declared by the author; the file has no BUNIT" : "the author's correction"})`,
    `# binning: ${st.binning.mode === "equal_count" ? "equal count" : "user-set edges"}, ${st.binning.bins} bin${st.binning.bins === 1 ? "" : "s"}; edges ${st.binning.edges.map((e) => sig(e, 6)).join(" ")}`,
    `# pixels: ${st.counts.compared} compared, ${st.counts.third_map_missing} with no third-map value, ${st.counts.outside_edges} outside the edges, ${st.counts.in_bins} in bins`,
    "# AM = mean of cos 2(dpsi) with axial angles; uniform_sigma is the standard deviation of AM for independent uniform angles; an empty cell means not available",
    `# exported: ${at}`,
  ];
  const cols = ["bin", `lower_${u}`, `upper_${u}`, `centre_${u}`, `median_${u}`, "pixels", "independent_beams", "am", "am_weighted", "am_boot16", "am_boot84", "V", "V_weighted", "uniform_sigma_pixels", "uniform_sigma_beams", "median_abs_dpsi_deg"];
  const n = (v: number | null | undefined) => (v === null || v === undefined || !Number.isFinite(v) ? "" : v !== 0 && (Math.abs(v) >= 1e15 || Math.abs(v) < 1e-6) ? v.toExponential() : String(v));
  const rows = st.bins.map((b) => [String(b.index + 1), n(b.lo), n(b.hi), n(b.centre), n(b.median), String(b.n), n(b.independent_beams), n(b.am), n(b.am_weighted), n(b.bootstrap_am_16_84?.[0]), n(b.bootstrap_am_16_84?.[1]),
    n(b.prs?.V), n(b.prs?.V_weighted), n(b.uniform_reference?.sigma_pixels), n(b.uniform_reference?.sigma_beams), n(b.median_abs_dpsi_deg)]);
  return `${head.join("\n")}\n${cols.map(csvField).join(",")}\n${rows.map((r) => r.map((c) => csvField(csvGuard(c))).join(",")).join("\n")}\n`;
}

export type StrataProvenance = {
  tool: "Vesqira stratified orientation plot";
  label: "agreement statistic";
  resultDatasetId: number | null;
  maps: { first: string; second: string; third: string };
  thirdMapUnit: { unit: string; source: string };
  binning: Strata["binning"];
  counts: Strata["counts"];
  parameters: Record<string, number | string>;
  exportedAt: string;
};

const viewBoxOf = (el: SVGSVGElement) => {
  const [, , width = W, height = W] = (el.getAttribute("viewBox") ?? "").split(/\s+/).map(Number);
  return { width, height };
};

export function StratifiedView({ s: r, names, resultId = null }: { s: RelOrientSummary; names: string[]; resultId?: number | null }) {
  const st = r.stratified;
  const svgRef = useRef<SVGSVGElement>(null);
  const [err, setErr] = useState<string | null>(null);
  if (!st) return null;
  const u = st.third_map.unit;
  const bins = st.bins;
  const x0 = st.binning.edges[0]!;
  const x1 = st.binning.edges[st.binning.edges.length - 1]!;
  const span = x1 > x0 ? x1 - x0 : 1;
  const px = (v: number) => L + ((v - x0) / span) * PW;
  const ymax = plotRange(bins);
  const py = (v: number) => T + PH / 2 - (v / ymax) * (PH / 2);
  const yTicks = [-ymax, -ymax / 2, 0, ymax / 2, ymax];
  const xTicks = [x0, x0 + span / 2, x1];
  const third = names[2] ?? "the third map";
  const beams = bins.some((b) => b.uniform_reference?.sigma_beams != null);
  const label = `Alignment measure against ${third} in ${bins.length} bin${bins.length === 1 ? "" : "s"}, with the bootstrap range and the uniform-angle reference`;

  const provenance = (): StrataProvenance => ({
    tool: "Vesqira stratified orientation plot", label: "agreement statistic", resultDatasetId: resultId,
    maps: { first: names[0] ?? "", second: names[1] ?? "", third }, thirdMapUnit: { unit: u, source: st.third_map.unit_source },
    binning: st.binning, counts: st.counts, parameters: r.parameters as Record<string, number | string>, exportedAt: new Date().toISOString() });
  const svgText = () => {
    const el = svgRef.current;
    if (!el) return null;
    const css = getComputedStyle(document.documentElement);
    let raw = new XMLSerializer().serializeToString(el).replace(/ tabindex="0"/, "").replace(/ data-testid="[^"]*"/g, "");
    const box = viewBoxOf(el);
    raw = raw.replace('width="100%"', `width="${box.width}" height="${box.height}"`);
    if (!/^<svg[^>]*\sxmlns="/.test(raw)) raw = raw.replace("<svg", '<svg xmlns="http://www.w3.org/2000/svg"');
    return svgWithProvenance(resolveVars(raw, (n) => css.getPropertyValue(n).trim()), provenance());
  };
  const exportSvg = () => {
    const t = svgText();
    if (t) download("vesqira-orientation-by-bins.svg", new Blob([`<?xml version="1.0" encoding="UTF-8"?>\n${t}`], { type: "image/svg+xml" }));
  };
  async function exportPng() {
    const t = svgText();
    const el = svgRef.current;
    if (!t || !el) return;
    try {
      const box = viewBoxOf(el);
      const img = new Image(box.width, box.height);
      img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(t)}`;
      await img.decode();
      const canvas = document.createElement("canvas");
      canvas.width = box.width * 2;
      canvas.height = box.height * 2;
      const g = canvas.getContext("2d");
      if (!g) throw new Error("no canvas");
      g.drawImage(img, 0, 0, canvas.width, canvas.height);
      const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, "image/png"));
      if (!blob) throw new Error("no image");
      const bytes = pngWithText(new Uint8Array(await blob.arrayBuffer()), "vesqira-provenance", JSON.stringify(provenance()));
      download("vesqira-orientation-by-bins.png", new Blob([bytes as BlobPart], { type: "image/png" }));
      setErr(null);
    } catch (e) {
      setErr(`The browser could not draw the PNG (${e instanceof Error ? e.message : "no reason given"}); the SVG has the same figure.`);
    }
  }
  const exportCsv = () => download("vesqira-orientation-by-bins.csv", new Blob([strataCsv(st, names, new Date().toISOString())], { type: "text/csv;charset=utf-8" }));
  const footer = `${names[0] ?? EM} against ${names[1] ?? EM} · by ${third} (${u}) · ${st.binning.mode === "equal_count" ? "equal-count" : "user-set"} bins · agreement statistic`;
  const legend = wrapWords(`Line: the bin's range, at its AM. Dot: AM at the range centre. Whiskers: bootstrap 16–84% (pixels treated as independent, so optimistic). Grey: ±1σ of AM for uniform angles, for ${beams ? "independent beams" : "pixels"}. Line at 0: no preferred orientation. n: pixels in the bin.`, 54);
  const foot = wrapWords(footer, 50);
  const legendY = T + PH + 56;
  const footY = legendY + legend.length * LINE + 6;
  const H = footY + foot.length * LINE + 4;
  const twoRows = bins.length > 4;

  return (
    <div className={s.wrap} role="region" aria-label="Alignment by bins of a third map">
      <p className={s.kicker}>Binned by {third} · {st.binning.bins} bin{st.binning.bins === 1 ? "" : "s"} · unit {u}</p>
      <p className={s.note}>{st.third_map.unit_note ? `${u}: ${st.third_map.unit_note}. ` : ""}{st.counts.in_bins.toLocaleString("en-US")} of {st.counts.compared.toLocaleString("en-US")} compared pixels are in bins; {st.counts.third_map_missing.toLocaleString("en-US")} have no value in the third map{st.binning.mode === "edges" ? `; ${st.counts.outside_edges.toLocaleString("en-US")} lie outside the edges` : ""}. Pixels missing or masked in either orientation map were already left out of the comparison.</p>
      {(st.binning.notes ?? []).map((n) => <p key={n} className={s.note}>{n}.</p>)}
      <figure className={s.fig}>
        <svg ref={svgRef} data-testid="strata-plot" viewBox={`0 0 ${W} ${H}`} width="100%" role="img" aria-label={label} style={{ display: "block", maxWidth: W * 1.5, background: "var(--bg-1)", fontFamily: FONT }}>
          <rect x={0} y={0} width={W} height={H} fill="var(--bg-1)" />
          <text x={8} y={16} fontSize={12.5} fontWeight={600} fontFamily={FONT} fill="var(--text)">Alignment measure AM = ⟨cos 2Δψ⟩</text>
          {yTicks.map((v) => (
            <g key={v}>
              <line x1={L} x2={W - R} y1={py(v)} y2={py(v)} stroke={v === 0 ? "var(--text-2)" : "var(--line)"} strokeWidth={v === 0 ? 1.4 : 1} />
              <text x={L - 8} y={py(v) + 4} textAnchor="end" fontSize={11} fontFamily={NUM} fill="var(--text-2)">{sig(v, 2)}</text>
            </g>
          ))}
          {bins.map((b) => {
            const sg = referenceSigma(b);
            if (b.n === 0 || sg === null) return null;
            return <rect key={`r${b.index}`} data-ref x={px(b.lo)} width={Math.max(1, px(b.hi) - px(b.lo))} y={py(sg)} height={Math.max(0, py(-sg) - py(sg))} fill="var(--line)" opacity={0.7} />;
          })}
          <line x1={L} x2={W - R} y1={T + PH} y2={T + PH} stroke="var(--line-strong)" />
          {xTicks.map((v, i) => (
            <g key={i}>
              <line x1={px(v)} x2={px(v)} y1={T + PH} y2={T + PH + 4} stroke="var(--line-strong)" />
              <text x={px(v)} y={T + PH + 17} textAnchor={i === 0 ? "start" : i === 2 ? "end" : "middle"} fontSize={11} fontFamily={NUM} fill="var(--text-2)">{sig(v, 3)}</text>
            </g>
          ))}
          {bins.map((b, i) => (b.n > 0 && b.am !== null && b.am !== undefined ? (
            <g key={`b${b.index}`} data-bin={b.index}>
              <line x1={px(b.lo)} x2={px(b.hi)} y1={py(b.am)} y2={py(b.am)} stroke="var(--accent)" strokeWidth={2} strokeLinecap="round" />
              {b.bootstrap_am_16_84 ? (
                <>
                  <line x1={px(b.centre)} x2={px(b.centre)} y1={py(b.bootstrap_am_16_84[0]!)} y2={py(b.bootstrap_am_16_84[1]!)} stroke="var(--text)" strokeWidth={1.5} />
                  {b.bootstrap_am_16_84.map((v, k) => <line key={k} x1={px(b.centre) - 4} x2={px(b.centre) + 4} y1={py(v)} y2={py(v)} stroke="var(--text)" strokeWidth={1.5} />)}
                </>
              ) : null}
              <circle cx={px(b.centre)} cy={py(b.am)} r={3.6} fill="var(--accent)" stroke="var(--bg-1)" strokeWidth={1} />
              <text x={px(b.centre)} y={T - 6 - (twoRows ? (i % 2) * 12 : 0)} textAnchor="middle" fontSize={10.5} fontFamily={NUM} fill="var(--text-2)">n={b.n.toLocaleString("en-US")}</text>
            </g>
          ) : (
            <text key={`e${b.index}`} data-bin={b.index} x={px(b.centre)} y={py(0) - 8} textAnchor="middle" fontSize={10.5} fontFamily={NUM} fill="var(--text-2)">empty</text>
          )))}
          <text x={L + PW / 2} y={T + PH + 36} textAnchor="middle" fontSize={12} fontFamily={FONT} fill="var(--text-2)">{third} ({u})</text>
          {legend.map((t, k) => <text key={`l${k}`} x={8} y={legendY + k * LINE} fontSize={11} fontFamily={FONT} fill="var(--text-2)">{t}</text>)}
          {foot.map((t, k) => <text key={`f${k}`} x={8} y={footY + k * LINE} fontSize={10.5} fontFamily={NUM} fill="var(--text-3)">{t}</text>)}
        </svg>
        <figcaption className={s.note}>
          Axial statistics throughout: Δψ is folded into [−90°, 90°) before cos 2Δψ, so 1° and 179° are 2° apart. The whole comparison&apos;s chance level does not apply to a bin; none is drawn.
        </figcaption>
      </figure>
      <div className={s.tableWrap} role="region" aria-label="Alignment by bin, scrollable" tabIndex={0}>
        <table className={s.table} aria-label="Alignment by bin">
          <thead>
            <tr>
              <th scope="col">Bin</th>
              <th scope="col">Range ({u})</th>
              <th scope="col">Centre ({u})</th>
              <th scope="col">Pixels</th>
              <th scope="col">Beams</th>
              <th scope="col">AM</th>
              <th scope="col">16–84%</th>
              <th scope="col">V</th>
              <th scope="col">Uniform σ</th>
            </tr>
          </thead>
          <tbody>
            {bins.map((b) => (
              <tr key={b.index}>
                <th scope="row">{b.index + 1}</th>
                <td className="mono">{sig(b.lo)} to {sig(b.hi)}</td>
                <td className="mono">{sig(b.centre)}</td>
                <td className="mono">{b.n.toLocaleString("en-US")}</td>
                <td className="mono">{b.independent_beams != null ? fmt(b.independent_beams, 0) : EM}</td>
                <td className="mono">{fmt(b.am)}{b.am_weighted != null ? ` (w ${fmt(b.am_weighted)})` : ""}</td>
                <td className="mono">{b.bootstrap_am_16_84 ? `${fmt(b.bootstrap_am_16_84[0])} to ${fmt(b.bootstrap_am_16_84[1])}` : EM}</td>
                <td className="mono">{fmt(b.prs?.V, 1)}</td>
                <td className="mono">{b.uniform_reference?.sigma_pixels != null ? `${fmt(b.uniform_reference.sigma_pixels, 4)} px${b.uniform_reference.sigma_beams != null ? `, ${fmt(b.uniform_reference.sigma_beams, 4)} beams` : ""}` : EM}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className={s.note}>V is the projected Rayleigh statistic of the bin and is shown without a p-value: pixels are correlated over the beam. {beams ? "" : "Beams are not offered because a map declares no beam (BMAJ, BMIN). "}{st.policy}</p>
      <div className={s.actions}>
        <Button onClick={exportSvg}>Export figure (SVG)</Button>
        <Button onClick={exportPng}>Export figure (PNG)</Button>
        <Button onClick={exportCsv}>Export table (CSV)</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
