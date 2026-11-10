import { useEffect, useRef, useState } from "react";
import { AlertTriangle, X } from "lucide-react";
import * as api from "../api/reports";
import type { Preflight, ReportView } from "../api/reports";
import { Button, Icon } from "../ui";
import s from "./ReportsView.module.css";

/**
 * Exactly what a publication sends, before it is sent: how many parts, pictures and runs, what changed since the last
 * published revision, links readers cannot open, and who can read it. Publishing names the revision previewed here,
 * so a save in between (by the agent, say) makes it fail instead of sending unseen text.
 */
export function PublishDialog({ report, onClose, onPublished, onSetup }: { report: ReportView; onClose: () => void; onPublished: (n: number) => void; onSetup: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const [pf, setPf] = useState<Preflight | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<number | null>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal?.();
    api.preflightReport(report.id).then(setPf).catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not prepare the preview."));
  }, [report.id]);
  const blocks = pf?.revision.blocks ?? [];
  const pics = blocks.filter((b) => b.kind === "figure").length;
  const runs = blocks.filter((b) => b.kind === "run").length;
  const added = pf?.changes.filter((c) => c.status === "new").length ?? 0;
  const removed = pf?.changes.filter((c) => c.status === "removed").length ?? 0;
  const first = (report.publications.length ?? 0) === 0;
  const notSetUp = pf?.errors.some((e) => e.toLowerCase().startsWith("no publish server")) ?? false;

  async function go() {
    if (!pf) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await api.publishReport(report.id, pf.workingRevision);
      setDone(r.number);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Publishing failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <dialog ref={ref} className={s.dialog} onClose={onClose} aria-labelledby="publish-title">
      <div className={s.dialogHead}>
        <h2 id="publish-title">{done ? "Published" : `Publish “${report.title}”`}</h2>
        <button type="button" className={s.iconBtn} onClick={onClose} aria-label="Close"><Icon icon={X} /></button>
      </div>
      {done ? (
        <div className={s.dialogBody}>
          <p>Revision {done} is live. {pf?.readers?.length ? `${pf.readers.join(", ")} will see it the next time they open their link.` : "Nobody has a link yet: make one under Share."}</p>
          <div className={s.dialogActions}><Button variant="primary" onClick={() => onPublished(done)}>Done</Button></div>
        </div>
      ) : !pf ? (
        <div className={s.dialogBody}>{err ? <p className={s.error} role="alert">{err}</p> : <p className={s.muted}>Preparing the preview…</p>}</div>
      ) : (
        <div className={s.dialogBody}>
          <dl className={s.facts}>
            <div><dt>Readers get</dt><dd>{blocks.length} parts{pics ? `, ${pics} figure${pics === 1 ? "" : "s"} as pictures` : ""}{runs ? `, ${runs} run summar${runs === 1 ? "y" : "ies"}` : ""}. No data, paths, hosts or scripts.</dd></div>
            <div><dt>Since last time</dt><dd>{first ? "First publication." : added || removed ? `${added} new or changed part${added === 1 ? "" : "s"}${removed ? `, ${removed} removed` : ""}.` : "Nothing changed."}</dd></div>
            <div><dt>Attribution</dt><dd>{pf.revision.authors}</dd></div>
            <div><dt>Who can read</dt><dd>{pf.readers?.length ? pf.readers.join(", ") : "Nobody yet"}</dd></div>
          </dl>
          {pf.errors.length ? (
            <ul className={s.problems}>
              {pf.errors.map((e) => <li key={e} data-tone="err"><Icon icon={AlertTriangle} />{e}</li>)}
            </ul>
          ) : null}
          {pf.warnings.length ? (
            <ul className={s.problems}>
              {pf.warnings.map((w) => <li key={w}><Icon icon={AlertTriangle} />{w}</li>)}
            </ul>
          ) : null}
          {err ? <p className={s.error} role="alert">{err}</p> : null}
          <div className={s.dialogActions}>
            {notSetUp ? <Button onClick={onSetup}>Set up publishing</Button> : null}
            <span className={s.grow} />
            <button type="button" className={s.ghost} onClick={onClose}>Cancel</button>
            <Button variant="primary" disabled={busy || pf.errors.length > 0} onClick={() => void go()}>{busy ? "Publishing…" : `Publish revision ${pf.revision.number}`}</Button>
          </div>
        </div>
      )}
    </dialog>
  );
}
