import { Fragment, useEffect, useRef, useState } from "react";
import { Button, Input, Section, Select } from "../ui";
import { ApiError, isCatalog } from "../api/catalog";
import type { Dataset } from "../api/catalog";
import { getDerived, relativeOrientationTableUrl, runRelativeOrientation } from "../api/analysis";
import type { RelOrientParams, RelOrientSummary } from "../api/analysis";
import { EM } from "./format";
import s from "./RelativeOrientation.module.css";

/* Relative orientation (ADR 0047). Results are agreement statistics between two tracers: worded neutrally, never as a
   validation or a success of either side (AGENTS.md invariant 9). */

const DEFAULTS = { radiusArcmin: "10", minPixels: "5", nShiftsCatalog: "3000", nShiftsMaps: "1000", minShiftDeg: "1", keepFraction: "0.9", seed: "20261020", bins: "9" };

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

const fmt = (v: number | null | undefined, digits = 3) => (v === null || v === undefined || !Number.isFinite(v) ? EM : v.toFixed(digits));

/** "Compare orientation with…": from an orientation map (with a catalog or another map) or from a catalog (with a map). */
export function RelativeOrientationPanel({ dataset, datasets, onCreated }: { dataset: Dataset; datasets: Dataset[]; onCreated: (d: Dataset) => void }) {
  const [other, setOther] = useState("");
  const [v, setV] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
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
  const unreadable = fields.filter((k) => val(k).trim() === "" || !Number.isFinite(Number(val(k))));

  if (why) return <p className={s.note}>To compare this {fromCatalog ? "catalog" : "map"} with another orientation tracer, declare it first: {why}.</p>;

  async function go() {
    if (!chosen) return;
    setBusy(true);
    setErr(null);
    const n = (k: keyof typeof DEFAULTS) => Number(val(k));
    const params: RelOrientParams = { nShifts: n(catalogMode ? "nShiftsCatalog" : "nShiftsMaps"), minShiftDeg: n("minShiftDeg"), keepFraction: n("keepFraction"), seed: n("seed"), bins: n("bins") };
    if (catalogMode) Object.assign(params, { radiusArcmin: n("radiusArcmin"), minPixels: n("minPixels") });
    const map = fromCatalog ? chosen : dataset;
    const otherDs = fromCatalog ? dataset : chosen;
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      const r = await runRelativeOrientation({ datasetId: map.id, otherId: otherDs.id, params }, ctl.signal);
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
          <p className={s.note}>
            {catalogMode
              ? "The map is averaged as axial angles in a disk around each source (pixels within the radius on the sky, NaN left out, weighted by the map's declared reliability if it has one). The chance level moves the whole source pattern across the map, keeping each source's angle."
              : "The two maps must share a grid (align one first if not). The chance level moves the second map across the first by whole pixels, with wrap. Pixels are correlated, so the projected Rayleigh statistic is shown without a p-value."}
          </p>
          <div className={s.grid}>
            {catalogMode ? <Input label="Disk radius (arcmin)" value={val("radiusArcmin")} onChange={set("radiusArcmin")} inputMode="decimal" /> : null}
            {catalogMode ? <Input label="Fewest pixels in a disk" value={val("minPixels")} onChange={set("minPixels")} inputMode="numeric" /> : null}
            <Input label="Shifts in the null" value={val(catalogMode ? "nShiftsCatalog" : "nShiftsMaps")} onChange={set(catalogMode ? "nShiftsCatalog" : "nShiftsMaps")} inputMode="numeric" />
            <Input label="Shortest shift (deg)" value={val("minShiftDeg")} onChange={set("minShiftDeg")} inputMode="decimal" />
            <Input label="Share of samples a shift keeps" value={val("keepFraction")} onChange={set("keepFraction")} inputMode="decimal" />
            <Input label="Seed" value={val("seed")} onChange={set("seed")} inputMode="numeric" />
            <Input label="Histogram bins (equal width)" value={val("bins")} onChange={set("bins")} inputMode="numeric" />
          </div>
          {unreadable.length ? <p className={s.err}>Every parameter needs a number; {unreadable.length === 1 ? "one is" : `${unreadable.length} are`} empty or not a number.</p> : null}
          <Button variant="primary" disabled={busy || unreadable.length > 0} onClick={go}>{busy ? "Computing…" : "Compute the alignment measure"}</Button>
          {busy ? <Button onClick={() => abort.current?.abort()}>Cancel</Button> : null}
          <p className={s.note}>The seed is recorded with the result: the same inputs and seed give the same numbers. A null of thousands of shifts can take minutes on a large map.</p>
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
  const [rec, setRec] = useState<{ op: string; sources: number[]; summary: RelOrientSummary } | null>(null);
  useEffect(() => {
    setRec(null);
    getDerived(dataset.id)
      .then((d) => setRec(d.op === "relorient_catalog" || d.op === "relorient_maps" ? { op: d.op, sources: d.sources, summary: d.summary as unknown as RelOrientSummary } : null))
      .catch(() => setRec(null));
  }, [dataset.id]);
  if (!rec) return null;
  const names = rec.sources.map((id) => datasets.find((x) => x.id === id)?.name ?? `dataset ${id}`);
  return (
    <Section title="Relative orientation result" meta={`AM ${fmt(rec.summary.am)}`}>
      <RelOrientView s={rec.summary} names={names} tableUrl={rec.summary.table?.file ? relativeOrientationTableUrl(dataset.id) : null} />
    </Section>
  );
}

export function RelOrientView({ s: r, names, tableUrl }: { s: RelOrientSummary; names: string[]; tableUrl: string | null }) {
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
        {cat && r.bootstrap_am_16_84 ? (<><dt>Bootstrap 16–84%</dt><dd className="mono">{fmt(r.bootstrap_am_16_84[0])} to {fmt(r.bootstrap_am_16_84[1])}</dd></>) : null}
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
const HH = 180;
const HM = { l: 34, r: 8, t: 10, b: 40 };

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
            <text className={s.tick} x={HM.l - 4} y={py(t) + 3} textAnchor="end">{t.toFixed(2)}</text>
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
        {[0, 45, 90].map((d) => <text key={d} className={s.tick} x={HM.l + (d / 90) * (HW - HM.l - HM.r)} y={HH - HM.b + 12} textAnchor={d === 0 ? "start" : d === 90 ? "end" : "middle"}>{d}°</text>)}
        <text className={s.tick} x={(HW + HM.l) / 2} y={HH - 4} textAnchor="middle">|Δψ|: 0° parallel, 90° perpendicular</text>
      </svg>
      <figcaption className={s.note}>
        Bars: share of {r.mode === "map_catalog" ? "sources" : "pixels"} per bin ({k} equal-width bins of {90 / k}°).{band ? " Whiskers: the null's 2.5–97.5% range per bin; dot: its median." : " No null was run."}
      </figcaption>
    </figure>
  );
}

const SW = 320;

/** Map vs catalog: the map's orientation (solid) and the catalog's (dashed, outlined) at each source, in the map's pixel frame, over
    the map's outline. Not the map raster: the figure is self-contained. North and east depend on the map's WCS, so the axes are pixels. */
export function SegmentsFigure({ s: r }: { s: RelOrientSummary }) {
  const g = r.segments!;
  const nx = (r.map as { nx?: number }).nx ?? 1;
  const ny = (r.map as { ny?: number }).ny ?? 1;
  const pad = 8;
  const scale = (SW - 2 * pad) / Math.max(nx, ny);
  const w = nx * scale + 2 * pad;
  const h = ny * scale + 2 * pad;
  const len = Math.max(8, Math.min(40, (SW / Math.sqrt(Math.max(1, g.x.length))) * 0.5));
  const X = (x: number) => pad + (x + 0.5) * scale;
  const Y = (y: number) => h - pad - (y + 0.5) * scale;                   // FITS y up
  // the catalog's segment is the longer one, so it shows past the map's when the two agree
  const seg = (x: number, y: number, deg: number, f = 1) => {
    const a = (deg * Math.PI) / 180;
    const dx = (Math.cos(a) * len * f) / 2;
    const dy = (Math.sin(a) * len * f) / 2;
    return { x1: X(x) - dx, y1: Y(y) + dy, x2: X(x) + dx, y2: Y(y) - dy };
  };
  return (
    <figure className={s.fig}>
      <svg className={s.svg} viewBox={`0 0 ${w.toFixed(1)} ${h.toFixed(1)}`} role="img" aria-label={`Orientations at ${g.x.length} sources over the map outline: map solid, catalog dashed`}>
        <rect className={s.outline} x={pad} y={pad} width={nx * scale} height={ny * scale} />
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
      </svg>
      <figcaption className={s.note}>
        <span className={s.keyMap} aria-hidden="true" /> map, averaged in the disk · <span className={s.keyOther} aria-hidden="true" /> catalog · faded: not used (see the table for the reason).
        Pixel frame of the map, FITS y up; segment lengths are fixed (the catalog's is drawn longer so both show).{g.truncated ? " Only the first sources are drawn; the table has all of them." : ""}
      </figcaption>
    </figure>
  );
}
