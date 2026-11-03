import type { ReactNode } from "react";
import s from "./Pill.module.css";

/** Status is always text, never colour alone. */
export function Pill({ tone = "neutral", children }: { tone?: "neutral" | "ok" | "warn" | "err" | "accent"; children: ReactNode }) {
  return <span className={[s.pill, s[tone]].join(" ")}>{children}</span>;
}
