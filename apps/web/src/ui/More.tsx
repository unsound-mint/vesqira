import type { ReactNode } from "react";
import s from "./More.module.css";

/**
 * Advanced settings of a form behind one native disclosure, so the fields most people need and the button that submits them
 * stay in view. `open` starts it open when a setting inside is already changed from the default (it must not hide a choice).
 */
export function More({ summary = "More options", open, children }: { summary?: string; open?: boolean; children: ReactNode }) {
  return (
    <details className={s.more} open={open}>
      <summary className={s.toggle}>{summary}</summary>
      <div className={s.body}>{children}</div>
    </details>
  );
}
