import { Suspense, lazy, useState } from "react";
import { Share2 } from "lucide-react";
import { Button } from "../ui";
import type { Figure } from "../api/boards";

// the dialog brings the modal primitives (ADR 0033), which the rest of the app does not need until someone shares
const ShareFigureDialog = lazy(() => import("./ShareFigureDialog"));

/**
 * Opens "Share this view" (ADR 0071). `getFigure` gives the figure to freeze: the viewer saves what is on screen, a saved figure
 * returns itself. The caller says why it is disabled (the viewer cannot save a reduced preview).
 */
export function ShareFigureButton({ title, getFigure, label = "Share this view", variant, disabled, onClosed }: { title: string; getFigure: () => Promise<Figure>; label?: string; variant?: "default" | "primary"; disabled?: boolean; onClosed?: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button icon={Share2} {...(variant ? { variant } : {})} {...(disabled ? { disabled } : {})} onClick={() => setOpen(true)}>{label}</Button>
      {open ? <Suspense fallback={null}><ShareFigureDialog title={title} getFigure={getFigure} onClose={() => { setOpen(false); onClosed?.(); }} /></Suspense> : null}
    </>
  );
}
