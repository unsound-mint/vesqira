import { useId, useState } from "react";
import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Icon } from "./Icon";
import s from "./Section.module.css";

/** Collapsible inspector section: the progressive-disclosure unit (ADR 0005). */
export function Section({ title, defaultOpen = true, meta, children }: { title: string; defaultOpen?: boolean; meta?: ReactNode; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen);
  const id = useId();
  return (
    <section className={s.section}>
      <h2 className={s.heading}>
        <button type="button" className={s.toggle} aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
          <span className={open ? s.chevOpen : s.chev}><Icon icon={ChevronRight} /></span>
          <span className={s.title}>{title}</span>
          {meta ? <span className={s.meta}>{meta}</span> : null}
        </button>
      </h2>
      <div id={id} hidden={!open} className={s.body}>{children}</div>
    </section>
  );
}
