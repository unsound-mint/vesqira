import { Pill } from "./Pill";
import { tierMeaning, tierText } from "../api/tier";

/** The tier of an interpretation as a pill with words: verified, declared, inferred by a rule/mapping version, unresolved. */
export function TierBadge({ tier, mappingVersion, rule }: { tier: string | undefined | null; mappingVersion?: string | null; rule?: string | null }) {
  const tone = tier === "verified" ? "ok" : tier === "inferred" ? "warn" : tier === "unresolved" ? "err" : "neutral";
  return (
    <span title={tierMeaning(tier)} data-tier={tier ?? ""}>
      <Pill tone={tone}>{tierText(tier, mappingVersion, rule)}</Pill>
    </span>
  );
}
