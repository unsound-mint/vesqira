import { Redo2, Undo2 } from "lucide-react";
import { Button } from "../ui";
import s from "./UndoBar.module.css";

/** Undo and Redo for one surface, each naming the command it would reverse ("Undo: move panel"). */
export function UndoBar({ surface, undoName, redoName, busy = false, error = null, onUndo, onRedo, onDismiss }: {
  surface: string;
  undoName: string | null;
  redoName: string | null;
  busy?: boolean;
  error?: string | null;
  onUndo: () => void;
  onRedo: () => void;
  onDismiss?: () => void;
}) {
  return (
    <div className={s.bar} role="group" aria-label={`Undo and redo for the ${surface}`}>
      <Button icon={Undo2} disabled={!undoName || busy} onClick={onUndo} title={`Undo (Ctrl+Z)`}>{undoName ? `Undo: ${undoName}` : "Undo"}</Button>
      <Button icon={Redo2} disabled={!redoName || busy} onClick={onRedo} title={`Redo (Ctrl+Shift+Z)`}>{redoName ? `Redo: ${redoName}` : "Redo"}</Button>
      {error ? (
        <p role="alert" className={s.err}>
          {error}{" "}
          {onDismiss ? <button type="button" className={s.link} onClick={onDismiss}>Dismiss</button> : null}
        </p>
      ) : null}
    </div>
  );
}
