import { useState } from "react";
import type { Controls, ControlsDeclaration, Run } from "../api/runs";
import { setRunControls } from "../api/runs";
import { Button, Checkbox, Input, Select } from "../ui";
import { CONSTRUCTION_LABEL, KERNEL_LABEL, NOISE_LABEL, WIDTH_UNITS } from "./controlsText";
import { ControlsView } from "./ControlsView";
import s from "./RunPage.module.css";

type Draft = {
  smooth: boolean; kernel: string; width: string; unit: string; smoothNote: string;
  noise: boolean; noiseKind: string; sigma: string; sigmaUnit: string; estimated: string; noiseNote: string;
  construct: boolean; constrKind: string; params: string; constrNote: string;
};

function fromControls(c: Controls | null | undefined): Draft {
  return {
    smooth: !!c?.smoothing, kernel: c?.smoothing?.kernel ?? "gaussian_fwhm", width: c?.smoothing?.width != null ? String(c.smoothing.width) : "", unit: c?.smoothing?.unit ?? "", smoothNote: c?.smoothing?.note ?? "",
    noise: !!c?.noise, noiseKind: c?.noise?.kind ?? "scalar_sigma", sigma: c?.noise?.sigma != null ? String(c.noise.sigma) : "", sigmaUnit: c?.noise?.sigmaUnit ?? "", estimated: c?.noise?.estimated ?? "", noiseNote: c?.noise?.note ?? "",
    construct: !!c?.construction, constrKind: c?.construction?.kind ?? "shifted_map_null", params: (c?.construction?.params ?? []).map((p) => `${p.key} = ${p.value}`).join("\n"), constrNote: c?.construction?.note ?? "",
  };
}

/** Number as typed, or null for empty; NaN for something that is not a number (the server refuses it, the form says so first). */
const num = (v: string): number | null => (v.trim() === "" ? null : Number(v.trim()));

/** The request body for the draft, or the problem with it. Nothing is defaulted: a width needs its unit, a σ its unit. */
export function declarationOf(d: Draft): { body: ControlsDeclaration } | { error: string } {
  const body: ControlsDeclaration = {};
  if (d.smooth) {
    const w = num(d.width);
    if (w !== null && !(w > 0)) return { error: "The smoothing width must be a positive number." };
    if (w !== null && !d.unit) return { error: "Choose the unit of the smoothing width." };
    if (w === null && d.unit) return { error: "A unit without a width: enter the width, or clear the unit." };
    body.smoothing = { kernel: d.kernel as never, width: w, ...(w !== null ? { unit: d.unit as never } : {}), note: d.smoothNote.trim() };
  } else body.smoothing = null;
  if (d.noise) {
    const sg = num(d.sigma);
    if (d.noiseKind !== "scalar_sigma" && sg !== null) return { error: "A σ value goes with the one-σ model only." };
    if (sg !== null && !(sg > 0)) return { error: "σ must be a positive number." };
    if (sg !== null && !d.sigmaUnit.trim()) return { error: "Enter the unit of σ (for example K or Jy/beam)." };
    body.noise = { kind: d.noiseKind as never, sigma: sg, ...(sg !== null ? { sigmaUnit: d.sigmaUnit.trim() } : {}), estimated: d.estimated.trim(), note: d.noiseNote.trim() };
  } else body.noise = null;
  if (d.construct) {
    const params: { key: string; value: string }[] = [];
    for (const line of d.params.split("\n")) {
      if (!line.trim()) continue;
      const i = line.indexOf("=");
      if (i < 1) return { error: `Parameters are written name = value, one per line: “${line.trim()}”.` };
      params.push({ key: line.slice(0, i).trim(), value: line.slice(i + 1).trim() });
    }
    body.construction = { kind: d.constrKind as never, params, note: d.constrNote.trim() };
  } else body.construction = null;
  return { body };
}

/** What was stated about how a run was smoothed, how its noise was modelled and how a control was built, with a form to state it. Stored as declared by you; nothing here is read from the files. */
export function ControlsEditor({ run, onSaved }: { run: Run; onSaved: (r: Run) => void }) {
  const [editing, setEditing] = useState(false);
  const [d, setD] = useState<Draft>(() => fromControls(run.controls));
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const stated = !!run.controls && (!!run.controls.smoothing || !!run.controls.noise || !!run.controls.construction);

  async function save() {
    const r = declarationOf(d);
    if ("error" in r) { setErr(r.error); return; }
    setBusy(true);
    setErr(null);
    try {
      onSaved(await setRunControls(run.id, r.body));
      setEditing(false);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not save.");
    } finally {
      setBusy(false);
    }
  }
  const set = (p: Partial<Draft>) => setD((x) => ({ ...x, ...p }));

  if (!editing) {
    return (
      <>
        {stated ? <ControlsView controls={run.controls} /> : <p className={s.block}>Nothing is stated about smoothing, the noise model or how a control was built. That is not the same as none being used.</p>}
        <div className={s.actions}>
          <Button type="button" onClick={() => { setD(fromControls(run.controls)); setErr(null); setEditing(true); }}>{stated ? "Edit what you state" : "State controls"}</Button>
        </div>
      </>
    );
  }
  return (
    <form className={s.block} aria-label="State the controls" onSubmit={(e) => { e.preventDefault(); if (!busy) void save(); }}>
      <p className={s.note}>You are the author of what you state here. It is stored as declared by you, shown on the run page and carried to a reviewer with the run. A block you leave off is not stated.</p>
      <Checkbox label="State the smoothing" checked={d.smooth} onChange={(e) => set({ smooth: e.target.checked })} />
      {d.smooth ? (
        <div className={s.row}>
          <Select label="Kernel" value={d.kernel} onChange={(e) => set({ kernel: e.target.value })}>
            {Object.entries(KERNEL_LABEL).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </Select>
          <Input label="Width" value={d.width} onChange={(e) => set({ width: e.target.value })} inputMode="decimal" spellCheck={false} />
          <Select label="Unit of the width" value={d.unit} onChange={(e) => set({ unit: e.target.value })}>
            <option value="">(none: width not stated)</option>
            {WIDTH_UNITS.map((u) => <option key={u} value={u}>{u}</option>)}
          </Select>
          <Input label={d.kernel === "other" ? "Describe the kernel" : "Note (optional)"} value={d.smoothNote} onChange={(e) => set({ smoothNote: e.target.value })} maxLength={300} />
        </div>
      ) : null}
      <Checkbox label="State the noise model" checked={d.noise} onChange={(e) => set({ noise: e.target.checked })} />
      {d.noise ? (
        <div className={s.row}>
          <Select label="Noise model" value={d.noiseKind} onChange={(e) => set({ noiseKind: e.target.value })}>
            {Object.entries(NOISE_LABEL).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </Select>
          {d.noiseKind === "scalar_sigma" ? (
            <>
              <Input label="σ" value={d.sigma} onChange={(e) => set({ sigma: e.target.value })} inputMode="decimal" spellCheck={false} />
              <Input label="Unit of σ" value={d.sigmaUnit} onChange={(e) => set({ sigmaUnit: e.target.value })} spellCheck={false} placeholder="K" />
            </>
          ) : null}
          <Input label="How the noise was estimated" value={d.estimated} onChange={(e) => set({ estimated: e.target.value })} maxLength={300} placeholder="robust MAD of line-free channels" />
          <Input label={d.noiseKind === "other" ? "Describe the model" : "Note (optional)"} value={d.noiseNote} onChange={(e) => set({ noiseNote: e.target.value })} maxLength={300} />
        </div>
      ) : null}
      <Checkbox label="State how a control or null was built" checked={d.construct} onChange={(e) => set({ construct: e.target.checked })} />
      {d.construct ? (
        <div className={s.row}>
          <Select label="Construction" value={d.constrKind} onChange={(e) => set({ constrKind: e.target.value })}>
            {Object.entries(CONSTRUCTION_LABEL).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </Select>
          <label className={s.note}>
            Parameters (name = value, one per line)
            <textarea className={s.area} rows={3} value={d.params} onChange={(e) => set({ params: e.target.value })} spellCheck={false} placeholder={"n_shifts = 1000\nseed = 5"} />
          </label>
          <Input label={d.constrKind === "other" ? "Describe the construction" : "Note (optional)"} value={d.constrNote} onChange={(e) => set({ constrNote: e.target.value })} maxLength={300} />
        </div>
      ) : null}
      <div className={s.row}>
        <Button type="submit" variant="primary" disabled={busy}>Save</Button>
        <Button type="button" onClick={() => setEditing(false)} disabled={busy}>Cancel</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </form>
  );
}
