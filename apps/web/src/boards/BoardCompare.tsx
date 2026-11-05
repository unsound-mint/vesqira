import type { ReactNode } from "react";
import { Button } from "../ui";
import type { Figure } from "../api/boards";
import { bytes } from "../catalog/format";
import { metricRows, paramDiff } from "../compare/diff";
import s from "./BoardCompare.module.css";

function code(f: Figure): ReactNode {
  const r = f.provenance.run;
  if (!r) return "no run recorded";
  if (!r.gitCommit) return "no commit recorded";
  return (
    <>
      <span className="mono">{r.gitCommit.slice(0, 12)}{r.dirty ? "-dirty" : ""}</span>
      {r.reproducibleFromCommit ? null : <span className={s.flag}> not reproducible from the commit</span>}
    </>
  );
}

/** Same name and size is a reason to suspect the same input, not a proof; identity is never claimed from them. */
function inputVerdict(a: Figure, b: Figure): string {
  const x = a.provenance.source;
  const y = b.provenance.source;
  const same = x.name === y.name && x.sizeBytes === y.sizeBytes;
  return same ? "Same file name and size; that suggests the same input but the files were not compared byte for byte." : "The inputs differ in name or size.";
}

/** "What differs" between two board panels, built only from what each figure's run declared. Nothing here is computed from data. */
export function BoardCompare({ a, b, onCompare }: { a: Figure; b: Figure; onCompare?: (datasetIdA: number, datasetIdB: number) => void }) {
  const ra = a.provenance.run;
  const rb = b.provenance.run;
  const both = ra && rb;
  const diffs = both ? paramDiff(ra.params, rb.params) : [];
  const results = both ? metricRows(ra.metrics ?? [], rb.metrics ?? []) : [];
  const na = ra?.name ?? a.title;
  const nb = rb?.name ?? b.title;
  const ca = a.provenance.view.channel;
  const cb = b.provenance.view.channel;
  const sa = a.provenance.source;
  const sb = b.provenance.source;
  const channel = (c: number | undefined) => (c === undefined ? "not stated" : String(c + 1));
  return (
    <section className={s.wrap} aria-label="What differs">
      <h3 className={s.h}>Compare · {na} vs {nb}</h3>
      {!ra ? <p className={s.note}>No run is recorded for {a.title}.</p> : null}
      {!rb ? <p className={s.note}>No run is recorded for {b.title}.</p> : null}
      {both ? (
        diffs.length ? (
          <>
            <h4 className={s.h2}>Settings that differ ({diffs.length})</h4>
            <table className={s.table}>
              <thead><tr><th scope="col">Setting</th><th scope="col">{na}</th><th scope="col">{nb}</th></tr></thead>
              <tbody>{diffs.map((d) => <tr key={d.key}><th scope="row">{d.key}</th><td className="mono">{d.a}</td><td className="mono">{d.b}</td></tr>)}</tbody>
            </table>
          </>
        ) : (
          <p className={s.note}>No recorded setting differs.</p>
        )
      ) : null}
      <table className={s.table}>
        <thead><tr><th scope="col">Aspect</th><th scope="col">{na}</th><th scope="col">{nb}</th></tr></thead>
        <tbody>
          <tr><th scope="row">Code</th><td>{code(a)}</td><td>{code(b)}</td></tr>
          <tr><th scope="row">Input</th><td className="mono">{sa.name}, {bytes(sa.sizeBytes)}</td><td className="mono">{sb.name}, {bytes(sb.sizeBytes)}</td></tr>
          {ca !== cb ? <tr><th scope="row">View</th><td>channel {channel(ca)}</td><td>channel {channel(cb)}</td></tr> : null}
        </tbody>
      </table>
      <p className={s.note}>{inputVerdict(a, b)}</p>
      {ca !== cb ? <p className={s.note}>The panels show different channels: that is a difference in the view, not in the runs.</p> : null}
      {results.length > 0 ? (
        <>
          <h4 className={s.h2}>Results ({results.length})</h4>
          <table className={s.table}>
            <thead><tr><th scope="col">Result</th><th scope="col">{na}</th><th scope="col">{nb}</th><th scope="col">Comparable?</th></tr></thead>
            <tbody>{results.map((r) => <tr key={r.key}><th scope="row">{r.key}</th><td className="mono">{r.a}</td><td className="mono">{r.b}</td><td>{r.basis}</td></tr>)}</tbody>
          </table>
        </>
      ) : null}
      <p className={s.note}>As declared by the runs; Vesqira did not compute or check these numbers.</p>
      {onCompare ? <div><Button type="button" onClick={() => onCompare(a.datasetId, b.datasetId)}>Open both maps side by side</Button></div> : null}
    </section>
  );
}
