import { Select } from "../ui";
import { TierBadge } from "../ui/TierBadge";
import { maskTypeLabel } from "../api/masks";
import type { Mask } from "../api/masks";
import type { CarryChoice } from "../api/analysis";
import s from "./AnalysisPanel.module.css";

/** What the researcher chose for each mask of the source: "" = not chosen yet. A cut has no default but "leave behind"; a resampling or smoothing has none at all. */
export type CarryState = Record<number, "" | "carry" | "leave">;

export type CarryOp = "cutout" | "slab" | "subset" | "align" | "convolve";

/** The rule by which a flag follows the data, in the words the new mask's own rule uses (ADR 0055). Shown before the choice, so it is never a surprise. */
export const CARRY_RULE: Record<CarryOp, string> = {
  cutout: "Cut with exactly the same window as the data: a flagged pixel stays flagged at the same place, and nothing else is flagged.",
  slab: "Cut to the same channel range as the data; a mask of one plane applies to every channel and is kept whole.",
  subset: "Cut with the subset's window, its step and its channel range, exactly as the data is.",
  align: "Reprojected as a float plane with the exact (area-overlap) method: an output pixel is flagged when more than a millionth of the input area it covers is flagged, so any contributing flagged pixel flags it. Outside the input's footprint nothing is flagged.",
  convolve: "An output pixel is flagged when the convolution kernel, truncated to the square the data is convolved with, reaches a flagged pixel; the flagged area grows by the kernel's half-width.",
};

/** The masks of a source that can be carried: everything but the no-value mask, which is computed again from the result. */
export const carriable = (masks: Mask[]): Mask[] => masks.filter((m) => m.type !== "missing");

/** The request's choices: every mask that was decided. For a cut, a mask not chosen is sent as left behind; the server records it. */
export function carryChoices(masks: Mask[], state: CarryState, required: boolean): CarryChoice[] {
  return carriable(masks).flatMap((m) => {
    const c = state[m.id] ?? "";
    if (c === "") return required ? [] : [{ id: m.id, carry: false }];
    return [{ id: m.id, carry: c === "carry" }];
  });
}

/** True when an operation that wants every mask decided still has one that is not. */
export const carryUndecided = (masks: Mask[], state: CarryState, required: boolean): boolean => required && carriable(masks).some((m) => (state[m.id] ?? "") === "");

/**
 * One choice per mask of the source: carry it into the new file, or leave it behind. Nothing is carried unless chosen; a resampling or a
 * smoothing asks for every mask and has no default. The no-value mask is never offered: it is computed again from the result.
 */
export function MaskCarry({ masks, state, onChange, op, required }: { masks: Mask[]; state: CarryState; onChange: (s: CarryState) => void; op: CarryOp; required: boolean }) {
  const list = carriable(masks);
  const hasMissing = masks.some((m) => m.type === "missing");
  if (list.length === 0 && !hasMissing) return null;
  return (
    <fieldset style={{ border: 0, padding: 0, margin: 0, display: "grid", gap: "var(--sp-2)" }} aria-label="Masks of the new file" data-testid="mask-carry">
      <legend className={s.note}>Masks of the new file: {required ? "choose for each, none is chosen for you" : "none is carried unless you choose"}</legend>
      {list.length > 0 ? <p className={s.note}>{CARRY_RULE[op]}</p> : null}
      {list.map((m) => {
        const c = state[m.id] ?? (required ? "" : "leave");
        return (
          <div key={m.id} className={s.two}>
            <Select label={`${maskTypeLabel(m.type)}: ${m.name}`} value={c} onChange={(e) => onChange({ ...state, [m.id]: e.target.value as CarryState[number] })}>
              {required ? <option value="">Choose…</option> : null}
              <option value="carry" disabled={m.stale}>Carry into the new file</option>
              <option value="leave">Leave behind (recorded)</option>
            </Select>
            <span className={s.note}>{m.stale ? "stale: made from other bytes, it cannot be carried" : <TierBadge tier={m.tier} mappingVersion={m.mappingVersion} />}</span>
          </div>
        );
      })}
      {hasMissing ? <p className={s.note}>The no-value mask is computed again from the new file&apos;s own values; it is never carried.</p> : null}
    </fieldset>
  );
}
