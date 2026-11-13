import { useId } from "react";
import { ChevronDown } from "lucide-react";
import { Icon } from "./Icon";
import type { InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";
import s from "./Controls.module.css";

/** Native checkbox, restyled: forms, labels and screen readers keep working. */
export function Checkbox({ label, ...rest }: { label: ReactNode } & Omit<InputHTMLAttributes<HTMLInputElement>, "type">) {
  return (
    <label className={s.row}>
      <input type="checkbox" className={s.check} {...rest} />
      <span>{label}</span>
    </label>
  );
}

export function Switch({ label, ...rest }: { label: ReactNode } & Omit<InputHTMLAttributes<HTMLInputElement>, "type" | "role">) {
  return (
    <label className={s.row}>
      <input type="checkbox" role="switch" className={s.switch} {...rest} />
      <span>{label}</span>
    </label>
  );
}

/** `description` is a plain sentence under the field, read with it. */
export function Select({ label, description, children, "aria-describedby": extra, ...rest }: { label: string; description?: string } & SelectHTMLAttributes<HTMLSelectElement>) {
  const id = useId();
  const descId = `${id}-desc`;
  const described = [description ? descId : "", extra ?? ""].filter(Boolean).join(" "); // a caller's own description is added, never replaces this one
  return (
    <div className={s.field}>
      <label htmlFor={id} className={s.label}>{label}</label>
      <div className={s.selectWrap}>
        <select id={id} className={s.select} aria-describedby={described || undefined} {...rest}>{children}</select>
        <span className={s.chevron}><Icon icon={ChevronDown} /></span>
      </div>
      {description ? <span id={descId} className={s.hint}>{description}</span> : null}
    </div>
  );
}

/** `hint` is a short gloss under the field ("1 day" for a time limit of 1-00:00:00), read with the field; nothing shows when it is empty. `description` is a plain sentence under it. */
export function Input({ label, hint, description, "aria-describedby": extra, ...rest }: { label: string; hint?: string; description?: string } & InputHTMLAttributes<HTMLInputElement>) {
  const id = useId();
  const hintId = `${id}-hint`;
  const descId = `${id}-desc`;
  const described = [hint ? hintId : "", description ? descId : "", extra ?? ""].filter(Boolean).join(" "); // a caller's own description is added, never replaces these
  return (
    <div className={s.field}>
      <label htmlFor={id} className={s.label}>{label}</label>
      <input id={id} className={s.input} aria-describedby={described || undefined} {...rest} />
      {hint ? <span id={hintId} className={s.hint}>= {hint}</span> : null}
      {description ? <span id={descId} className={s.hint}>{description}</span> : null}
    </div>
  );
}

/** Native range input. Arrow keys, Home/End and PageUp/PageDown come from the browser. */
export function Slider({ label, valueText, hideValue, style, ...rest }: { label: string; valueText?: string; hideValue?: boolean } & Omit<InputHTMLAttributes<HTMLInputElement>, "type">) {
  const id = useId();
  // the filled part of the track follows the value
  const lo = Number(rest.min ?? 0);
  const hi = Number(rest.max ?? 100);
  const v = Number(rest.value ?? rest.defaultValue ?? lo);
  const fill = hi > lo ? Math.min(100, Math.max(0, ((v - lo) / (hi - lo)) * 100)) : 0;
  return (
    <div className={s.field}>
      <div className={s.labelRow}>
        <label htmlFor={id} className={s.label}>{label}</label>
        {valueText && !hideValue ? <output htmlFor={id} className={s.value}>{valueText}</output> : null}
      </div>
      <input id={id} type="range" className={s.range} aria-valuetext={valueText} style={{ ...style, ["--p" as string]: `${fill}%` }} {...rest} />
    </div>
  );
}
