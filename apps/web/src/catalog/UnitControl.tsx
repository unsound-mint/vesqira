import { useState, type FormEvent } from "react";
import { ruleOf } from "../api/declared";
import type { Dataset } from "../api/catalog";
import { Button, Input } from "../ui";
import { TierBadge } from "../ui/TierBadge";
import { fileStatesItsUnit } from "./unit";
import s from "./AnalysisPanel.module.css";

/**
 * The unit of the values when the file does not say. A file that states its own BUNIT shows nothing here (it cannot be given
 * another), and neither does an orientation map (its values are angles). Otherwise the unit is what you declare: kept as typed, shown everywhere as declared by you and never as the
 * file's, and it does not change the file. Withdrawing it brings back "unresolved".
 */
export function UnitControl({ dataset, onDeclare, onWithdraw }: {
  dataset: Dataset;
  onDeclare: (id: number, unit: string) => Promise<string | null>;
  onWithdraw: (id: number) => Promise<string | null>;
}) {
  const [text, setText] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (fileStatesItsUnit(dataset) || dataset.interpretation) return null;
  const d = dataset.unitDeclaration;

  async function submit(e: FormEvent) {
    e.preventDefault();
    const unit = text.trim();
    if (!unit || busy) return;
    setBusy(true);
    const r = await onDeclare(dataset.id, unit);
    setBusy(false);
    setErr(r);
    if (r === null) setText("");
  }
  async function confirmAgain(unit: string) {
    setBusy(true);
    setErr(await onDeclare(dataset.id, unit));
    setBusy(false);
  }
  async function withdraw() {
    setBusy(true);
    setErr(await onWithdraw(dataset.id));
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      {d ? (
        <>
          <dl className={s.facts}>
            <dt>Unit</dt>
            <dd className="mono">{d.unit}</dd>
            <dt>Source</dt>
            <dd>{d.declaredBy === "user" ? "declared by you" : ruleOf(d.declaredBy) !== null ? `declared by your rule ${ruleOf(d.declaredBy)}` : `proposed by ${d.declaredBy}`}, {new Date(d.declaredAt).toLocaleDateString()} <TierBadge tier={d.tier} mappingVersion={d.mappingVersion} rule={d.declaredBy === "user" || ruleOf(d.declaredBy) !== null ? null : d.declaredBy} /></dd>
          </dl>
          <p className={s.note}>{ruleOf(d.declaredBy) !== null ? `The file has no BUNIT. This unit was declared by your import rule ${ruleOf(d.declaredBy)}, not by you and not by the file; declare one below to replace it.` : "The file has no BUNIT. This is your declaration, kept apart from the file, which is not changed."}</p>
          {d.stale ? (
            <p role="status" className={s.note}>The file has changed since you declared this unit, so it is not being used. Confirm that it still applies, or withdraw it.</p>
          ) : null}
          <div className={s.two}>
            {d.stale ? <Button type="button" variant="primary" disabled={busy} onClick={() => void confirmAgain(d.unit)}>It still applies</Button> : null}
            <Button type="button" disabled={busy} onClick={() => void withdraw()}>Withdraw the unit</Button>
          </div>
        </>
      ) : (
        <form className={s.wrap} onSubmit={(e) => void submit(e)}>
          <p className={s.note}>The file has no BUNIT, so its values have no unit. Vesqira does not guess one.</p>
          <Input label="Declare the unit" value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} maxLength={40} autoComplete="off" aria-describedby={`unit-help-${dataset.id}`} />
          <p id={`unit-help-${dataset.id}`} className={s.note}>Kept exactly as typed and shown as declared by you, never as the file's. The file is not changed.</p>
          <div><Button type="submit" variant="primary" disabled={busy || !text.trim()}>Declare</Button></div>
        </form>
      )}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
