import type { NewerNotice } from "../api/catalog";
import { Button } from "../ui";
import { noticeText } from "./identity";
import s from "./LocationsPanel.module.css";

/**
 * "A newer result is available", "this file changed", "the data behind this figure changed" (ADR 0053). A notice only:
 * the dataset keeps pointing at what it pointed at and a figure keeps its picture.
 */
export function NewerNotices({ notices, onOpen, onRegister }: { notices: NewerNotice[] | undefined; onOpen?: (datasetId: number) => void; onRegister?: (path: string) => void }) {
  if (!notices || notices.length === 0) return null;
  return (
    <div role="status" className={s.notice} aria-label="A newer result is available">
      {notices.map((n, i) => (
        <div key={`${n.reason}-${n.name}-${i}`} className={s.noticeRow}>
          <p className={s.noticeText}>{noticeText(n)}</p>
          {n.reason === "newer_file" && n.datasetId !== 0 && onOpen ? <Button type="button" onClick={() => onOpen(n.datasetId)}>Open {n.name}</Button> : null}
          {n.reason === "newer_file" && n.datasetId === 0 && onRegister ? <Button type="button" onClick={() => onRegister(n.path)}>Register {n.name}</Button> : null}
        </div>
      ))}
    </div>
  );
}
