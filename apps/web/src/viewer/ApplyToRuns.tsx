import { useEffect, useState } from "react";
import { ApiError } from "../api/catalog";
import type { ApplyPreview, ApplyResult, ApplyRow, ApplyScopeName, ApplyUndoResult, Dataset } from "../api/catalog";
import { Basis, Button, Select } from "../ui";
import s from "./OrientationPanels.module.css";

export type ApplyActions = {
  preview: (id: number, scope?: ApplyScopeName) => Promise<ApplyPreview>;
  apply: (sourceId: number, scope: ApplyScopeName, targetIds: number[]) => Promise<ApplyResult>;
  undo: (applyId: string) => Promise<ApplyUndoResult>;
};

const errText = (e: unknown) => (e instanceof ApiError ? [e.message, ...e.reasons].join(" · ") : "Could not reach the server.");

/** Rows with the same reason are listed once with a count: hundreds of runs refuse for the same few reasons. */
export function reasonCounts(rows: ApplyRow[], outcome: ApplyRow["outcome"]): { reason: string; n: number }[] {
  const m = new Map<string, number>();
  for (const r of rows) if (r.outcome === outcome) m.set(r.reason ?? "", (m.get(r.reason ?? "") ?? 0) + 1);
  return [...m].map(([reason, n]) => ({ reason, n })).sort((a, b) => b.n - a.n);
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/**
 * "Apply to the other runs": copy a declaration the person made by hand to the same-role output of the other runs read with the same tool
 * profile. The preview comes from the catalog; the server checks every target again on confirm, never overwrites a declaration and never
 * overrides a file that states its own convention. Shown only for a declaration made directly by the user.
 */
export function ApplyToRuns({ dataset, actions }: { dataset: Dataset; actions: ApplyActions }) {
  const cur = dataset.interpretation;
  const direct = !!cur && cur.declaredBy === "user" && !cur.applyId && !cur.stale;
  const stamp = cur ? `${cur.unit}|${cur.frame ?? ""}|${cur.quantity ?? ""}|${cur.reliabilityId ?? ""}` : "";
  const [scope, setScope] = useState<ApplyScopeName | undefined>(undefined);
  const [preview, setPreview] = useState<ApplyPreview | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [result, setResult] = useState<ApplyResult | null>(null);
  const [undone, setUndone] = useState<number | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!direct) { setPreview(null); return; }
    let live = true;
    actions.preview(dataset.id, scope).then((p) => { if (live) setPreview(p); }, () => { if (live) setPreview(null); });
    return () => { live = false; };
  }, [actions, dataset.id, direct, stamp, scope, result]);

  if (!direct || !preview || !preview.eligible || preview.scopes.length === 0) return null;

  const declarable = preview.rows.filter((r) => r.outcome === "declare");
  const skipped = preview.counts.skipped ?? 0;
  const refused = preview.counts.refused ?? 0;
  const unit = cur!.unit === "deg" ? "degrees" : "radians";

  async function run() {
    setBusy(true);
    setMsg(null);
    try {
      const r = await actions.apply(dataset.id, preview!.scope as ApplyScopeName, declarable.map((x) => x.datasetId));
      setResult(r);
      setUndone(null);
      setConfirming(false);
    } catch (e) {
      setMsg(errText(e));
    }
    setBusy(false);
  }

  async function undo() {
    setBusy(true);
    setMsg(null);
    try {
      setUndone((await actions.undo(result!.applyId)).removed);
    } catch (e) {
      setMsg(errText(e));
    }
    setBusy(false);
  }

  const reasons = (rows: ApplyRow[], outcome: ApplyRow["outcome"]) => (
    <ul>{reasonCounts(rows, outcome).map((x) => <li key={x.reason}>{x.n} × {x.reason}</li>)}</ul>
  );

  return (
    <div className={s.wrap}>
      <Basis summary="Apply to the other runs">
        <p>
          Copy this declaration ({unit}{cur!.frame ? `, ${cur!.frame} frame` : ""}) to the orientation map of the other runs read with {preview.source?.profile ?? "the same tool profile"}.
          A map that is already declared is never changed, and a file whose own header states the convention keeps its own statement.
        </p>
      </Basis>
      {preview.scopes.length > 1 ? (
        <Select label="Which runs" value={preview.scope} onChange={(e) => { setScope(e.target.value as ApplyScopeName); setResult(null); setConfirming(false); }}>
          {preview.scopes.map((x) => <option key={x.scope} value={x.scope}>{x.label} ({plural(x.runs, "other run", "other runs")})</option>)}
        </Select>
      ) : <p className={s.note}>{preview.scopes[0]!.label}: {plural(preview.scopes[0]!.runs, "other run", "other runs")}.</p>}
      {result ? (
        <div role="status" className={s.wrap}>
          <p>
            Declared {result.counts.declared ?? 0}; skipped {result.counts.skipped ?? 0}; refused {result.counts.refused ?? 0}.
            {result.rows.some((r) => r.outcome === "declared" && !r.reliability) ? ` ${result.rows.filter((r) => r.outcome === "declared" && !r.reliability).length} were declared without a reliability map (their run has none, or not exactly one).` : ""}
          </p>
          {(result.counts.refused ?? 0) > 0 ? <><p className={s.note}>Refused:</p>{reasons(result.rows, "refused")}</> : null}
          {(result.counts.skipped ?? 0) > 0 ? <><p className={s.note}>Skipped:</p>{reasons(result.rows, "skipped")}</> : null}
          {undone === null ? (
            (result.counts.declared ?? 0) > 0 ? <Button type="button" disabled={busy} onClick={undo}>Undo this apply</Button> : null
          ) : (
            <p>Removed {plural(undone, "declaration", "declarations")}. A declaration someone edited since was left alone.</p>
          )}
        </div>
      ) : (
        <div className={s.wrap}>
          <p>
            Would declare {declarable.length}; skip {skipped}; refuse {refused}.
          </p>
          {skipped > 0 ? <><p className={s.note}>Skipped:</p>{reasons(preview.rows, "skipped")}</> : null}
          {refused > 0 ? <><p className={s.note}>Refused:</p>{reasons(preview.rows, "refused")}</> : null}
          {declarable.length > 0 ? (
            confirming ? (
              <>
                <p className={s.warn}>
                  This declares {plural(declarable.length, "map", "maps")} as axial orientation in {unit}, declared by you and applied from {preview.source?.name}; it is not verified.
                  {cur!.unit === "rad" ? " The values of each map are checked against radians now; a map beyond 2π is refused." : ""} Each run’s own reliability map is used where it has exactly one.
                </p>
                <Button type="button" variant="primary" disabled={busy} onClick={run}>Confirm: declare {declarable.length}</Button>
                <Button type="button" disabled={busy} onClick={() => setConfirming(false)}>Cancel</Button>
              </>
            ) : (
              <Button type="button" variant="primary" onClick={() => setConfirming(true)}>Apply to {plural(declarable.length, "run", "runs")}…</Button>
            )
          ) : <p className={s.note}>Nothing to apply.</p>}
        </div>
      )}
      {msg ? <p role="alert" className={s.err}>{msg}</p> : null}
    </div>
  );
}
