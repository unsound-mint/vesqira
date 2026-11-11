import type { Dataset } from "../api/catalog";

/** True when the file's own header states a unit (a BUNIT card): then nothing can be declared for it. */
export function fileStatesItsUnit(d: Pick<Dataset, "header">): boolean {
  const h = d.header;
  return "bunit" in h && (h.bunit_status === "declared" || h.bunit != null); // a catalog has no BUNIT: its columns carry their own units
}

/** The unit a dataset is found by in filters: the file's BUNIT, or else the one you declared; "" when there is neither. */
export function effectiveUnit(d: Pick<Dataset, "header" | "unitDeclaration">): string {
  const fromFile = String("bunit" in d.header ? (d.header.bunit ?? "") : "").trim();
  return fromFile !== "" ? fromFile : (d.unitDeclaration?.unit ?? "").trim();
}
