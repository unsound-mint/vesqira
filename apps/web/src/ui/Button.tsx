import type { AnchorHTMLAttributes, ButtonHTMLAttributes, ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Icon } from "./Icon";
import s from "./Button.module.css";

type Common = { variant?: "default" | "primary" | "danger" | "ghost"; size?: "sm" | "md" | "lg" };

export function Button({
  variant = "default",
  size = "sm",
  icon,
  children,
  className,
  type = "button",
  ...rest
}: Common & { icon?: LucideIcon; children: ReactNode } & ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button type={type} className={[s.btn, s[variant], s[size], className].filter(Boolean).join(" ")} {...rest}>
      {icon ? <Icon icon={icon} /> : null}
      {children}
    </button>
  );
}

/** A link (a download, another page) that looks and sizes like a Button, so actions read as one family. */
export function LinkButton({
  variant = "default",
  size = "sm",
  icon,
  children,
  className,
  ...rest
}: Common & { icon?: LucideIcon; children: ReactNode } & AnchorHTMLAttributes<HTMLAnchorElement>) {
  return (
    <a className={[s.btn, s[variant], s[size], className].filter(Boolean).join(" ")} {...rest}>
      {icon ? <Icon icon={icon} /> : null}
      {children}
    </a>
  );
}

/** Icon-only button. `label` is required: it is the accessible name and the tooltip. */
export function IconButton({
  icon,
  label,
  variant = "ghost",
  size = "sm",
  pressed,
  type = "button",
  ...rest
}: Common & { icon: LucideIcon; label: string; pressed?: boolean } & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label">) {
  return (
    <button
      type={type}
      aria-label={label}
      title={label}
      aria-pressed={pressed}
      className={[s.btn, s.iconOnly, s[variant], s[size]].join(" ")}
      {...rest}
    >
      <Icon icon={icon} />
    </button>
  );
}
