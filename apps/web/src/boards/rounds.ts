import type { ChangeLine, ItemChange } from "../api/boards";

/** The words of one change, shared by the reviewer's page and the author's history so they cannot disagree. */
const KIND: Record<ChangeLine["kind"], string> = {
  added: "Added",
  removed: "Removed",
  redrawn: "Redrawn",
  title: "Title changed",
  caption: "Caption changed",
  note: "Note changed",
  corrections: "Corrections changed",
  masks: "Masks changed",
  access: "Access label changed",
  state: "Run state changed",
};

export function lineText(l: ChangeLine): string {
  return `${KIND[l.kind]}: ${l.title}${l.detail ? ` (${l.detail})` : ""}`;
}

/** "1 new, 1 redrawn, 2 unchanged, 1 removed": only the parts that are not zero. */
export function summary(items: ItemChange[], removed: number): string {
  const n = (state: ItemChange["state"]) => items.filter((i) => i.state === state).length;
  const parts = [[n("new"), "new"], [n("redrawn"), "redrawn"], [n("unchanged"), "unchanged"], [removed, "removed"]] as const;
  return parts.filter(([k]) => k > 0).map(([k, w]) => `${k} ${w}`).join(", ") || "nothing";
}

/** The marker on a figure. Words carry the meaning; the glyph is decoration and the colour only a hint. */
export const MARKER: Record<ItemChange["state"], { glyph: string; text: string }> = {
  new: { glyph: "+", text: "New" },
  redrawn: { glyph: "↻", text: "Redrawn" },
  unchanged: { glyph: "=", text: "Unchanged" },
};
