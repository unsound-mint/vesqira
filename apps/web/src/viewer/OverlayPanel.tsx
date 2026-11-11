import { useState } from "react";
import { ArrowDown, ArrowUp, Pencil, Plus, X } from "lucide-react";
import { Button, IconButton, Input, Select, Switch } from "../ui";
import type { Dataset } from "../api/catalog";
import { overlayBasis, overlayTitle } from "../scene/basis";
import { needsGeometry } from "../scene/geometry";
import { OVERLAY_COLOR_NAMES } from "../scene/palette";
import { candidates, channelsOf, draftToOverlay, emptyDraft, newOverlayId, overlayToDraft, unitOf } from "../scene/overlayForm";
import type { AddableKind, Draft } from "../scene/overlayForm";
import { ORIENTATION_ID, REGIONS_ID } from "../scene/scene";
import type { PaneAction, Scene, SceneOverlay } from "../scene/scene";
import type { GeometryMap } from "../scene/geometry";
import s from "./OverlayPanel.module.css";

const KINDS: { id: AddableKind; label: string }[] = [
  { id: "contours", label: "Contours" },
  { id: "orientation", label: "Orientation lines" },
  { id: "beam", label: "Beam" },
  { id: "grid", label: "Coordinate grid" },
];
const BUILT_IN = new Set([ORIENTATION_ID, REGIONS_ID]);

/**
 * The pane's overlays in drawing order (the first at the bottom): show or hide, move, edit, remove, and add one from a
 * registered dataset. Every change is a scene action. Each row says what it draws and how it was placed, or why it is not
 * drawn. The base map's own orientation lines and regions can be hidden and moved but not removed: other controls drive them.
 */
export function OverlayPanel({ scene, dispatch, geometry, datasets }: { scene: Scene | null; dispatch: (a: PaneAction) => void; geometry: GeometryMap; datasets: Dataset[] }) {
  const [editing, setEditing] = useState<{ id: string | null; draft: Draft } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  if (!scene) return null;
  const baseId = scene.base.datasetId;
  const nameOf = (id: number) => datasets.find((d) => d.id === id)?.name ?? `dataset ${id}`;
  const n = scene.overlays.length;

  function apply() {
    if (!editing || !scene) return;
    const old = editing.id ? scene.overlays.find((o) => o.id === editing.id) : undefined;
    const id = old?.id ?? newOverlayId(editing.draft.kind, scene);
    const next = draftToOverlay(editing.draft, id, old?.visible ?? true, datasets, baseId);
    if (typeof next === "string") {
      setProblem(next);
      return;
    }
    if (old) {
      // a patch: every setting of the edited overlay, and null for the ones it no longer has
      const patch: Record<string, unknown> = {};
      for (const k of Object.keys(old)) if (k !== "id" && k !== "kind" && !(k in next)) patch[k] = null;
      for (const [k, v] of Object.entries(next)) if (k !== "id" && k !== "kind") patch[k] = v;
      dispatch({ type: "updateOverlay", id: old.id, patch });
    } else {
      dispatch({ type: "addOverlay", overlay: next });
    }
    setEditing(null);
    setProblem(null);
  }

  return (
    <div className={s.wrap}>
      {n === 0 ? <p className={s.note}>No overlays.</p> : (
        <ol className={s.list} aria-label="Overlays, drawn in this order (the last on top)">
          {scene.overlays.map((o, i) => (
            <Row key={o.id} o={o} i={i} n={n} scene={scene} geometry={geometry} nameOf={nameOf} dispatch={dispatch}
              onEdit={() => { const d = overlayToDraft(o); if (d) { setEditing({ id: o.id, draft: d }); setProblem(null); } }}
              editing={editing?.id === o.id} />
          ))}
        </ol>
      )}
      {editing ? (
        <OverlayForm draft={editing.draft} adding={editing.id === null} datasets={datasets} baseId={baseId} problem={problem}
          onChange={(draft) => setEditing({ ...editing, draft })} onApply={apply} onCancel={() => { setEditing(null); setProblem(null); }} />
      ) : (
        <div className={s.actions}>
          <Button icon={Plus} onClick={() => { setEditing({ id: null, draft: emptyDraft("contours", candidates("contours", datasets, baseId).find((d) => d.id !== baseId)?.id ?? baseId) }); setProblem(null); }} disabled={n >= 16}>
            Add overlay
          </Button>
        </div>
      )}
      <p className={s.hint}>Overlays from another dataset are placed by its WCS, converting celestial frames with astropy; its data are never resampled onto this map.</p>
    </div>
  );
}

function Row({ o, i, n, scene, geometry, nameOf, dispatch, onEdit, editing }: {
  o: SceneOverlay; i: number; n: number; scene: Scene; geometry: GeometryMap; nameOf: (id: number) => string; dispatch: (a: PaneAction) => void; onEdit: () => void; editing: boolean;
}) {
  const title = overlayTitle(o, scene, nameOf);
  const entry = geometry[o.id];
  const basis = o.visible || !needsGeometry(o, scene) ? overlayBasis(o, scene, entry, nameOf) : "Hidden.";
  const refused = o.visible && entry?.status === "refused";
  const computing = o.visible && needsGeometry(o, scene) && (!entry || entry.status === "loading");
  const removable = !BUILT_IN.has(o.id);
  const editable = removable && o.kind !== "regions" && o.kind !== "catalog";
  const color = (o as { color?: string }).color;
  return (
    <li className={s.row} data-overlay={o.id} aria-current={editing ? "true" : undefined}>
      <div className={s.head}>
        <Switch label={<span className={s.title}>{color ? <span className={s.swatch} style={{ background: `var(--overlay-${color})` }} aria-hidden="true" /> : null}{title}</span>}
          checked={o.visible} onChange={(e) => dispatch({ type: "updateOverlay", id: o.id, patch: { visible: e.target.checked } })} />
        <div className={s.tools}>
          <IconButton icon={ArrowUp} label={`Draw ${title} lower (earlier)`} disabled={i === 0} onClick={() => dispatch({ type: "moveOverlay", id: o.id, to: i - 1 })} />
          <IconButton icon={ArrowDown} label={`Draw ${title} higher (later)`} disabled={i === n - 1} onClick={() => dispatch({ type: "moveOverlay", id: o.id, to: i + 1 })} />
          {editable ? <IconButton icon={Pencil} label={`Edit ${title}`} onClick={onEdit} /> : null}
          {removable ? <IconButton icon={X} label={`Remove ${title}`} onClick={() => dispatch({ type: "removeOverlay", id: o.id })} /> : null}
        </div>
      </div>
      {computing ? <p className={s.basis} role="status">Computing its placement on this map…</p>
        : refused ? <p className={s.refused} role="status">{basis}</p>
          : basis ? <p className={s.basis}>{basis}</p> : null}
    </li>
  );
}

function OverlayForm({ draft, adding, datasets, baseId, problem, onChange, onApply, onCancel }: {
  draft: Draft; adding: boolean; datasets: Dataset[]; baseId: number; problem: string | null; onChange: (d: Draft) => void; onApply: () => void; onCancel: () => void;
}) {
  const set = (patch: Partial<Draft>) => onChange({ ...draft, ...patch });
  const pool = candidates(draft.kind, datasets, baseId);
  const ds = datasets.find((d) => d.id === (draft.kind === "grid" ? baseId : draft.datasetId));
  const hasRel = ds?.interpretation?.reliabilityId != null;
  const unit = ds ? unitOf(ds) : undefined;
  return (
    <form className={s.form} aria-label={adding ? "Add an overlay" : "Edit the overlay"} onSubmit={(e) => { e.preventDefault(); onApply(); }}>
      {adding ? (
        <Select label="Kind" value={draft.kind} onChange={(e) => {
          const kind = e.target.value as AddableKind;
          const next = candidates(kind, datasets, baseId);
          onChange({ ...emptyDraft(kind, next.some((d) => d.id === draft.datasetId) ? draft.datasetId : next[0]?.id ?? null) });
        }}>
          {KINDS.map((k) => <option key={k.id} value={k.id}>{k.label}</option>)}
        </Select>
      ) : null}
      {draft.kind !== "grid" ? (
        <Select label={draft.kind === "beam" ? "Beam of" : "From dataset"} value={draft.datasetId ?? ""} disabled={!adding} onChange={(e) => set({ datasetId: e.target.value ? Number(e.target.value) : null })}>
          {pool.length === 0 ? <option value="">{draft.kind === "orientation" ? "No other map is declared as an orientation map" : "No dataset"}</option> : null}
          {pool.map((d) => <option key={d.id} value={d.id}>{d.id === baseId ? `${d.name} (this map)` : d.name}</option>)}
        </Select>
      ) : null}
      {draft.kind === "contours" && ds ? (
        <>
          <Input label={`Channel (1 to ${channelsOf(ds)})`} inputMode="numeric" value={draft.channel} onChange={(e) => set({ channel: e.target.value })} />
          <Select label="Levels" value={draft.levelMode} onChange={(e) => set({ levelMode: e.target.value as Draft["levelMode"], values: e.target.value === "sigma" ? "3, 5, 10" : draft.levelMode === "sigma" ? "" : draft.values })}>
            <option value="sigma">Multiples of σ (robust, from the channel)</option>
            <option value="list">A list of values</option>
            <option value="linear">Start, step, count</option>
            <option value="geometric">Start, factor, count</option>
          </Select>
          {draft.levelMode === "sigma" || draft.levelMode === "list" ? (
            <Input label={draft.levelMode === "sigma" ? "Multiples of σ" : `Values${unit ? ` in ${unit}` : " (the file has no unit)"}`} value={draft.values} onChange={(e) => set({ values: e.target.value })} />
          ) : (
            <div className={s.fields}>
              <Input label={`Start${unit ? ` (${unit})` : ""}`} inputMode="decimal" value={draft.start} onChange={(e) => set({ start: e.target.value })} />
              {draft.levelMode === "linear"
                ? <Input label="Step" inputMode="decimal" value={draft.step} onChange={(e) => set({ step: e.target.value })} />
                : <Input label="Factor" inputMode="decimal" value={draft.factor} onChange={(e) => set({ factor: e.target.value })} />}
              <Input label="Count" inputMode="numeric" value={draft.count} onChange={(e) => set({ count: e.target.value })} />
            </div>
          )}
          <p className={s.hint}>{unit ? `Levels are in the file's unit, ${unit}.` : "The file states no unit: levels are plain numbers."}{draft.levelMode === "sigma" ? " σ is 1.4826 × the median absolute deviation of the channel's finite pixels, signal included." : ""}</p>
        </>
      ) : null}
      {draft.kind === "orientation" ? (
        <>
          <Select label="Spacing" value={draft.sampling} onChange={(e) => set({ sampling: e.target.value as Draft["sampling"] })}>
            <option value="auto">Lines a set distance apart on screen</option>
            <option value="step">Every n-th pixel of that map</option>
          </Select>
          {draft.sampling === "auto"
            ? <Input label="Screen pixels between lines" inputMode="numeric" value={draft.gapPx} onChange={(e) => set({ gapPx: e.target.value })} />
            : <Input label="Every n-th pixel" inputMode="numeric" value={draft.stepPx} onChange={(e) => set({ stepPx: e.target.value })} />}
          <div className={s.fields}>
            <Select label="Length" value={draft.length} disabled={!hasRel} onChange={(e) => set({ length: e.target.value as Draft["length"] })}>
              <option value="fixed">Fixed</option>
              <option value="reliability">By reliability</option>
            </Select>
            <Select label="Opacity" value={draft.opacity} disabled={!hasRel} onChange={(e) => set({ opacity: e.target.value as Draft["opacity"] })}>
              <option value="uniform">Uniform</option>
              <option value="reliability">By reliability</option>
            </Select>
          </div>
          <Input label="Only reliability at least (optional)" inputMode="decimal" value={draft.threshold} disabled={!hasRel} onChange={(e) => set({ threshold: e.target.value })} />
          {!hasRel ? <p className={s.hint}>That map declares no reliability map, so lines are drawn with fixed length and uniform opacity.</p> : null}
        </>
      ) : null}
      {draft.kind === "beam" ? (
        <Select label="Corner" value={draft.corner} onChange={(e) => set({ corner: e.target.value as Draft["corner"] })}>
          <option value="bottom-left">Bottom left</option>
          <option value="bottom-right">Bottom right</option>
          <option value="top-left">Top left</option>
          <option value="top-right">Top right</option>
        </Select>
      ) : null}
      {draft.kind === "grid" ? (
        <Select label="Grid frame" value={draft.frame} onChange={(e) => set({ frame: e.target.value as Draft["frame"] })}>
          <option value="icrs">ICRS (RA, Dec)</option>
          <option value="fk5">FK5 J2000 (RA, Dec)</option>
          <option value="galactic">Galactic (l, b)</option>
        </Select>
      ) : null}
      <div className={s.fields}>
        <Select label="Colour" value={draft.color} onChange={(e) => set({ color: e.target.value as Draft["color"] })}>
          {OVERLAY_COLOR_NAMES.map((c) => <option key={c} value={c}>{c}</option>)}
        </Select>
        <Select label="Width" value={draft.width} onChange={(e) => set({ width: e.target.value })}>
          <option value="">Default</option>
          {["0.5", "1", "1.5", "2", "3"].map((w) => <option key={w} value={w}>{w} px</option>)}
        </Select>
        <Select label="Line" value={draft.dash} onChange={(e) => set({ dash: e.target.value as Draft["dash"] })}>
          <option value="solid">Solid</option>
          <option value="dashed">Dashed</option>
        </Select>
      </div>
      {problem ? <p className={s.refused} role="alert">{problem}</p> : null}
      <div className={s.actions}>
        <Button type="submit" variant="primary" disabled={draft.kind !== "grid" && pool.length === 0}>{adding ? "Add" : "Apply"}</Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
    </form>
  );
}
