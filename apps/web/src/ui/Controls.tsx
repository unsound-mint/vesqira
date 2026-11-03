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

export function Select({ label, children, ...rest }: { label: string } & SelectHTMLAttributes<HTMLSelectElement>) {
  const id = useId();
  return (
    <div className={s.field}>
      <label htmlFor={id} className={s.label}>{label}</label>
      <div className={s.selectWrap}>
        <select id={id} className={s.select} {...rest}>{children}</select>
        <span className={s.chevron}><Icon icon={ChevronDown} /></span>
      </div>
    </div>
  );
}

export function Input({ label, ...rest }: { label: string } & InputHTMLAttributes<HTMLInputElement>) {
  const id = useId();
  return (
    <div className={s.field}>
      <label htmlFor={id} className={s.label}>{label}</label>
      <input id={id} className={s.input} {...rest} />
    </div>
  );
}

/** Native range input. Arrow keys, Home/End and PageUp/PageDown come from the browser. */
export function Slider({ label, valueText, ...rest }: { label: string; valueText?: string } & Omit<InputHTMLAttributes<HTMLInputElement>, "type">) {
  const id = useId();
  return (
    <div className={s.field}>
      <div className={s.labelRow}>
        <label htmlFor={id} className={s.label}>{label}</label>
        {valueText ? <output htmlFor={id} className={s.value}>{valueText}</output> : null}
      </div>
      <input id={id} type="range" className={s.range} aria-valuetext={valueText} {...rest} />
    </div>
  );
}
