import { maskTypeLabel } from "../api/masks";
import type { AppliedRegionMask } from "../api/planes";
import { TierBadge } from "../ui/TierBadge";
import s from "./RegionPanel.module.css";

const n = (v: number) => v.toLocaleString("en-US");

/** The masks a region's spectrum or histogram applied, each with its policy and what it did. Counts are pixels with a value, summed over the channels read. */
export function AppliedMasks({ masks, noValue, excluded, testId }: { masks: AppliedRegionMask[]; noValue?: number; excluded?: number; testId: string }) {
  return (
    <div data-testid={testId}>
      <ul className={s.note} aria-label="Masks applied">
        {masks.map((m) => (
          <li key={m.id}>
            {maskTypeLabel(m.type)} mask “{m.name}”: {m.policy === "exclude" ? "excluded" : "kept (not excluded)"}; {n(m.pixels_flagged_with_value)} pixels with a value flagged, {n(m.pixels_excluded)} left out. {m.tier ? <TierBadge tier={m.tier} /> : null}
          </li>
        ))}
      </ul>
      <p className={s.note}>
        {noValue !== undefined ? `${n(noValue)} pixels have no value in the file (counted apart from masks). ` : ""}
        {excluded !== undefined ? `${n(excluded)} pixels with a value were left out by masks.` : ""}
      </p>
    </div>
  );
}
