import type { Dataset } from "../api/catalog";

/** True when the file's own header states a unit (a BUNIT card): then nothing can be declared for it. */
export function fileStatesItsUnit(d: Pick<Dataset, "header">): boolean {
  const h = d.header;
  return "bunit" in h && (h.bunit_status === "declared" || h.bunit != null); // a catalog has no BUNIT: its columns carry their own units
}

/** The unit you corrected the file's BUNIT to (ADR 0061), when it is in force; null otherwise. */
export function correctedUnit(d: Pick<Dataset, "corrections">): string | null {
  const c = d.corrections;
  return c && !c.stale && typeof c.state.unit?.value === "string" ? c.state.unit.value : null;
}

/** The unit a dataset is found by in filters: your correction, else the file's BUNIT, else the one you declared; "" when there is none. */
export function effectiveUnit(d: Pick<Dataset, "header" | "unitDeclaration" | "corrections">): string {
  const corrected = correctedUnit(d);
  if (corrected) return corrected;
  const fromFile = String("bunit" in d.header ? (d.header.bunit ?? "") : "").trim();
  return fromFile !== "" ? fromFile : (d.unitDeclaration?.unit ?? "").trim();
}
