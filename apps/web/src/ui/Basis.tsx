import type { ReactNode } from "react";
import s from "./Basis.module.css";

/**
 * The first clause of a long provenance sentence: up to the first "; " (or the end), cut at a word near `max` characters.
 * The whole sentence is still shown in the disclosure, so nothing is dropped, only moved one click away.
 */
export function leadClause(text: string, max = 96): string {
  const cut = text.indexOf("; ");
  const first = cut > 0 ? text.slice(0, cut) : text.replace(/\.$/, "");
  if (first.length <= max) return first.replace(/[.,:]$/, "");
  const room = first.slice(0, max);
  const at = room.lastIndexOf(" ");
  return `${room.slice(0, at > max / 2 ? at : max).replace(/[\s,;:(]+$/, "")}…`;
}

/**
 * A one-line summary with the long text behind a "Basis" disclosure: how a number or a placement was obtained (provenance,
 * conventions, caveats) is always one click away and never crowds the line the eye needs. Keyboard and screen readers get
 * the native <details>. Without `children` it is just the line.
 */
export function Basis({ summary, label = "Basis", tone = "note", role, children }: { summary: ReactNode; label?: string; tone?: "note" | "warn"; role?: "alert" | "status" | "note"; children?: ReactNode }) {
  return (
    <div className={s.basis}>
      <p className={tone === "warn" ? s.warn : s.summary} role={role}>{summary}</p>
      {children ? (
        <details className={s.details}>
          <summary className={s.toggle}>{label}</summary>
          <div className={s.body}>{children}</div>
        </details>
      ) : null}
    </div>
  );
}
