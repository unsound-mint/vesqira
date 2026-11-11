import { useEffect, useRef } from "react";

/** Is the event aimed at something that has its own undo (a text field, a text area, an editor)? Then the key is left alone. */
export function ownsUndo(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  if (t.isContentEditable) return true;
  if (t instanceof HTMLTextAreaElement) return true;
  if (t instanceof HTMLInputElement) return !["checkbox", "radio", "button", "range", "submit", "reset"].includes(t.type);
  return t.closest(".monaco-editor, .xterm") !== null;
}

/** What a key press asks for: Ctrl or Cmd+Z undoes, with Shift (or Ctrl+Y) redoes. Null for any other key. */
export function undoKey(e: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "shiftKey" | "altKey">): "undo" | "redo" | null {
  if (e.altKey || (e.ctrlKey === e.metaKey)) return null; // exactly one of Ctrl and Cmd
  const k = e.key.toLowerCase();
  if (k === "z") return e.shiftKey ? "redo" : "undo";
  if (k === "y" && e.ctrlKey && !e.shiftKey) return "redo";
  return null;
}

/**
 * Ctrl+Z / Cmd+Z and Ctrl+Shift+Z / Cmd+Shift+Z for one surface while it is enabled. A key aimed at a text field keeps the
 * browser's own undo of the text; a key the surface cannot act on (nothing to undo) is not swallowed.
 */
export function useUndoKeys(enabled: boolean, handlers: { undo: () => boolean; redo: () => boolean }) {
  const h = useRef(handlers);
  h.current = handlers;
  useEffect(() => {
    if (!enabled) return;
    const on = (e: KeyboardEvent) => {
      const which = undoKey(e);
      if (!which || e.defaultPrevented || ownsUndo(e.target)) return;
      if (h.current[which]()) e.preventDefault();
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [enabled]);
}
