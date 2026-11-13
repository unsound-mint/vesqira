import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Icon } from "./Icon";
import s from "./TreeRow.module.css";

/**
 * One explorer row: 28px, indent by level, optional trailing detail.
 * `fullName` is the whole name when `children` is a shortened form of it: it is the title (unless `title` is given) and the accessible name.
 * Without `onSelect` the row is plain text (no dead button in the tab order). `expanded` marks a row that opens a folder.
 */
export function TreeRow({ icon, level = 0, selected, expanded, onSelect, trailing, title: titleProp, fullName, rowProps, children }: { icon?: LucideIcon; level?: number; selected?: boolean; expanded?: boolean; onSelect?: () => void; trailing?: ReactNode; title?: string; fullName?: string; rowProps?: { tabIndex?: number; "data-roving"?: string }; children: ReactNode }) {
  const style = { paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` };
  const title = titleProp ?? fullName;
  const inner = (
    <>
      {icon ? <Icon icon={icon} /> : null}
      {fullName ? (
        <span className={s.name}>
          <span aria-hidden="true">{children}</span>
          <span className={s.full}>{fullName}</span>
        </span>
      ) : <span className={s.name}>{children}</span>}
      {trailing ? <span className={s.trail}>{trailing}</span> : null}
    </>
  );
  if (!onSelect) return <div className={`${s.row} ${s.plain}`} title={title} style={style}>{inner}</div>;
  return (
    <button type="button" className={s.row} aria-current={selected ? "true" : undefined} aria-expanded={expanded} title={title} onClick={onSelect} style={style} {...rowProps}>
      {inner}
    </button>
  );
}
