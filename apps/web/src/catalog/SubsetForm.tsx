import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { listSavedRegions } from "../api/regions";
import type { SavedRegion } from "../api/regions";
import type { SubsetInput, SubsetSaved } from "../api/subsets";
import { Button, Input, Select } from "../ui";
import { boxRegions, EMPTY_FIELDS, inputOf } from "./subsets";
import type { SubsetFields } from "./subsets";
import s from "./AnalysisPanel.module.css";

/**
 * The definition of a virtual subset (ADR 0063): a box (typed, or taken from a saved region), every N-th pixel, a spectral interval
 * (channels, or a velocity or frequency interval with its unit and frame) and a name and a reason. Numbers count from 1. Nothing is
 * written to a file: the server checks the window against the real file and keeps the definition.
 */
export function SubsetForm({ datasetId, initial = EMPTY_FIELDS, expectVersion, submitLabel, save, onSaved, onCancel }: {
  datasetId: number;
  initial?: SubsetFields;
  expectVersion?: number;
  submitLabel: string;
  save: (input: SubsetInput) => Promise<SubsetSaved>;
  onSaved: (r: SubsetSaved) => void;
  onCancel?: () => void;
}) {
  const [f, setF] = useState<SubsetFields>(initial);
  const [regions, setRegions] = useState<SavedRegion[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof SubsetFields>(k: K, v: SubsetFields[K]) => setF((cur) => ({ ...cur, [k]: v }));
  useEffect(() => {
    let live = true;
    listSavedRegions(datasetId).then((r) => live && setRegions(boxRegions(r))).catch(() => undefined);
    return () => { live = false; };
  }, [datasetId]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const r = inputOf(f, expectVersion);
    if ("problem" in r) { setErr(r.problem); return; }
    setBusy(true);
    setErr(null);
    try {
      onSaved(await save(r.input));
    } catch (x) {
      setErr(x instanceof Error ? x.message : "The subset was not saved.");
    }
    setBusy(false);
  }

  return (
    <form className={s.wrap} onSubmit={(e) => void submit(e)} aria-label="Virtual subset">
      <Input label="Name" value={f.name} onChange={(e) => set("name", e.target.value)} maxLength={80} autoComplete="off" />
      <Input label="Reason" value={f.reason} onChange={(e) => set("reason", e.target.value)} maxLength={500} autoComplete="off" hint="" />
      <p className={s.note}>The reason is kept with this version of the subset.</p>
      <Select label="Area" value={f.area} onChange={(e) => set("area", e.target.value as SubsetFields["area"])}>
        <option value="whole">The whole image</option>
        <option value="box">A box of pixels</option>
        <option value="region" disabled={regions.length === 0}>{regions.length === 0 ? "A saved region (none saved)" : "The box of a saved region"}</option>
      </Select>
      {f.area === "box" ? (
        <div className={s.two}>
          <Input label="x from" value={f.x0} onChange={(e) => set("x0", e.target.value)} inputMode="numeric" />
          <Input label="x to" value={f.x1} onChange={(e) => set("x1", e.target.value)} inputMode="numeric" />
          <Input label="y from" value={f.y0} onChange={(e) => set("y0", e.target.value)} inputMode="numeric" />
          <Input label="y to" value={f.y1} onChange={(e) => set("y1", e.target.value)} inputMode="numeric" />
        </div>
      ) : null}
      {f.area === "region" ? (
        <Select label="Region" value={f.regionId} onChange={(e) => set("regionId", e.target.value)}>
          <option value="">Choose a region…</option>
          {regions.map((r) => <option key={r.id} value={r.id}>{r.name} ({r.shape.shape})</option>)}
        </Select>
      ) : null}
      <Input label="Keep every N-th pixel" value={f.step} onChange={(e) => set("step", e.target.value)} inputMode="numeric" hint="" />
      <p className={s.note}>Values are not averaged: the pixels kept are the source's pixels, and the coordinates stay exact.</p>
      <Select label="Spectral range" value={f.spectral} onChange={(e) => set("spectral", e.target.value as SubsetFields["spectral"])}>
        <option value="all">All channels</option>
        <option value="channels">A range of channels</option>
        <option value="world">An interval in velocity or frequency</option>
      </Select>
      {f.spectral === "channels" ? (
        <div className={s.two}>
          <Input label="From channel" value={f.c0} onChange={(e) => set("c0", e.target.value)} inputMode="numeric" />
          <Input label="To channel" value={f.c1} onChange={(e) => set("c1", e.target.value)} inputMode="numeric" />
        </div>
      ) : null}
      {f.spectral === "world" ? (
        <>
          <div className={s.two}>
            <Input label="From" value={f.lo} onChange={(e) => set("lo", e.target.value)} inputMode="decimal" />
            <Input label="To" value={f.hi} onChange={(e) => set("hi", e.target.value)} inputMode="decimal" />
            <Input label="Unit" value={f.unit} onChange={(e) => set("unit", e.target.value)} spellCheck={false} />
            <Input label="Frame (optional)" value={f.frame} onChange={(e) => set("frame", e.target.value)} spellCheck={false} placeholder="as the file declares" />
          </div>
          <p className={s.note}>A channel is in when its centre lies in the interval. The unit must convert to the axis' own unit without assuming a convention, and a frame must be the one the file declares: none is converted.</p>
        </>
      ) : null}
      <div className={s.two}>
        <Button type="submit" variant="primary" disabled={busy}>{busy ? "Checking against the file…" : submitLabel}</Button>
        {onCancel ? <Button type="button" onClick={onCancel} disabled={busy}>Cancel</Button> : null}
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </form>
  );
}
