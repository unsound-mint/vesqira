import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { UndoBar } from "./UndoBar";
import { ownsUndo, undoKey, useUndoKeys } from "./keys";

const ev = (key: string, m: Partial<{ ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }> = {}) => ({ key, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, ...m });

describe("undo keys", () => {
  it("maps Ctrl and Cmd+Z to undo and with Shift to redo, and nothing else", () => {
    expect(undoKey(ev("z", { ctrlKey: true }))).toBe("undo");
    expect(undoKey(ev("z", { metaKey: true }))).toBe("undo");
    expect(undoKey(ev("Z", { ctrlKey: true, shiftKey: true }))).toBe("redo");
    expect(undoKey(ev("z", { metaKey: true, shiftKey: true }))).toBe("redo");
    expect(undoKey(ev("y", { ctrlKey: true }))).toBe("redo");
    expect(undoKey(ev("z"))).toBeNull();
    expect(undoKey(ev("z", { ctrlKey: true, metaKey: true }))).toBeNull();
    expect(undoKey(ev("z", { ctrlKey: true, altKey: true }))).toBeNull();
    expect(undoKey(ev("x", { ctrlKey: true }))).toBeNull();
  });

  it("leaves text fields to the browser's own undo", () => {
    const t = document.createElement("textarea");
    const i = document.createElement("input");
    const c = document.createElement("input");
    c.type = "checkbox";
    expect([ownsUndo(t), ownsUndo(i), ownsUndo(c), ownsUndo(document.body)]).toEqual([true, true, false, false]);
  });

  function Probe({ enabled = true, undo = () => true, redo = () => true }: { enabled?: boolean; undo?: () => boolean; redo?: () => boolean }) {
    useUndoKeys(enabled, { undo, redo });
    return <><button>focusable</button><textarea aria-label="note" /></>;
  }

  it("calls undo and redo, swallows the key only when it acted, and ignores a text field and a disabled surface", () => {
    const undo = vi.fn(() => true);
    const redo = vi.fn(() => false);
    const { rerender } = render(<Probe undo={undo} redo={redo} />);
    expect(fireEvent.keyDown(document.body, { key: "z", ctrlKey: true })).toBe(false); // preventDefault called
    expect(undo).toHaveBeenCalledTimes(1);
    expect(fireEvent.keyDown(document.body, { key: "z", metaKey: true, shiftKey: true })).toBe(true); // nothing to redo: the key goes on
    expect(redo).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(screen.getByLabelText("note"), { key: "z", ctrlKey: true });
    expect(undo).toHaveBeenCalledTimes(1);
    rerender(<Probe enabled={false} undo={undo} redo={redo} />);
    fireEvent.keyDown(document.body, { key: "z", ctrlKey: true });
    expect(undo).toHaveBeenCalledTimes(1);
  });
});

describe("UndoBar", () => {
  it("names the commands on its buttons, disables what is empty, and shows an error", async () => {
    const onUndo = vi.fn();
    const onRedo = vi.fn();
    const { rerender } = render(<UndoBar surface="board" undoName="move panel" redoName={null} onUndo={onUndo} onRedo={onRedo} />);
    await userEvent.click(screen.getByRole("button", { name: "Undo: move panel" }));
    expect(onUndo).toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Redo" })).toBeDisabled();
    rerender(<UndoBar surface="board" undoName={null} redoName="move panel" error="Could not undo “x”: gone." onUndo={onUndo} onRedo={onRedo} onDismiss={() => undefined} />);
    expect(screen.getByRole("button", { name: "Undo" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Redo: move panel" })).toBeEnabled();
    expect(screen.getByRole("alert")).toHaveTextContent("Could not undo");
  });
});
