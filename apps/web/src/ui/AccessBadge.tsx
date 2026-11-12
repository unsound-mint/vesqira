import { EyeOff, Users } from "lucide-react";
import { Icon } from "./Icon";
import { Pill } from "./Pill";

/** An access label (ADR 0069). The icon and the words carry it; the tone only reinforces. Renders nothing for a released dataset. */
export function AccessBadge({ label }: { label: string | undefined }) {
  if (label === "collaboration") return <Pill tone="warn"><Icon icon={Users} />&nbsp;Collaboration only</Pill>;
  if (label === "unreleased") return <Pill tone="err"><Icon icon={EyeOff} />&nbsp;Unreleased</Pill>;
  return null;
}
