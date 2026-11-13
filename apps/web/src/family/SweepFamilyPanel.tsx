import { useEffect, useState } from "react";
import { Button, Select } from "../ui";
import { sweepFamily } from "../api/family";
import type { SweepFamily } from "../api/family";
import type { FamilyPreset } from "./FamilyView";
import s from "./Family.module.css";

/** A sweep's finished jobs as a family (ADR 0070): which output to compare, which jobs contribute a map, and which are left out and why. */
export function SweepFamilyPanel({ sweepId, onOpen }: { sweepId: number; onOpen: (p: FamilyPreset) => void }) {
  const [role, setRole] = useState<string | undefined>(undefined);
  const [fam, setFam] = useState<SweepFamily | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    setErr(null);
    sweepFamily(sweepId, role, ac.signal).then(setFam).catch((e) => { if (!ac.signal.aborted) setErr(e instanceof Error ? e.message : "Could not read the outputs of this sweep."); });
    return () => ac.abort();
  }, [sweepId, role]);

  if (err) return <p role="alert" className={s.err}>{err}</p>;
  if (!fam) return <p className={s.note} role="status">Reading the outputs of the sweep…</p>;
  const labels: Record<number, string> = {};
  for (const m of fam.members) labels[m.datasetId] = m.label;
  const open = () => onOpen({ nonce: Date.now(), ids: fam.members.map((m) => m.datasetId), labels, referenceId: fam.referenceId || undefined, title: `Sweep ${fam.name} as a family`, excluded: fam.excluded.map((x) => ({ label: x.label, reason: x.reason })) });
  return (
    <section aria-label="Outputs as a family" className={s.block}>
      <h3 className={s.h3}>Compare the outputs as a family</h3>
      {fam.roles.length > 1 ? (
        <Select label="Which output" value={fam.role} onChange={(e) => setRole(e.target.value || undefined)}>
          <option value="">Choose…</option>
          {fam.roles.map((r) => <option key={r.role} value={r.role}>{`${r.role} (${r.jobs} jobs)`}</option>)}
        </Select>
      ) : null}
      {fam.note ? <p className={s.note} role="status">{fam.note}</p> : null}
      {fam.members.length > 0 ? (
        <ul className={s.cands} aria-label="Jobs compared">
          {fam.members.map((m) => <li key={m.jobId}><strong>{m.label}</strong> <span className={s.note}>{m.name}</span></li>)}
        </ul>
      ) : null}
      {fam.excluded.length > 0 ? (
        <>
          <h4 className={s.h3}>Left out, not compared</h4>
          <ul className={s.cands} aria-label="Jobs left out">
            {fam.excluded.map((x) => <li key={x.jobId}><strong>{x.label}</strong> <span className={s.note}>{x.reason}</span></li>)}
          </ul>
        </>
      ) : null}
      <div className={s.row}>
        <Button variant="primary" disabled={fam.members.length < 2} onClick={open}>{`Open ${fam.members.length} maps in the family view`}</Button>
      </div>
    </section>
  );
}
