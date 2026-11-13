import { useCallback, useEffect, useState } from "react";
import { rerunCheck } from "../api/jobs";
import type { Job } from "../api/jobs";
import type { RerunCheck as Check } from "../api/runs";
import { EM } from "../catalog/format";
import { Button, Pill } from "../ui";
import { MODE_TEXT } from "../runs/ParentComparison";
import s from "./RerunCheck.module.css";

const RESULT: Record<string, { text: string; tone: "ok" | "warn" | "neutral" }> = {
  matched: { text: "matched", tone: "ok" },
  differed: { text: "differs", tone: "warn" },
  unknown: { text: "not comparable", tone: "neutral" },
};

/** Before a rerun draft is submitted: how it relates to its parent run, what the host's code folder looks like now compared with what the parent observed, and which settings changed. Nothing here submits or changes anything. */
export function RerunCheck({ job }: { job: Job }) {
  const [chk, setChk] = useState<Check | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    setBusy(true);
    setErr(null);
    rerunCheck(job.id)
      .then(setChk)
      .catch((e: unknown) => { setChk(null); setErr(e instanceof Error ? e.message : "Could not check."); })
      .finally(() => setBusy(false));
  }, [job.id]);
  useEffect(() => { setChk(null); load(); }, [load]);

  if (!chk && !err) return busy ? <p className={s.note} role="status">Checking the code folder against the parent run…</p> : null;
  if (!chk) return <p className={s.note} role="note">The rerun check is not available: {err}</p>;
  const mode = MODE_TEXT[chk.mode];
  return (
    <section className={s.box} aria-label="Rerun check">
      <p className={s.lead}><strong>{mode.name} rerun</strong> of an earlier run: {mode.what}.</p>
      {chk.mode === "strict" && chk.blocking ? (
        <div role="alert" className={s.warn}>
          <p>A strict rerun would be refused now, and nothing would be sent:</p>
          <ul>{chk.messages.map((m) => <li key={m}>{m}</li>)}</ul>
        </div>
      ) : null}
      {chk.mode === "best_effort" && chk.messages.length > 0 ? (
        <div role="note" className={s.warn}>
          <p>Differences from the parent run (the job still runs):</p>
          <ul>{chk.messages.map((m) => <li key={m}>{m}</li>)}</ul>
        </div>
      ) : null}
      <p className={s.lead}><strong>Code commit.</strong> {chk.codePin.why}</p>
      {chk.mode !== "adapted" && chk.codeChecked && chk.messages.length === 0 ? <p className={s.lead}>No difference found in what could be read before the job runs.</p> : null}
      {chk.differences.length > 0 ? (
        <div className={s.scroll}>
          <table className={s.table}>
            <thead><tr><th scope="col">Fact</th><th scope="col">Compared</th><th scope="col">Parent observed</th><th scope="col">Now on the host</th></tr></thead>
            <tbody>
              {chk.differences.map((d) => (
                <tr key={d.key}>
                  <th scope="row">{d.label}</th>
                  <td><Pill tone={RESULT[d.result]?.tone ?? "neutral"}>{RESULT[d.result]?.text ?? d.result}</Pill>{d.why ? <span className={s.src}> {d.why}</span> : null}</td>
                  <td className="mono">{d.parent ? d.parent.slice(0, 20) : EM}</td>
                  <td className="mono">{d.this ? d.this.slice(0, 20) : EM}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      {chk.settingsKnown ? (
        chk.settings.length > 0 ? (
          <div>
            <p className={s.lead}>{chk.settings.length} setting{chk.settings.length === 1 ? "" : "s"} changed from the parent run:</p>
            <ul className={s.list}>
              {chk.settings.map((c) => (
                <li key={c.key}><span className="mono">{c.key}</span>: <span className="mono">{c.parent ?? EM}</span> to <span className="mono">{c.now ?? EM}</span></li>
              ))}
            </ul>
          </div>
        ) : (
          <p className={s.lead}>No setting changed from the parent run.</p>
        )
      ) : null}
      {chk.notes.map((n) => <p key={n} className={s.src}>{n}</p>)}
      <div className={s.row}>
        <Button type="button" onClick={load} disabled={busy}>{busy ? "Checking…" : "Check again"}</Button>
      </div>
    </section>
  );
}
