// A bounded undo/redo stack of named commands whose effects are server calls (a board's edits). The viewer pane has its own
// reducer-based history (sceneCommands.ts) because its state is a pure value; a board's state is on the server, so a command
// here carries its own inverse call.

export const MAX_DEPTH = 50;

/** Thrown by a command's undo or redo when the thing it would change is no longer as it left it (another window, or the server refused). */
export class Conflict extends Error {
  constructor(message: string) {
    super(message);
    this.name = "Conflict";
  }
}

export interface Command {
  /** Shown as "Undo: <name>"; a short lower-case phrase such as "move panel". */
  name: string;
  /** Reverse the command. Throw Conflict (or let a server error through) when it cannot be reversed exactly. */
  undo(): Promise<void>;
  /** Do it again after an undo. */
  redo(): Promise<void>;
}

export type UndoSnapshot = { undoName: string | null; redoName: string | null; busy: boolean; error: string | null };

/**
 * The commands of one surface. A command is pushed after its effect succeeded. Undo and redo run one at a time. A command that
 * cannot be applied (a conflict, a server error) is dropped with the whole redo stack: after a failure the history no longer
 * describes the server, so nothing that depends on it is offered. The error is the command's own account of what it did and did not change.
 */
export class UndoStack {
  private past: Command[] = [];
  private future: Command[] = [];
  private busy = false;
  private error: string | null = null;
  private listeners = new Set<() => void>();
  private snap: UndoSnapshot = { undoName: null, redoName: null, busy: false, error: null };

  constructor(private readonly max = MAX_DEPTH) {}

  private publish() {
    const p = this.past[this.past.length - 1];
    const f = this.future[this.future.length - 1];
    this.snap = { undoName: p?.name ?? null, redoName: f?.name ?? null, busy: this.busy, error: this.error };
    this.listeners.forEach((l) => l());
  }

  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };
  /** A stable object until something changes (for useSyncExternalStore). */
  getSnapshot = () => this.snap;

  /** Record a command whose effect has already happened. A new command ends the redo branch. */
  push(c: Command) {
    this.past = [...this.past, c].slice(-this.max);
    this.future = [];
    this.error = null;
    this.publish();
  }

  clear() {
    this.past = [];
    this.future = [];
    this.error = null;
    this.publish();
  }

  clearError() {
    if (this.error === null) return;
    this.error = null;
    this.publish();
  }

  get depth() {
    return this.past.length;
  }

  async undo(): Promise<boolean> {
    const c = this.past[this.past.length - 1];
    if (!c || this.busy) return false;
    this.busy = true;
    this.error = null;
    this.publish();
    try {
      await c.undo();
      this.past = this.past.slice(0, -1);
      this.future = [...this.future, c];
      return true;
    } catch (e) {
      this.past = this.past.slice(0, -1);
      this.future = [];
      this.error = `Could not undo “${c.name}”: ${e instanceof Error ? e.message : "it failed."}`;
      return false;
    } finally {
      this.busy = false;
      this.publish();
    }
  }

  async redo(): Promise<boolean> {
    const c = this.future[this.future.length - 1];
    if (!c || this.busy) return false;
    this.busy = true;
    this.error = null;
    this.publish();
    try {
      await c.redo();
      this.future = this.future.slice(0, -1);
      this.past = [...this.past, c];
      return true;
    } catch (e) {
      this.future = [];
      this.error = `Could not redo “${c.name}”: ${e instanceof Error ? e.message : "it failed."}`;
      return false;
    } finally {
      this.busy = false;
      this.publish();
    }
  }
}
