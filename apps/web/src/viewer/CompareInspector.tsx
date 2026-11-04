import { useState } from "react";
import { Button } from "../ui";
import type { Dataset, Compat } from "../api/catalog";
import type { Run, RunParam } from "../api/runs";
import { metricText } from "../api/metrics";
import type { Metric } from "../api/metrics";
import { runAnalysis } from "../api/analysis";
import type { Viewer } from "./useViewer";
import { runFor } from "../catalog/RunPanel";
import { EM } from "../catalog/format";
import { beamText, pixelScale } from "../catalog/resolution";
import s from "./CompareInspector.module.css";

export type ParamDiff = { key: string; a: string; b: string };

/** Parameters recorded differently by two runs (or recorded by only one). Equal settings are not listed. */
export function paramDiff(a: RunParam[], b: RunParam[]): ParamDiff[] {
  const ma = new Map(a.map((p) => [p.key, p.value]));
  const mb = new Map(b.map((p) => [p.key, p.value]));
  const keys = [...new Set([...ma.keys(), ...mb.keys()])].sort();
  return keys.filter((k) => ma.get(k) !== mb.get(k)).map((k) => ({ key: k, a: ma.get(k) ?? EM, b: mb.get(k) ?? EM }));
}

export type MetricRow = { key: string; a: string; b: string; basis: string };

/**
 * The results two runs report, side by side. Whether two numbers may be compared depends on what they measure, so each row
 * says if both runs gave the same definition; the same name with different or missing definitions is not offered as comparable.
 */
export function metricRows(a: Metric[], b: Metric[]): MetricRow[] {
  const ma = new Map(a.map((m) => [m.key, m]));
  const mb = new Map(b.map((m) => [m.key, m]));
  return [...new Set([...ma.keys(), ...mb.keys()])].sort().map((key) => {
    const x = ma.get(key);
    const y = mb.get(key);
    let basis = "only one run reports it";
    if (x && y) {
      if (!x.definition || !y.definition) basis = "definition not stated by both: not shown as comparable";
      else if (x.definition !== y.definition) basis = "definitions differ: not directly comparable";
      else if (x.unit !== y.unit) basis = "units differ: not directly comparable";
      else basis = "same definition and unit";
    }
    return { key, a: x ? metricText(x) : EM, b: y ? metricText(y) : EM, basis };
  });
}

const kind = (d: Dataset) => (d.interpretation ? `axial orientation, ${d.interpretation.unit === "deg" ? "degrees" : "radians"} (${d.interpretation.declaredBy === "user" ? "declared by you" : "proposed by the run import"})` : "scalar");
const commit = (r: Run | undefined) => (r ? (r.gitCommit ? r.gitCommit.slice(0, 12) + (r.dirty ? " -dirty (not reproducible)" : "") : "no commit recorded") : "not from an imported run");

/** What differs between two maps, and the explicit next steps (axial difference, alignment). Nothing is merged silently. */
export function CompareInspector({ a, b, va, vb, runs, compat, onCreated }: { a: Dataset; b: Dataset; va: Viewer; vb: Viewer; runs: Run[]; compat: Compat | null; onCreated: (d: Dataset) => void }) {
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const ra = runFor(runs, a.id)?.run;
  const rb = runFor(runs, b.id)?.run;
  const diffs = ra && rb ? paramDiff(ra.params, rb.params) : [];
  const results = ra && rb ? metricRows(ra.metrics, rb.metrics) : [];
  const pct = (v: Viewer) => (v.plane ? `${v.plane.head.stats.finite_count.toLocaleString("en-US")} of ${(v.plane.head.stats.finite_count + v.plane.head.stats.nonfinite_count).toLocaleString("en-US")}` : EM);
  const bothAxial = a.interpretation !== null && b.interpretation !== null;
  const sameMachine = a.hostId === 0 && b.hostId === 0;

  async function go(op: "axial_diff" | "scalar_diff" | "align", from: Dataset, to: Dataset) {
    setBusy(true);
    setErr(null);
    try {
      const r = await runAnalysis({ op, datasetId: from.id, referenceId: to.id, params: op === "align" ? { method: "interp" } : {} });
      if (r.dataset) onCreated(r.dataset);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    }
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      <table className={s.table}>
        <thead>
          <tr><th scope="col" /><th scope="col">{a.name}</th><th scope="col">{b.name}</th></tr>
        </thead>
        <tbody>
          <tr><th scope="row">Treated as</th><td>{kind(a)}</td><td>{kind(b)}</td></tr>
          <tr><th scope="row">Brightness unit</th><td className="mono">{va.view?.bunit ?? "unresolved"}</td><td className="mono">{vb.view?.bunit ?? "unresolved"}</td></tr>
          <tr><th scope="row">Pixel scale</th><td className="mono">{pixelScale(a)}</td><td className="mono">{pixelScale(b)}</td></tr>
          <tr><th scope="row">Beam (resolution)</th><td className="mono">{beamText(a)}</td><td className="mono">{beamText(b)}</td></tr>
          <tr><th scope="row">Run</th><td>{ra?.name ?? EM}</td><td>{rb?.name ?? EM}</td></tr>
          <tr><th scope="row">Code</th><td className="mono">{commit(ra)}</td><td className="mono">{commit(rb)}</td></tr>
          <tr><th scope="row">Channel shown</th><td className="mono">{va.plane ? `${va.plane.head.channel + 1} of ${va.plane.head.channels}` : EM}</td><td className="mono">{vb.plane ? `${vb.plane.head.channel + 1} of ${vb.plane.head.channels}` : EM}</td></tr>
          <tr><th scope="row">Pixels with a value</th><td className="mono">{pct(va)}</td><td className="mono">{pct(vb)}</td></tr>
        </tbody>
      </table>
      <p className={s.note}>A change in the number of valid pixels is a change in coverage, not better agreement.</p>
      {ra && rb ? (
        diffs.length ? (
          <details open>
            <summary>Run settings that differ ({diffs.length})</summary>
            <table className={s.table}>
              <tbody>{diffs.map((d) => <tr key={d.key}><th scope="row">{d.key}</th><td className="mono">{d.a}</td><td className="mono">{d.b}</td></tr>)}</tbody>
            </table>
          </details>
        ) : (
          <p className={s.note}>The two runs recorded the same settings.</p>
        )
      ) : (
        <p className={s.note}>Run settings are compared when both maps come from imported runs.</p>
      )}
      {results.length > 0 ? (
        <details open>
          <summary>Run results ({results.length})</summary>
          <table className={s.table}>
            <tbody>{results.map((r) => <tr key={r.key}><th scope="row">{r.key}</th><td className="mono">{r.a}</td><td className="mono">{r.b}</td><td>{r.basis}</td></tr>)}</tbody>
          </table>
          <p className={s.note}>Results are as declared by each run; a difference between them is not explained by this table.</p>
        </details>
      ) : null}
      <p className={s.note}>Grids: {compat ? (compat.compatible ? "the same, so pixels correspond one to one." : `not confirmed the same (${compat.reasons.length} reason${compat.reasons.length === 1 ? "" : "s"}); see the list above.`) : "checking…"}</p>
      {bothAxial && compat?.compatible && sameMachine ? (
        <div className={s.next}>
          <Button type="button" disabled={busy} onClick={() => go("axial_diff", a, b)}>Axial difference map ({a.name} − {b.name})</Button>
          <p className={s.note}>Taken modulo 180°: 1° and 179° differ by 2°, not 178°. Pixels without a value in either map stay NaN.</p>
        </div>
      ) : null}
      {!a.interpretation && !b.interpretation && compat?.compatible && sameMachine ? (
        <div className={s.next}>
          <Button type="button" disabled={busy} onClick={() => go("scalar_diff", a, b)}>Difference map ({a.name} − {b.name})</Button>
          <p className={s.note}>Needs the same declared brightness unit in both; nothing is converted. Pixels without a value in either map stay NaN, a real zero stays 0. The maps’ beams are not matched: if their resolutions differ, the difference mixes resolution with signal (beams: {beamText(a)}; {beamText(b)}).</p>
        </div>
      ) : null}
      {compat && !compat.compatible && sameMachine ? (
        <div className={s.next}>
          <Button type="button" disabled={busy} onClick={() => go("align", a, b)}>Propose: align {a.name} onto {b.name}’s grid</Button>
          <p className={s.note}>Creates a new file by interpolation. It does not match angular resolution, and pixels outside {a.name}’s footprint are NaN. Both originals stay unchanged.</p>
        </div>
      ) : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
