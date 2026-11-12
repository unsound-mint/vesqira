import { useCallback, useEffect, useState } from "react";
import { ACCESS_NAME, getDatasetAccess, setDatasetAccess } from "../api/access";
import type { AccessLabel, DatasetAccess } from "../api/access";
import { AccessBadge, Button, Input, Select } from "../ui";
import { useCan } from "../account/permissions";
import s from "./AnalysisPanel.module.css";

const ORDER: AccessLabel[] = ["none", "collaboration", "unreleased"];

/**
 * The access label of one dataset (ADR 0069): what you declare about showing it to others. Figures and boards made from it carry
 * the most restrictive label among their sources, and publishing a board that uses a labelled dataset asks you to confirm. The
 * label is your statement; Vesqira cannot check it against the file.
 */
export function AccessPanel({ id, onChanged }: { id: number; onChanged?: () => void }) {
  const can = useCan();
  const [acc, setAcc] = useState<DatasetAccess | null>(null);
  const [label, setLabel] = useState<AccessLabel>("none");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    getDatasetAccess(id).then((a) => { setAcc(a); setLabel(a.label); }).catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not read the label."));
  }, [id]);
  useEffect(() => {
    setAcc(null);
    setErr(null);
    setReason("");
    load();
  }, [load]);

  if (!acc) return err ? <p role="alert" className={s.note}>{err}</p> : <p className={s.note}>Reading the label…</p>;
  const writable = can("dataset.write");
  const rank = (l: AccessLabel) => ORDER.indexOf(l);
  const lowering = rank(label) < rank(acc.label);
  const blocked = label !== "none" && acc.blockedLabels.includes(label);

  async function save() {
    setBusy(true);
    setErr(null);
    try {
      const a = await setDatasetAccess(id, label, reason.trim());
      setAcc(a);
      setLabel(a.label);
      setReason("");
      onChanged?.();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not set the label.");
    } finally {
      setBusy(false);
    }
  }

  const inherited = acc.effective.label !== "none" && rank(acc.effective.label) > rank(acc.label);
  return (
    <div className={s.wrap}>
      <p className={s.note}>
        {acc.label === "none" ? "Released: no label." : <AccessBadge label={acc.label} />}
        {inherited ? <> This dataset also carries <strong>{ACCESS_NAME[acc.effective.label]}</strong> from {(acc.effective.via ?? []).map((v) => (v.how ? `${v.name} (${v.how})` : v.name)).join("; ")}, which it was made from.</> : null}
      </p>
      {writable ? (
        <>
          <Select label="Access label" value={label} onChange={(e) => setLabel(e.target.value as AccessLabel)}>
            <option value="none">Released (no label)</option>
            <option value="collaboration">Collaboration only</option>
            <option value="unreleased">Unreleased</option>
          </Select>
          <Input label="Reason (kept in the history)" value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
          {lowering && !acc.canLower ? <p className={s.note}>Making a dataset less restricted needs the Maintainer role or higher.</p> : null}
          {blocked ? <p className={s.note}>This workspace&rsquo;s admin has made &ldquo;{ACCESS_NAME[label]}&rdquo; block publishing: boards using this dataset cannot be published here and work using it cannot be pushed here.</p> : null}
          <div><Button disabled={busy || label === acc.label || (lowering && !acc.canLower)} onClick={() => void save()}>Set label</Button></div>
        </>
      ) : <p className={s.note}>You can read this label but not change it.</p>}
      {err ? <p role="alert" className={s.note}>{err}</p> : null}
      <p className={s.note}>A label is what you declare, not something checked in the file. A reviewer never receives the data, only the pictures you publish, but a picture of unreleased data shows it.</p>
      {acc.history.length > 0 ? (
        <details>
          <summary className={s.note}>History ({acc.history.length})</summary>
          <ul className={s.note} style={{ margin: 0, paddingLeft: 16 }}>
            {acc.history.map((h) => <li key={h.id}>{new Date(h.at).toLocaleString()}: {ACCESS_NAME[h.previous]} to {ACCESS_NAME[h.label]}{h.reason ? `, “${h.reason}”` : ""}</li>)}
          </ul>
        </details>
      ) : null}
    </div>
  );
}
