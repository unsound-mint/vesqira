import { useId, useState } from "react";
import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Icon } from "./Icon";
import s from "./Section.module.css";

/** Collapsible inspector section: the progressive-disclosure unit (ADR 0005). */
export function Section({ title, defaultOpen = true, openSignal, meta, onToggle, children }: { title: string; defaultOpen?: boolean; /** Opens the section each time this value changes (not on first render). */ openSignal?: number; meta?: ReactNode; onToggle?: (open: boolean) => void; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen);
  const [seen, setSeen] = useState(openSignal);
  if (openSignal !== seen) { // adjusting state while rendering: the documented way to react to a changed prop
    setSeen(openSignal);
    setOpen(true);
  }
  const id = useId();
  return (
    <section className={s.section}>
      <h2 className={s.heading}>
        <button type="button" className={s.toggle} aria-expanded={open} aria-controls={id} onClick={() => { setOpen(!open); onToggle?.(!open); }}>
          <span className={open ? s.chevOpen : s.chev}><Icon icon={ChevronRight} /></span>
          <span className={s.title}>{title}</span>
          {meta ? <span className={s.meta}>{meta}</span> : null}
        </button>
      </h2>
      <div id={id} hidden={!open} className={s.body}>{children}</div>
    </section>
  );
}
