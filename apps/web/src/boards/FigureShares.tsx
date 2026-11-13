import { useEffect, useState } from "react";
import { AccessBadge, Button } from "../ui";
import { listAllFigureShares, revokeSnapshot } from "../api/boards";
import type { Snapshot } from "../api/boards";
import s from "./BoardsView.module.css";

const status = (sn: Snapshot) => (sn.revoked ? "Revoked" : new Date(sn.expiresAt) < new Date() ? "Expired" : "Active");

/**
 * Links made by sharing one figure on its own (ADR 0071), listed where board links are: the author finds each one, sees whether it
 * is active and whether the reviewer commented, and revokes it. Nothing here can show a link again: it is shown once, when made.
 */
export function FigureShares({ refreshKey }: { refreshKey: number }) {
  const [shares, setShares] = useState<Snapshot[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    listAllFigureShares().then((l) => live && setShares(Array.isArray(l) ? l : [])).catch(() => live && setShares([]));
    return () => {
      live = false;
    };
  }, [refreshKey]);

  if (shares.length === 0) return null;
  return (
    <section className={s.share} aria-label="Shared figures">
      <h3 className={s.h}>Shared figures</h3>
      <p className={s.note}>Figures shared on their own, without a board. Revoking ends the link at once; the figure stays.</p>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <ul className={s.snaps}>
        {shares.map((sn) => (
          <li key={sn.id}>
            <span>{sn.title || `Figure ${sn.id}`}</span>{sn.label ? ` · ${sn.label}` : ""} {sn.access ? <AccessBadge label={sn.access} /> : null}{" "}
            <span className={s.src}>· {sn.comments} comment{sn.comments === 1 ? "" : "s"} · {status(sn)} · expires {new Date(sn.expiresAt).toLocaleDateString()}</span>
            <div className={s.row}>
              <Button
                disabled={sn.revoked}
                aria-label={`Revoke the link to ${sn.title || `figure ${sn.id}`}${sn.label ? `, ${sn.label}` : ""}`}
                onClick={() => void revokeSnapshot(sn.id).then(() => listAllFigureShares()).then(setShares).then(() => setErr(null)).catch((e: Error) => setErr(e.message))}
              >
                Revoke link
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
