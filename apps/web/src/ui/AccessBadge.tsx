import { EyeOff, Users } from "lucide-react";
import { Icon } from "./Icon";
import s from "./AccessBadge.module.css";

/** An access label (ADR 0069). The icon and the words carry it, never the colour. Renders nothing for a released dataset. */
export function AccessBadge({ label }: { label: string | undefined }) {
  if (label === "collaboration") return <span className={s.badge}><Icon icon={Users} />Collaboration only</span>;
  if (label === "unreleased") return <span className={`${s.badge} ${s.unreleased}`}><Icon icon={EyeOff} />Unreleased</span>;
  return null;
}
