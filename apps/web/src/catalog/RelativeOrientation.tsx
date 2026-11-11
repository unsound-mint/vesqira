import { Fragment, useEffect, useRef, useState } from "react";
import { Basis, Button, Input, More, Section, Select } from "../ui";
import { QueueNote, useQueueReason } from "../ui/QueueNote";
import { newRequestId } from "../api/http";
import { ApiError, isCatalog } from "../api/catalog";
import type { Dataset } from "../api/catalog";
import { getDerived, relativeOrientationTableUrl, runRelativeOrientation } from "../api/analysis";
import type { RelOrientParams, RelOrientSummary } from "../api/analysis";
import type { Controls } from "../api/runs";
import { ControlsView } from "../runs/ControlsView";
import { EM } from "./format";
import s from "./RelativeOrientation.module.css";

/* Relative orientation (ADR 0047). Results are agreement statistics between two tracers: worded neutrally, never as a
   validation or a success of either side (AGENTS.md invariant 9). */

const DEFAULTS = {
  snrMin: "3", radiusArcmin: "10", minPixels: "5", nShiftsCatalog: "3000", nShiftsMaps: "1000", minShiftDeg: "1", keepFraction: "0.9", seed: "20261020", bins: "9" };

const norm = (c: string) => c.toLowerCase().replace(/\s+/g, "");
const SNR_PAIRS = [["P(%)", "sP(%)"], ["Pcor", "sPcor"]];

/** A proposal for the P/σP filter from the catalog's declared value columns, or null: P(%) with sP(%), else Pcor with sPcor. The person
    still chooses; nothing is applied from a name match alone. */
export function proposeSnrColumns(values: string[]): { p: string; sigma: string } | null {
  for (const [p, sg] of SNR_PAIRS) {
    const a = values.find((c) => norm(c) === norm(p!));
    const b = values.find((c) => norm(c) === norm(sg!));
    if (a && b) return { p: a, sigma: b };
  }
  return null;
}

/** What is still missing before a dataset can enter a comparison, or null when it can. */
export function notReady(d: Dataset): string | null {
  if (d.hostId !== 0) return "it lives on another host; relative orientation runs on this computer only for now";
  if (isCatalog(d)) {
    const c = d.catalogDeclaration;
    if (!c) return "its columns are not declared";
    if (c.stale) return "the file changed since its columns were declared";
    if (!c.angle) return "no angle column is declared";
    if (!c.angle.quantity || c.angle.quantity === "unspecified") return "what its angle measures is not declared";
    return null;
  }
  const i = d.interpretation;
  if (!i || i.kind !== "axial_orientation") return "it is not declared as an orientation map";
  if (i.stale) return "the file changed since it was declared";
  const miss = [i.frame ? null : "the angle's frame", !i.quantity || i.quantity === "unspecified" ? "what the angle measures" : null].filter(Boolean);
  return miss.length ? `${miss.join(" and ")} ${miss.length > 1 ? "are" : "is"} not declared` : null;
}

/** Why a bootstrap interval has no width: every resample gave one value, so it says nothing about uncertainty. Null when it has width. */
export function bootstrapNote(range: readonly number[], used: number): string | null {
  const [lo, hi] = range;
  if (lo === undefined || hi === undefined || !(hi - lo < 5e-4)) return null;
  return `No spread: all 2000 resamples of the ${used} used source${used === 1 ? "" : "s"} gave the same value, which happens when every source has the same |Δψ| or very few are used. It is not a sign of certainty.`;
}

const fmt = (v: number | null | undefined, digits = 3) => (v === null || v === undefined || !Number.isFinite(v) ? EM : v.toFixed(digits));

/** "Compare orientation with…": from an orientation map (with a catalog or another map) or from a catalog (with a map). */
export function RelativeOrientationPanel({ dataset, datasets, onCreated }: { dataset: Dataset; datasets: Dataset[]; onCreated: (d: Dataset) => void }) {
  const [other, setOther] = useState("");
  const [v, setV] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [rid, setRid] = useState<string | null>(null);
  const queued = useQueueReason(busy, rid);
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);
  const [err, setErr] = useState<{ msg: string; reasons: string[] } | null>(null);
  const why = notReady(dataset);
  const fromCatalog = isCatalog(dataset);
  const candidates = datasets.filter((d) => d.id !== dataset.id && !d.archived && (fromCatalog ? !isCatalog(d) && d.interpretation?.kind === "axial_orientation" : isCatalog(d) ? d.catalogDeclaration?.angle : d.interpretation?.kind === "axial_orientation"));
  const chosen = datasets.find((d) => String(d.id) === other) ?? null;
  const catalogMode = fromCatalog || (chosen !== null && isCatalog(chosen));
  const chosenWhy = chosen ? notReady(chosen) : null;
  const val = (k: keyof typeof DEFAULTS) => v[k] ?? DEFAULTS[k];
  const set = (k: string) => (e: { target: { value: string } }) => setV({ ...v, [k]: e.target.value });
  // every field must be a number: an empty or unreadable one is never sent as a silent default
  const fields: (keyof typeof DEFAULTS)[] = [...(catalogMode ? (["radiusArcmin", "minPixels", "nShiftsCatalog"] as const) : (["nShiftsMaps"] as const)), "minShiftDeg", "keepFraction", "seed", "bins"];
  const catalogDs = fromCatalog ? dataset : chosen;
  const valueCols = catalogMode && catalogDs && isCatalog(catalogDs) ? (catalogDs.catalogDeclaration?.values ?? []) : [];
  const proposal = proposeSnrColumns(valueCols);
  const snrP = v.snrP ?? "";
  const snrSigma = v.snrSigma ?? "";
  const snrOn = catalogMode && snrP !== "" && snrSigma !== "";
  const snrHalf = catalogMode && (snrP === "") !== (snrSigma === "");
  const unreadable = [...fields, ...(snrOn ? (["snrMin"] as const) : [])].filter((k) => val(k).trim() === "" || !Number.isFinite(Number(val(k))));

  if (why) return <p className={s.note}>To compare this {fromCatalog ? "catalog" : "map"} with another orientation tracer, declare it first: {why}.</p>;

  async function go() {
    if (!chosen) return;
    setBusy(true);
    const id = newRequestId();
    setRid(id);
    setErr(null);
    const n = (k: keyof typeof DEFAULTS) => Number(val(k));
    const params: RelOrientParams = { nShifts: n(catalogMode ? "nShiftsCatalog" : "nShiftsMaps"), minShiftDeg: n("minShiftDeg"), keepFraction: n("keepFraction"), seed: n("seed"), bins: n("bins") };
    if (catalogMode) Object.assign(params, { radiusArcmin: n("radiusArcmin"), minPixels: n("minPixels") });
    if (snrOn) Object.assign(params, { snrColumn: snrP, snrSigmaColumn: snrSigma, snrMin: n("snrMin") });
    const map = fromCatalog ? chosen : dataset;
    const otherDs = fromCatalog ? dataset : chosen;
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      const r = await runRelativeOrientation({ datasetId: map.id, otherId: otherDs.id, params }, ctl.signal, id);
      onCreated(r.dataset);
    } catch (e) {
      if (ctl.signal.aborted) setErr({ msg: "Cancelled: the computation was stopped and nothing was kept.", reasons: [] });
      else setErr({ msg: e instanceof Error ? e.message : "The comparison did not run.", reasons: e instanceof ApiError ? e.reasons : [] });
    }
    abort.current = null;
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      <p className={s.note}>An agreement statistic between two orientation tracers, both turned into the B-field orientation from their declarations. Neither side is treated as the reference.</p>
      <Select label="Compare orientation with" value={other} onChange={(e) => setOther(e.target.value)}>
        <option value="">Choose a {fromCatalog ? "map" : "catalog or map"}…</option>
        {candidates.map((d) => <option key={d.id} value={d.id}>{d.name}{isCatalog(d) ? " (catalog)" : ""}</option>)}
      </Select>
      {candidates.length === 0 ? <p className={s.note}>{fromCatalog ? "No map is declared as an orientation map yet." : "No catalog with a declared angle and no other orientation map is registered yet."}</p> : null}
      {chosenWhy ? <p className={s.note}>{chosen?.name}: {chosenWhy}.</p> : null}
      {chosen && !chosenWhy ? (
        <>
          <Basis summary={catalogMode ? "The map is averaged as axial angles in a disk around each source; the chance level shifts the source pattern across the map." : "The two maps must share a grid; the chance level shifts the second map across the first by whole pixels."}>
            <p>
              {catalogMode
                ? "The map is averaged as axial angles in a disk around each source (pixels within the radius on the sky, NaN left out, weighted by the map's declared reliability if it has one). The chance level moves the whole source pattern across the map, keeping each source's angle."
                : "The two maps must share a grid (align one first if not). The chance level moves the second map across the first by whole pixels, with wrap. Pixels are correlated, so the projected Rayleigh statistic is shown without a p-value."}
            </p>
          </Basis>
          <div className={s.grid}>
            {catalogMode ? <Input label="Disk radius (arcmin)" value={val("radiusArcmin")} onChange={set("radiusArcmin")} inputMode="decimal" /> : null}
            <Input label="Shifts in the null" value={val(catalogMode ? "nShiftsCatalog" : "nShiftsMaps")} onChange={set(catalogMode ? "nShiftsCatalog" : "nShiftsMaps")} inputMode="numeric" />
          </div>
          <More summary="Advanced settings" open={unreadable.length > 0 || snrOn || snrHalf}>
            <div className={s.grid}>
              {catalogMode ? <Input label="Fewest pixels in a disk" value={val("minPixels")} onChange={set("minPixels")} inputMode="numeric" /> : null}
              <Input label="Shortest shift (deg)" value={val("minShiftDeg")} onChange={set("minShiftDeg")} inputMode="decimal" />
              <Input label="Share of samples a shift keeps" value={val("keepFraction")} onChange={set("keepFraction")} inputMode="decimal" />
              <Input label="Seed" value={val("seed")} onChange={set("seed")} inputMode="numeric" />
              <Input label="Histogram bins (equal width)" value={val("bins")} onChange={set("bins")} inputMode="numeric" />
            </div>
            {catalogMode ? (
              <fieldset className={s.filter} aria-label="Optional row filter on P/σP">
                <div className={s.grid}>
                  <Select label="Filter rows: column P" value={snrP} onChange={set("snrP")} disabled={valueCols.length === 0}>
                    <option value="">Off: use every row</option>
                    {valueCols.map((c) => <option key={c} value={c}>{c}</option>)}
                  </Select>
                  <Select label="Filter rows: column σP" value={snrSigma} onChange={set("snrSigma")} disabled={valueCols.length === 0}>
                    <option value="">Off: use every row</option>
                    {valueCols.map((c) => <option key={c} value={c}>{c}</option>)}
                  </Select>
                  {snrOn ? <Input label="Keep rows with P/σP ≥" value={val("snrMin")} onChange={set("snrMin")} inputMode="decimal" /> : null}
                </div>
              </fieldset>
            ) : null}
            {catalogMode && valueCols.length === 0 ? <p className={s.note}>No value columns are declared for the catalog, so there is nothing to filter on; declare the P and σP columns as values to enable the P/σP filter.</p> : null}
            {catalogMode && proposal && !(snrP === proposal.p && snrSigma === proposal.sigma) ? (
              <p className={s.note}>
                Proposal, from the column names only: P = {proposal.p}, σP = {proposal.sigma}.{" "}
                <Button onClick={() => setV({ ...v, snrP: proposal.p, snrSigma: proposal.sigma })}>Use this proposal</Button>
              </p>
            ) : null}
            {snrOn ? <p className={s.note}>Rows with P/σP below {val("snrMin")} are left out and listed in the table with that reason; rows with P or σP missing are left out with their own reason. The default is the threshold your comparison script uses (3); the filter is recorded with the result.</p> : null}
            <p className={s.note}>The seed is recorded with the result: the same inputs and seed give the same numbers.</p>
          </More>
          {snrHalf ? <p className={s.err}>The P/σP filter needs both columns: choose P and σP, or set both to Off.</p> : null}
          {unreadable.length ? <p className={s.err}>Every parameter needs a number; {unreadable.length === 1 ? "one is" : `${unreadable.length} are`} empty or not a number.</p> : null}
          <Button variant="primary" disabled={busy || unreadable.length > 0 || snrHalf} onClick={go}>{busy ? "Computing…" : "Compute the alignment measure"}</Button>
          {busy ? <Button onClick={() => abort.current?.abort()}>Cancel</Button> : null}
          <QueueNote reason={queued} />
          <p className={s.note}>A null of thousands of shifts can take minutes on a large map.</p>
        </>
      ) : null}
      {err ? (
        <div role="alert" className={s.err}>
          <p>{err.msg}</p>
          {err.reasons.length ? <ul>{err.reasons.map((r) => <li key={r}>{r}</li>)}</ul> : null}
        </div>
      ) : null}
    </div>
  );
}

/** The result of a relative-orientation comparison, from the dataset's derived record, in its own inspector section. Renders nothing for
    other datasets. */
export function RelativeOrientationResult({ dataset, datasets }: { dataset: Dataset; datasets: Dataset[] }) {
  const [rec, setRec] = useState<{ op: string; sources: number[]; summary: RelOrientSummary; controls: Controls | null } | null>(null);
  useEffect(() => {
    setRec(null);
    getDerived(dataset.id)
      .then((d) => setRec(d.op === "relorient_catalog" || d.op === "relorient_maps" ? { op: d.op, sources: d.sources, summary: d.summary as unknown as RelOrientSummary, controls: d.params.controls ?? null } : null))
      .catch(() => setRec(null));
  }, [dataset.id]);
  if (!rec) return null;
  const names = rec.sources.map((id) => datasets.find((x) => x.id === id)?.name ?? `dataset ${id}`);
  return (
    <Section title="Relative orientation result" meta={`AM ${fmt(rec.summary.am)}`}>
      <RelOrientView s={rec.summary} names={names} tableUrl={rec.summary.table?.file ? relativeOrientationTableUrl(dataset.id) : null} controls={rec.controls} />
    </Section>
  );
}

export function RelOrientView({ s: r, names, tableUrl, controls }: { s: RelOrientSummary; names: string[]; tableUrl: string | null; controls?: Controls | null }) {
  const n = r.null;
  const c = r.counts;
  const p = r.parameters;
  const cat = r.mode === "map_catalog";
  const excluded = Object.entries(c.excluded ?? {});
  return (
    <div className={s.wrap} role="region" aria-label="Relative orientation result">
      <p className={s.kicker}>Agreement statistic · {names[0]} against {names[1] ?? EM}</p>
      <dl className={s.facts}>
        <dt>Alignment measure ⟨cos 2Δψ⟩</dt>
        <dd className={s.big}>{fmt(r.am)}</dd>
        {r.am_weighted !== null && r.am_weighted !== undefined ? (<><dt>Weighted</dt><dd className="mono">{fmt(r.am_weighted)}</dd></>) : null}
        <dt>Chance level (95th percentile of the null)</dt>
        <dd className="mono">{n.skipped ? `not computed: ${n.skipped}` : fmt(n.chance95)}</dd>
        <dt>One-sided p (null ≥ observed)</dt>
        <dd className="mono">{n.skipped ? EM : `${fmt(n.p_one_sided, 4)} from ${n.kept ?? 0} kept of ${n.attempted ?? 0} shifts`}</dd>
        <dt>Median |Δψ|</dt>
        <dd className="mono">{fmt(r.median_abs_dpsi_deg, 1)}°</dd>
        {cat && r.bootstrap_am_16_84 ? (<><dt>Bootstrap 16–84%</dt><dd><span className="mono">{fmt(r.bootstrap_am_16_84[0])} to {fmt(r.bootstrap_am_16_84[1])}</span>{bootstrapNote(r.bootstrap_am_16_84, c.used) ? <span className={s.sub}>{bootstrapNote(r.bootstrap_am_16_84, c.used)}</span> : null}</dd></>) : null}
        {!cat && r.prs ? (<><dt>Projected Rayleigh V</dt><dd className="mono">{fmt(r.prs.V, 1)}{r.prs.V_weighted !== null && r.prs.V_weighted !== undefined ? ` (weighted ${fmt(r.prs.V_weighted, 1)})` : ""}, n = {r.prs.n?.toLocaleString("en-US")}</dd></>) : null}
        {!cat && r.independent_samples ? (<><dt>Independent beams (approx.)</dt><dd className="mono">{r.independent_samples.value !== null && r.independent_samples.value !== undefined ? fmt(r.independent_samples.value, 0) : `not offered: ${r.independent_samples.reason}`}</dd></>) : null}
        <dt>{cat ? "Sources used" : "Pixels used"}</dt>
        <dd className="mono">{c.used.toLocaleString("en-US")} of {(cat ? c.rows : c.pixels)?.toLocaleString("en-US")}{c.share_used !== null && c.share_used !== undefined ? ` (${(100 * c.share_used).toFixed(1)}%)` : ""}</dd>
      </dl>
      {excluded.length ? (
        <ul className={s.list} aria-label="Excluded samples">
          {excluded.map(([why, k]) => <li key={why}><span className="mono">{k.toLocaleString("en-US")}</span> {why}</li>)}
        </ul>
      ) : null}
      {controls?.construction ? (<><p className={s.kicker}>How the control was built</p><ControlsView controls={controls} only="construction" /></>) : null}
      <DeltaPsiHistogram s={r} />
      {cat && r.segments ? <SegmentsFigure s={r} /> : null}
      {!cat && r.prs?.note ? <p className={s.note}>{r.prs.note}</p> : null}
      {!cat && r.independent_samples?.caveat ? <p className={s.note}>{r.independent_samples.caveat}</p> : null}
      {tableUrl ? <a className={s.link} href={tableUrl} download>Download the per-{cat ? "source" : "pixel"} table (CSV)</a> : <p className={s.note}>No table: {r.table?.reason ?? "none was written"}.</p>}
      <details className={s.details}>
        <summary>Definitions, parameters and method</summary>
        <dl className={s.facts}>
          {Object.entries(r.definitions ?? {}).map(([k, d]) => (<Fragment key={k}><dt>{k.replace(/_/g, " ")}</dt><dd>{d}</dd></Fragment>))}
          <dt>Parameters</dt>
          <dd className="mono">{Object.entries(p).map(([k, x]) => `${k}=${x}`).join(" ")}</dd>
          {r.snr_filter ? (<><dt>P/σP filter</dt><dd>{String(r.snr_filter.rule)}; columns {String(r.snr_filter.p_column)} and {String(r.snr_filter.sigma_column)}; {String(r.snr_filter.excluded_below)} rows below, {String(r.snr_filter.excluded_missing)} with P or σP missing.</dd></>) : null}
          <dt>Conversion</dt>
          <dd>{r.conversion}</dd>
          {r.sampling ? (<><dt>Sampling</dt><dd>{r.sampling}</dd></>) : null}
          <dt>Weighting</dt>
          <dd>{typeof r.weighting === "string" ? r.weighting : Object.values(r.weighting ?? {}).join(" ")}</dd>
          <dt>Null</dt>
          <dd>{n.method}{n.scored_on ? `, scored on ${n.scored_on}` : ""}{n.wrapped_below_min_shift ? `; ${n.wrapped_below_min_shift} kept shifts are shorter than the minimum once wrapped (the script's rule tests the unwrapped offset)` : ""}</dd>
        </dl>
        <p className={s.note}>{r.policy}</p>
      </details>
    </div>
  );
}

const HW = 320;
const HH = 196;
const HM = { l: 52, r: 12, t: 10, b: 46 };

/** Fraction of samples per |Δψ| bin, with the null's 2.5–97.5% band and median per bin when a null was run. */
export function DeltaPsiHistogram({ s: r }: { s: RelOrientSummary }) {
  const h = r.histogram;
  const band = r.null.histogram_band ?? null;
  const k = h.fractions.length;
  const ymax = Math.max(0.01, ...h.fractions, ...(band ? band.hi : []));
  const bw = (HW - HM.l - HM.r) / k;
  const px = (i: number) => HM.l + i * bw;
  const py = (f: number) => HH - HM.b - (f / ymax) * (HH - HM.b - HM.t);
  const ticks = [0, ymax / 2, ymax];
  return (
    <figure className={s.fig}>
      <svg className={s.svg} viewBox={`0 0 ${HW} ${HH}`} role="img" aria-label={`Histogram of |Δψ| in ${k} equal-width bins${band ? " with the null's 95% band" : ""}`}>
        {ticks.map((t) => (
          <g key={t}>
            <line className={s.rule} x1={HM.l} x2={HW - HM.r} y1={py(t)} y2={py(t)} />
            <text className={s.tick} x={HM.l - 6} y={py(t) + 4} textAnchor="end">{t.toFixed(2)}</text>
          </g>
        ))}
        {h.fractions.map((f, i) => (
          <rect key={i} className={s.bar} x={px(i) + 1} width={Math.max(0, bw - 2)} y={py(f)} height={Math.max(0, py(0) - py(f))} rx={2}>
            <title>{`${h.edges_deg[i]}–${h.edges_deg[i + 1]}°: ${h.counts[i]} (${(100 * f).toFixed(1)}%)${band ? `; null ${(100 * band.lo[i]!).toFixed(1)}–${(100 * band.hi[i]!).toFixed(1)}%` : ""}`}</title>
          </rect>
        ))}
        {band ? band.lo.map((lo, i) => (
          <g key={`b${i}`}>
            <line className={s.band} x1={px(i) + bw / 2} x2={px(i) + bw / 2} y1={py(band.hi[i]!)} y2={py(lo)} />
            <line className={s.band} x1={px(i) + bw * 0.35} x2={px(i) + bw * 0.65} y1={py(band.hi[i]!)} y2={py(band.hi[i]!)} />
            <line className={s.band} x1={px(i) + bw * 0.35} x2={px(i) + bw * 0.65} y1={py(lo)} y2={py(lo)} />
            <circle className={s.median} cx={px(i) + bw / 2} cy={py(band.median[i]!)} r={2.5} />
          </g>
        )) : null}
        <line className={s.axis} x1={HM.l} x2={HW - HM.r} y1={py(0)} y2={py(0)} />
        {[0, 45, 90].map((d) => <text key={d} className={s.tick} x={HM.l + (d / 90) * (HW - HM.l - HM.r)} y={HH - HM.b + 16} textAnchor={d === 0 ? "start" : d === 90 ? "end" : "middle"}>{d}°</text>)}
        <text className={s.tick} x={(HW + HM.l - HM.r) / 2} y={HH - 6} textAnchor="middle">|Δψ| (0° parallel, 90° perpendicular)</text>
        <text className={s.tick} transform={`translate(11 ${(HH - HM.b + HM.t) / 2}) rotate(-90)`} textAnchor="middle">share of {r.mode === "map_catalog" ? "sources" : "pixels"}</text>
      </svg>
      <figcaption className={s.note}>
        Bars: share of {r.mode === "map_catalog" ? "sources" : "pixels"} per bin ({k} equal-width bins of {90 / k}°).{band ? " Whiskers: the null's 2.5–97.5% range per bin; dot: its median." : " No null was run."}
      </figcaption>
    </figure>
  );
}

const SW = 320;
const SM = { l: 40, r: 10, t: 10, b: 36 };

/** The longest "round" length (1, 2, 5 times a power of ten) that fits in `max`, for a scale bar. */
export function niceLength(max: number): number {
  if (!(max > 0)) return 0;
  const p = 10 ** Math.floor(Math.log10(max));
  return [5, 2, 1].map((m) => m * p).find((v) => v <= max) ?? p;
}

/** Map vs catalog: the map's orientation (solid) and the catalog's (dashed, outlined) at each source, in the map's pixel frame, over
    the map's outline. Not the map raster: the figure is self-contained. North and east depend on the map's WCS, which the result does
    not carry, so the axes are pixels (a frame with ticks) and the scale bar comes from the map's stated pixel scale. */
export function SegmentsFigure({ s: r }: { s: RelOrientSummary }) {
  const g = r.segments!;
  const nx = (r.map as { nx?: number }).nx ?? 1;
  const ny = (r.map as { ny?: number }).ny ?? 1;
  const scaleArcmin = (r.map as { pixel_scale_arcmin?: number }).pixel_scale_arcmin;
  const scale = (SW - SM.l - SM.r) / Math.max(nx, ny);
  const w = SW;
  const h = ny * scale + SM.t + SM.b;
  const len = Math.max(8, Math.min(40, (SW / Math.sqrt(Math.max(1, g.x.length))) * 0.5));
  const X = (x: number) => SM.l + (x + 0.5) * scale;
  const Y = (y: number) => h - SM.b - (y + 0.5) * scale;                   // FITS y up
  // the catalog's segment is the longer one, so it shows past the map's when the two agree
  const seg = (x: number, y: number, deg: number, f = 1) => {
    const a = (deg * Math.PI) / 180;
    const dx = (Math.cos(a) * len * f) / 2;
    const dy = (Math.sin(a) * len * f) / 2;
    return { x1: X(x) - dx, y1: Y(y) + dy, x2: X(x) + dx, y2: Y(y) - dy };
  };
  const xTicks = [0, Math.round(nx / 2), nx];
  const yTicks = [0, Math.round(ny / 2), ny];
  const barArcmin = scaleArcmin && scaleArcmin > 0 ? niceLength(0.3 * nx * scaleArcmin) : 0;
  const barPx = barArcmin && scaleArcmin ? (barArcmin / scaleArcmin) * scale : 0;
  const bx = SM.l + nx * scale - 8 - barPx;
  const by = h - SM.b - 10;
  return (
    <figure className={s.fig}>
      <svg className={s.svg} viewBox={`0 0 ${w.toFixed(1)} ${h.toFixed(1)}`} role="img" aria-label={`Orientations at ${g.x.length} sources over the map outline in its pixel frame: map solid, catalog dashed`}>
        <rect className={s.outline} x={SM.l} y={SM.t} width={nx * scale} height={ny * scale} />
        {xTicks.map((t) => (
          <g key={`x${t}`}>
            <line className={s.axis} x1={SM.l + t * scale} x2={SM.l + t * scale} y1={SM.t + ny * scale} y2={SM.t + ny * scale + 4} />
            <text className={s.tick} x={SM.l + t * scale} y={SM.t + ny * scale + 16} textAnchor={t === 0 ? "start" : t === nx ? "end" : "middle"}>{t}</text>
          </g>
        ))}
        {yTicks.map((t) => (
          <g key={`y${t}`}>
            <line className={s.axis} x1={SM.l - 4} x2={SM.l} y1={h - SM.b - t * scale} y2={h - SM.b - t * scale} />
            <text className={s.tick} x={SM.l - 7} y={h - SM.b - t * scale + (t === 0 ? 0 : t === ny ? 8 : 4)} textAnchor="end">{t}</text>
          </g>
        ))}
        <text className={s.tick} x={SM.l + (nx * scale) / 2} y={h - 4} textAnchor="middle">x (pixel)</text>
        <text className={s.tick} transform={`translate(10 ${SM.t + (ny * scale) / 2}) rotate(-90)`} textAnchor="middle">y (pixel)</text>
        {g.x.map((x, i) => {
          const y = g.y[i]!;
          const m = g.map_pix_deg[i];
          const o = seg(x, y, g.other_pix_deg[i]!);
          return (
            <g key={i} opacity={g.used[i] ? 1 : 0.35}>
              <line className={s.segOther} {...o} />
              {m !== null && m !== undefined ? <line className={s.segMap} {...seg(x, y, m, 0.6)} /> : null}
            </g>
          );
        })}
        {barPx > 0 ? (
          <g>
            <line className={s.scaleBar} x1={bx} x2={bx + barPx} y1={by} y2={by} />
            <text className={s.tick} x={bx + barPx / 2} y={by - 5} textAnchor="middle">{barArcmin >= 1 ? `${barArcmin}′` : `${barArcmin * 60}″`}</text>
          </g>
        ) : null}
      </svg>
      <figcaption className={s.note}>
        <span className={s.keyMap} aria-hidden="true" /> map, averaged in the disk · <span className={s.keyOther} aria-hidden="true" /> catalog · faded: not used (see the table for the reason).
        Pixel frame of the map, FITS y up; the bar is the map's pixel scale. North and east are not drawn: they depend on the map's WCS, which this figure does not carry. Segment lengths are fixed (the catalog's is drawn longer so both show).{g.truncated ? " Only the first sources are drawn; the table has all of them." : ""}
      </figcaption>
    </figure>
  );
}
