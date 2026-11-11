/**
 * Interpretation tiers (ADR 0055): how far an interpretation of the data is established. No imports, so the reviewer bundle can use it.
 * These are the app's own words; the worker's header statuses keep theirs ("declared" there means the file says it).
 */
export type Tier = "verified" | "declared" | "inferred" | "unresolved";

/** Short text for a badge: always words, never colour alone. A rule's proposal names the rule and its mapping version. */
export function tierText(tier: string | undefined | null, mappingVersion?: string | null, rule?: string | null): string {
  switch (tier) {
    case "verified":
      return "verified";
    case "declared":
      return "declared";
    case "inferred":
      return rule ? `inferred by ${rule}${mappingVersion ? `/${mappingVersion}` : ""}` : `inferred${mappingVersion ? ` (mapping ${mappingVersion})` : ""}`;
    case "unresolved":
      return "unresolved";
    default:
      return "tier not recorded";
  }
}

/** What a tier means, for a tooltip or a note. */
export function tierMeaning(tier: string | undefined | null): string {
  switch (tier) {
    case "verified":
      return "Established by a check the app ran. It is not a statement that the science is right.";
    case "declared":
      return "Stated by the researcher. The app applies it faithfully and has not checked it.";
    case "inferred":
      return "Proposed by an adapter or a rule from what it read; nobody has confirmed it.";
    case "unresolved":
      return "Nothing is known.";
    default:
      return "Saved before tiers were recorded.";
  }
}

/** One line of a figure's provenance about how far an interpretation is established: "orientation: declared. not from the file; ...". */
export function tierLine(t: { what: string; tier: string; rule?: string; mappingVersion?: string; note: string }): string {
  return `${t.what}: ${tierText(t.tier, t.mappingVersion, t.rule)}. ${t.note}.`;
}
