import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Icon } from "./Icon";
import s from "./TreeRow.module.css";

/** One explorer row: 28px, indent by level, optional trailing detail. */
export function TreeRow({ icon, level = 0, selected, onSelect, trailing, children }: { icon?: LucideIcon; level?: number; selected?: boolean; onSelect?: () => void; trailing?: ReactNode; children: ReactNode }) {
  return (
    <button type="button" className={s.row} aria-current={selected ? "true" : undefined} onClick={onSelect} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>
      {icon ? <Icon icon={icon} /> : null}
      <span className={s.name}>{children}</span>
      {trailing ? <span className={s.trail}>{trailing}</span> : null}
    </button>
  );
}
