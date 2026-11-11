import { useSyncExternalStore } from "react";
import { UndoStack } from "./stack";
import type { UndoSnapshot } from "./stack";

const none = new UndoStack();
const noSnapshot: UndoSnapshot = none.getSnapshot();

/** The state of a stack (or of none, with no board open) for rendering. */
export function useUndoStack(stack: UndoStack | null): UndoSnapshot {
  const s = stack ?? none;
  return useSyncExternalStore(s.subscribe, stack ? s.getSnapshot : () => noSnapshot);
}
