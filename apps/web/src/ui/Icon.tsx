import type { LucideIcon } from "lucide-react";

/** Row icons are 16px, rail icons 20px, 1.5px stroke (ADR 0005). Decorative unless a label is given. */
export function Icon({ icon: Glyph, size = 16, label }: { icon: LucideIcon; size?: 16 | 20; label?: string }) {
  return (
    <Glyph
      size={size}
      strokeWidth={1.5}
      aria-hidden={label ? undefined : true}
      aria-label={label}
      role={label ? "img" : undefined}
      focusable="false"
    />
  );
}
