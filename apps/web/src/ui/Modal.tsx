import { Dialog, Modal as AriaModal, ModalOverlay } from "react-aria-components";
import type { ReactNode } from "react";
import s from "./Modal.module.css";

/**
 * A modal dialog with real focus containment (ADR 0033): Tab and Shift+Tab stay inside, the page behind is hidden from
 * assistive technology, Escape asks to close, and focus goes back to what opened it. Behaviour comes from React Aria;
 * the look is ours and is shared: a scrim over the page and one card (surface, border, radius, shadow, padding) that
 * scrolls when its content is long, with `ModalActions` sticking to its bottom edge. Callers do not restyle the card.
 * `keepEscape` leaves Escape to the content (a terminal needs it). `size` is the card's width. `fullscreen` has no card: the content owns the whole viewport (Present, the editor), and
 * `className` is for that content.
 */
export function Modal({ label, onClose, fullscreen, size = "md", className, keepEscape, children }: { label: string; onClose: () => void; fullscreen?: boolean; size?: "sm" | "md" | "lg"; className?: string; keepEscape?: boolean; children: ReactNode }) {
  return (
    <ModalOverlay className={`${s.overlay} ${fullscreen ? s.fullscreen : s.scrim}`} isOpen isKeyboardDismissDisabled={keepEscape} onOpenChange={(open) => { if (!open) onClose(); }}>
      <AriaModal className={s.modal}>
        <Dialog aria-label={label} className={`${s.dialog} ${fullscreen ? "" : `${s.card} ${s[size]}`} ${className ?? ""}`}>{children}</Dialog>
      </AriaModal>
    </ModalOverlay>
  );
}

/** The dialog's buttons: right-aligned, and kept in view at the bottom of a card whose content scrolls. */
export function ModalActions({ children }: { children: ReactNode }) {
  return <div className={s.actions}>{children}</div>;
}
