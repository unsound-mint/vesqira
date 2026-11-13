import type { DatasetSummary } from "../api/catalog";

/** True when the file's own header states a unit (a BUNIT card): then nothing can be declared for it. */
export function fileStatesItsUnit(d: Pick<DatasetSummary, "facts">): boolean {
  return d.facts.bunitStated; // false for a catalog: it has no BUNIT, its columns carry their own units
}

/** The unit you corrected the file's BUNIT to (ADR 0061), when it is in force; null otherwise. */
export function correctedUnit(d: Pick<DatasetSummary, "corrections">): string | null {
  const c = d.corrections;
  return c && !c.stale && typeof c.state.unit?.value === "string" ? c.state.unit.value : null;
}

/** The unit a dataset is found by in filters: your correction, else the file's BUNIT, else the one you declared; "" when there is none. */
export function effectiveUnit(d: Pick<DatasetSummary, "facts" | "unitDeclaration" | "corrections">): string {
  const corrected = correctedUnit(d);
  if (corrected) return corrected;
  const fromFile = (d.facts.bunit ?? "").trim();
  return fromFile !== "" ? fromFile : (d.unitDeclaration?.unit ?? "").trim();
}
