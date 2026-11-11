import { useState } from "react";
import type { FormEvent } from "react";
import { Button, Checkbox, Input, Select } from "../ui";
import { TierBadge } from "../ui/TierBadge";
import { createMask, forgetMask, maskTypeLabel } from "../api/masks";
import type { CreateMask, Mask } from "../api/masks";
import type { Dataset } from "../api/catalog";
import { isCatalog } from "../api/catalog";
import { listSavedRegions, isLine } from "../api/regions";
import type { SavedRegion } from "../api/regions";
import { masksChanged, setMaskShown, useMasks, useShownMasks } from "../viewer/useMasks";
import { HATCH } from "../viewer/maskHatch";
import s from "./AnalysisPanel.module.css";

type Method = "low_snr" | "saturated" | "regions" | "import" | "missing";

const ruleText = (m: Mask): string => {
  const r = m.rule as { rule?: string; sigma?: { source?: string; name?: string }; source?: string; grid?: string; note?: string };
  return [r.rule, r.sigma?.source, r.source ? `threshold from: ${r.source}` : "", r.grid].filter(Boolean).join(". ");
};

/**
 * Typed masks of a dataset (ADR 0055). A mask flags pixels for a stated reason and changes nothing in the data. Making one is a
 * stated rule (a threshold, regions, a file you provide); the no-value mask is computed from the data. Showing one hatches it on the
 * plane; applying it is decided per analysis, per mask. Zero is a value: no mask here flags it unless its rule says so.
 */
export function MasksPanel({ dataset, datasets }: { dataset: Dataset; datasets: Dataset[] }) {
  const local = dataset.hostId === 0 && !isCatalog(dataset);
  const { masks, error } = useMasks(local ? dataset.id : null);
  const shown = useShownMasks(dataset.id);
  const [method, setMethod] = useState<Method>("low_snr");
  const [f, setF] = useState<Record<string, string>>({ sigmaKind: "mad", k: "3", nonzero: "flagged", type: "invalid" });
  const [regs, setRegs] = useState<SavedRegion[] | null>(null);
  const [picked, setPicked] = useState<number[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (k: string, v: string) => setF({ ...f, [k]: v });
  if (!local) return <p className={s.note}>Masks are made on this computer, for an image read here.</p>;
  const sameHost = datasets.filter((d) => d.id !== dataset.id && d.hostId === 0 && !isCatalog(d) && !d.archived);

  async function loadRegions() {
    setRegs((await listSavedRegions(dataset.id)).filter((r) => !isLine(r)));
  }
  function request(): CreateMask | string {
    const name = f.name?.trim() || undefined;
    if (method === "missing") return { method, name };
    if (method === "low_snr") {
      const k = Number(f.k);
      if (!(k > 0)) return "The threshold k must be a positive number.";
      if (f.sigmaKind === "value" && !(Number(f.sigma) > 0)) return "Sigma must be a positive number.";
      if (f.sigmaKind === "map" && !f.sigmaMap) return "Choose the noise map.";
      return { method, name, k, sigma: f.sigmaKind === "value" ? { kind: "value", value: Number(f.sigma) } : f.sigmaKind === "map" ? { kind: "map", datasetId: Number(f.sigmaMap) } : { kind: "mad" } };
    }
    if (method === "saturated") {
      if (f.threshold?.trim() === "" || f.threshold === undefined || !Number.isFinite(Number(f.threshold))) return "Give the saturation threshold.";
      if (!f.source?.trim()) return "Say where the threshold comes from.";
      return { method, name, threshold: Number(f.threshold), source: f.source.trim() };
    }
    if (method === "regions") return picked.length ? { method, name, regionIds: picked } : "Choose at least one saved region.";
    if (!f.importId) return "Choose the mask file (a registered image on the same grid).";
    if (f.type === "other" && !f.meaning?.trim()) return "A mask of type other needs a description of what it flags.";
    return { method, name, type: f.type as CreateMask["type"], importDatasetId: Number(f.importId), nonzero: f.nonzero as "flagged" | "valid", meaning: f.meaning?.trim() || undefined };
  }
  async function submit(e: FormEvent) {
    e.preventDefault();
    const body = request();
    if (typeof body === "string") { setErr(body); return; }
    setBusy(true);
    setErr(null);
    try {
      const m = await createMask(dataset.id, body);
      masksChanged();
      setMaskShown(dataset.id, m.id, true);
    } catch (x) {
      setErr(x instanceof Error ? x.message : "The mask could not be made.");
    }
    setBusy(false);
  }
  async function forget(m: Mask) {
    setErr(null);
    try { await forgetMask(m.id); setMaskShown(dataset.id, m.id, false); masksChanged(); } catch (x) { setErr(x instanceof Error ? x.message : "The mask could not be forgotten."); }
  }

  return (
    <div className={s.wrap}>
      <p className={s.note}>A mask flags pixels for a stated reason and changes nothing in the data. Pixels with no value are their own type, computed from the file; a value of zero is a value and is never flagged as missing. Each analysis says what to do with each mask.</p>
      {masks === null ? <p className={s.note}>Reading the masks…</p> : masks.length === 0 ? <p className={s.note}>No mask yet.</p> : (
        <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "grid", gap: "var(--sp-3)" }} aria-label="Masks">
          {masks.map((m) => (
            <li key={m.id} data-testid="mask-row" style={{ display: "grid", gap: "var(--sp-1)" }}>
              <Checkbox label={`Show ${m.name}`} checked={shown.includes(m.id)} onChange={(e) => setMaskShown(dataset.id, m.id, e.target.checked)} />
              <span className={s.note}>
                {maskTypeLabel(m.type)} ({HATCH[m.type].label}) · <TierBadge tier={m.tier} mappingVersion={m.mappingVersion} /> · {m.flagged.toLocaleString("en-US")} of {m.total.toLocaleString("en-US")} pixels
                {m.channels > 0 ? `, ${m.channels} channels` : ", every channel"}
              </span>
              <span className={s.note}>{ruleText(m)}{m.meaning ? ` Meaning: ${m.meaning}` : ""}</span>
              {m.stale ? <span role="status" className={s.note}>The file changed since this mask was made from it: it is not applied until you make it again.</span> : null}
              <div><Button type="button" onClick={() => void forget(m)}>Forget this mask</Button> <span className={s.note}>(its file stays on disk)</span></div>
            </li>
          ))}
        </ul>
      )}
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      <form className={s.wrap} onSubmit={(e) => void submit(e)} aria-label="New mask">
        <Select label="Make a mask" value={method} onChange={(e) => { setMethod(e.target.value as Method); if (e.target.value === "regions" && regs === null) void loadRegions(); }}>
          <option value="low_snr">Low signal-to-noise: value / σ below k</option>
          <option value="saturated">Saturated: value at or above a threshold</option>
          <option value="regions">Excluded by you: from saved regions</option>
          <option value="import">From a mask file you provide</option>
          <option value="missing">No value (NaN, BLANK): computed from the data</option>
        </Select>
        <Input label="Name (optional)" value={f.name ?? ""} onChange={(e) => set("name", e.target.value)} maxLength={80} />
        {method === "low_snr" ? (
          <>
            <Input label="Threshold k (flag where value / σ < k)" value={f.k ?? ""} onChange={(e) => set("k", e.target.value)} inputMode="decimal" />
            <Select label="σ comes from" value={f.sigmaKind ?? "mad"} onChange={(e) => set("sigmaKind", e.target.value)}>
              <option value="mad">A robust estimate: 1.4826 × MAD of each channel (includes the signal)</option>
              <option value="value">A value you state</option>
              <option value="map">A noise map you declare</option>
            </Select>
            {f.sigmaKind === "value" ? <Input label="σ (in the unit of the data)" value={f.sigma ?? ""} onChange={(e) => set("sigma", e.target.value)} inputMode="decimal" /> : null}
            {f.sigmaKind === "map" ? (
              <Select label="Noise map" value={f.sigmaMap ?? ""} onChange={(e) => set("sigmaMap", e.target.value)}>
                <option value="">Choose a file…</option>
                {sameHost.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
              </Select>
            ) : null}
            <p className={s.note}>A pixel with no value, or whose σ is unknown, zero or negative, is not flagged here: it has no ratio.</p>
          </>
        ) : null}
        {method === "saturated" ? (
          <>
            <Input label="Threshold (flag where value ≥ it)" value={f.threshold ?? ""} onChange={(e) => set("threshold", e.target.value)} inputMode="decimal" />
            <Input label="Where the threshold comes from" value={f.source ?? ""} onChange={(e) => set("source", e.target.value)} maxLength={200} />
          </>
        ) : null}
        {method === "regions" ? (
          <fieldset style={{ border: 0, padding: 0, margin: 0, display: "grid", gap: "var(--sp-1)" }}>
            <legend className={s.note}>Saved regions to exclude (every channel; pixel centres inside)</legend>
            {regs === null ? <Button type="button" onClick={() => void loadRegions()}>Load the saved regions</Button> : regs.length === 0 ? <span className={s.note}>No saved rectangle or circle for this dataset yet. Draw one in the viewer and save it.</span> : regs.map((r) => (
              <Checkbox key={r.id} label={`${r.name} (${r.shape.shape})`} checked={picked.includes(r.id)} onChange={(e) => setPicked(e.target.checked ? [...picked, r.id] : picked.filter((x) => x !== r.id))} />
            ))}
          </fieldset>
        ) : null}
        {method === "import" ? (
          <>
            <Select label="Mask file (a registered image)" value={f.importId ?? ""} onChange={(e) => set("importId", e.target.value)}>
              <option value="">Choose a file…</option>
              {sameHost.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
            </Select>
            <Select label="What its pixels flag" value={f.type ?? "invalid"} onChange={(e) => set("type", e.target.value)}>
              <option value="invalid">Invalid pixels</option>
              <option value="low_snr">Low signal-to-noise</option>
              <option value="saturated">Saturated</option>
              <option value="user_excluded">Excluded by you</option>
              <option value="other">Other (describe it)</option>
            </Select>
            <Select label="A nonzero pixel of the file means" value={f.nonzero ?? "flagged"} onChange={(e) => set("nonzero", e.target.value)}>
              <option value="flagged">flagged (zero is not flagged)</option>
              <option value="valid">valid (zero is what is flagged)</option>
            </Select>
            <Input label={f.type === "other" ? "What it flags (required)" : "Note (optional)"} value={f.meaning ?? ""} onChange={(e) => set("meaning", e.target.value)} maxLength={300} />
          </>
        ) : null}
        <div><Button type="submit" variant="primary" disabled={busy}>{busy ? "Working…" : "Make the mask"}</Button></div>
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
      </form>
    </div>
  );
}
