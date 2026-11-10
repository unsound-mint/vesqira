import { Dialog, Modal as AriaModal, ModalOverlay } from "react-aria-components";
import type { ReactNode } from "react";
import s from "./Modal.module.css";

/**
 * A modal dialog with real focus containment (ADR 0033): Tab and Shift+Tab stay inside, the page behind is hidden from
 * assistive technology, Escape asks to close, and focus goes back to what opened it. Behaviour comes from React Aria;
 * the look is ours. `className` is for the content, which fills the viewport when `fullscreen`.
 */
export function Modal({ label, onClose, fullscreen, className, children }: { label: string; onClose: () => void; fullscreen?: boolean; className?: string; children: ReactNode }) {
  return (
    <ModalOverlay className={`${s.overlay} ${fullscreen ? s.fullscreen : ""}`} isOpen onOpenChange={(open) => { if (!open) onClose(); }}>
      <AriaModal className={s.modal}>
        <Dialog aria-label={label} className={`${s.dialog} ${className ?? ""}`}>{children}</Dialog>
      </AriaModal>
    </ModalOverlay>
  );
}
