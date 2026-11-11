import type { SameContentMatch } from "../api/catalog";
import { Button } from "../ui";
import s from "./LocationsPanel.module.css";

export type Offer = { path: string; matches: SameContentMatch[] };

const RELATION_TEXT: Record<SameContentMatch["relation"], string> = {
  verified: "verified same bytes (equal full SHA-256)",
  probable: "probably the same (equal size and sampled fingerprint); not verified",
};

/**
 * Shown instead of registering when a registered dataset already holds this file's content (ADR 0053): add the file as
 * another location of that dataset, or register it as its own dataset anyway. Nothing was registered yet.
 */
export function SameContentOffer({ offer, onAdd, onSeparate, onDismiss }: { offer: Offer; onAdd: (datasetId: number) => void; onSeparate: () => void; onDismiss: () => void }) {
  return (
    <div role="status" className={s.notice} aria-label="A dataset already holds this content">
      <p className={s.noticeText}>
        <span className="mono">{offer.path.split("/").pop()}</span> looks like a file you already registered. Nothing was registered yet.
      </p>
      {offer.matches.map((m) => (
        <div key={m.datasetId} className={s.noticeRow}>
          <p className={s.noticeText}>
            <strong>{m.name}</strong>: {RELATION_TEXT[m.relation]}.
          </p>
          <Button type="button" onClick={() => onAdd(m.datasetId)}>Add as a location of {m.name}</Button>
        </div>
      ))}
      <div className={s.noticeRow}>
        <Button type="button" onClick={onSeparate}>Register as a separate dataset</Button>
        <Button type="button" onClick={onDismiss}>Cancel</Button>
      </div>
    </div>
  );
}
