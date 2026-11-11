import { Select } from "../ui";
import { maskTypeLabel } from "../api/masks";
import type { Mask, MaskPolicy, MaskUse } from "../api/masks";
import s from "./AnalysisPanel.module.css";

/** What each mask of one dataset does in this analysis: the choice is the person's, per mask, and has no default (a mask left at "not applied" is not applied). */
export type PolicyChoice = Record<number, { policy: "" | MaskPolicy; weight: string }>;

/** The masks to send, from the choices made. */
export function usesOf(choice: PolicyChoice): MaskUse[] {
  return Object.entries(choice).flatMap(([id, c]) => (c.policy ? [{ id: Number(id), policy: c.policy, weight: c.policy === "weight" ? Number(c.weight) : undefined }] : []));
}

/** True when a chosen weight is not a number from 0 to 1. */
export const badWeight = (choice: PolicyChoice): boolean => Object.values(choice).some((c) => c.policy === "weight" && !(c.weight.trim() !== "" && Number(c.weight) >= 0 && Number(c.weight) <= 1));

/** One policy control per usable mask. `weight` is offered only by an analysis that already has weights. */
export function MaskPolicies({ masks, choice, onChange, allowWeight = false, label = "Masks" }: { masks: Mask[]; choice: PolicyChoice; onChange: (c: PolicyChoice) => void; allowWeight?: boolean; label?: string }) {
  if (masks.length === 0) return null;
  return (
    <fieldset style={{ border: 0, padding: 0, margin: 0, display: "grid", gap: "var(--sp-2)" }} aria-label={label}>
      <legend className={s.note}>{label}: what to do with each (none is applied unless you choose)</legend>
      {masks.map((m) => {
        const c = choice[m.id] ?? { policy: "", weight: "0.5" };
        const set = (p: Partial<typeof c>) => onChange({ ...choice, [m.id]: { ...c, ...p } });
        return (
          <div key={m.id} className={s.two}>
            <Select label={`${maskTypeLabel(m.type)}: ${m.name}`} value={c.policy} disabled={m.stale} onChange={(e) => set({ policy: e.target.value as "" | MaskPolicy })}>
              <option value="">Not applied</option>
              <option value="exclude">Exclude the flagged pixels</option>
              {m.type === "missing" ? null : <option value="keep">Keep them (recorded as kept)</option>}
              {allowWeight && m.type !== "missing" ? <option value="weight">Weight them</option> : null}
            </Select>
            {c.policy === "weight" ? <label className={s.note}>Factor 0 to 1<input aria-label={`Weight factor for ${m.name}`} value={c.weight} onChange={(e) => set({ weight: e.target.value })} inputMode="decimal" /></label> : <span className={s.note}>{m.stale ? "stale: make it again" : ""}</span>}
          </div>
        );
      })}
    </fieldset>
  );
}
