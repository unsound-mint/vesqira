import { useEffect, useState } from "react";
import { Button, Input, Select } from "../ui";
import { QueueNote, useQueueReason } from "../ui/QueueNote";
import { newRequestId } from "../api/http";
import { createAnalysisJob, getDerived, runAnalysis } from "../api/analysis";
import type { AnalysisOp, Derived, StatsSummary } from "../api/analysis";
import type { Dataset } from "../api/catalog";
import type { Job } from "../api/jobs";
import { ControlsView } from "../runs/ControlsView";
import { MaskPolicies, badWeight, usesOf } from "./MaskPolicies";
import type { PolicyChoice } from "./MaskPolicies";
import { useMasks } from "../viewer/useMasks";
import { TierBadge } from "../ui/TierBadge";
import { MaskCarry, carryChoices, carryUndecided } from "./MaskCarry";
import type { CarryOp, CarryState } from "./MaskCarry";
import { maskTypeLabel } from "../api/masks";
import { EM } from "./format";
import { beamText } from "./resolution";
import s from "./AnalysisPanel.module.css";

const OPS: { id: AnalysisOp; label: string; help: string }[] = [
  { id: "stats", label: "Statistics", help: "Counts, mean, spread, noise per channel and the peak. NaN and BLANK are counted, never treated as zero." },
  { id: "moment", label: "Moment map", help: "Moment 0 (integrated), 1 (velocity centre) or 2 (dispersion) over a channel range. A pixel with nothing selected is NaN, never 0." },
  { id: "cutout", label: "Spatial cutout", help: "A new cube of a pixel box; the coordinates stay exact." },
  { id: "slab", label: "Spectral slab", help: "A new cube of a channel range; the spectral axis stays exact." },
  { id: "align", label: "Align to another map", help: "Reproject onto another file's celestial grid (astropy reproject). It interpolates the values and does not match angular resolution; outside the footprint is NaN." },
  { id: "convolve", label: "Match to a larger beam", help: "Smooth to a target beam with the Gaussian that, with the current beam, gives it. Needs a declared beam and a target at least as large in every direction. Missing pixels stay missing; noise becomes correlated; Jy/beam is rescaled by the beam-area ratio." },
];

const fmt = (v: number | null | undefined) => (v === null || v === undefined ? EM : Number(v.toPrecision(6)).toString());

/** New files from a cube: statistics, moments, cutouts, slabs, and alignment. Results are new datasets with their provenance. */
export function AnalysisPanel({ dataset, datasets, channels, onCreated, onJob }: { dataset: Dataset; datasets: Dataset[]; channels: number; onCreated: (d: Dataset) => void; onJob: (j: Job) => void }) {
  const [op, setOp] = useState<AnalysisOp>("moment");
  const [v, setV] = useState<Record<string, string>>({ order: "0" });
  const [ref, setRef] = useState("");
  const [stats, setStats] = useState<StatsSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [rid, setRid] = useState<string | null>(null);
  const queued = useQueueReason(busy, rid);
  const remote = dataset.hostId !== 0;
  const takesMasks = op === "stats" || op === "moment"; // on a host too: the job applies the masks kept next to the file (ADR 0064)
  const cutsOrResamples = op === "cutout" || op === "slab" || op === "align" || op === "convolve";
  const carryOp: CarryOp | null = cutsOrResamples ? op : null; // on a host too: the job writes the masks beside its result (ADR 0064)
  const { masks } = useMasks(takesMasks || cutsOrResamples ? dataset.id : null);
  const [policy, setPolicy] = useState<PolicyChoice>({});
  const [carry, setCarry] = useState<CarryState>({});
  const carryRequired = op === "align" || op === "convolve"; // a resampling or a smoothing wants every mask decided; a cut leaves behind what is not chosen
  useEffect(() => setCarry({}), [dataset.id, op]);
  const set = (k: string, val: string) => setV({ ...v, [k]: val });
  const info = OPS.find((o) => o.id === op)!;
  const candidates = datasets.filter((d) => d.id !== dataset.id && d.hostId === dataset.hostId);

  function params(): Record<string, string> {
    const p: Record<string, string> = {};
    const num = (k: string, name = k) => { if (v[k]?.trim()) p[name] = String(v[k]!.trim()); };
    if (op === "moment") { num("order"); num("threshold"); if (v.c0?.trim()) p.chanStart = String(Number(v.c0) - 1); if (v.c1?.trim()) p.chanEnd = String(Number(v.c1) - 1); }
    if (op === "slab") { p.chanStart = String(Number(v.c0 ?? "1") - 1); p.chanEnd = String(Number(v.c1 ?? String(channels)) - 1); }
    if (op === "cutout") { p.x0 = String(Number(v.x0 ?? "1") - 1); p.x1 = String(Number(v.x1 ?? "1") - 1); p.y0 = String(Number(v.y0 ?? "1") - 1); p.y1 = String(Number(v.y1 ?? "1") - 1); }
    if (op === "stats" && v.c0?.trim()) { p.chanStart = String(Number(v.c0) - 1); if (v.c1?.trim()) p.chanEnd = String(Number(v.c1) - 1); }
    if (op === "align") p.method = v.method || "interp";
    if (op === "convolve") { p.targetBmaj = v.tmaj ?? ""; p.targetBmin = v.tmin ?? ""; p.targetBpa = v.tpa || "0"; }
    return p;
  }

  async function go() {
    setBusy(true);
    const id = newRequestId();
    setRid(id);
    setErr(null);
    setStats(null);
    try {
      const uses = takesMasks ? usesOf(policy) : [];
      const choices = carryOp && masks ? carryChoices(masks, carry, carryRequired) : [];
      const req = { op, datasetId: dataset.id, referenceId: ref ? Number(ref) : undefined, params: params(), ...(uses.length ? { masks: uses } : {}), ...(choices.length ? { carryMasks: choices } : {}) };
      if (remote) onJob(await createAnalysisJob(req));
      else {
        const r = await runAnalysis(req, undefined, id);
        if (op === "stats") setStats(r.summary as StatsSummary);
        else if (r.dataset) onCreated(r.dataset);
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "The analysis did not work.");
    }
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      <Select label="Analysis" value={op} onChange={(e) => { setOp(e.target.value as AnalysisOp); setStats(null); }}>
        {OPS.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
      </Select>
      <p className={s.note}>{info.help}</p>
      {op === "moment" ? (
        <>
          <Select label="Moment" value={v.order ?? "0"} onChange={(e) => set("order", e.target.value)}>
            <option value="0">0: integrated intensity</option>
            <option value="1">1: velocity centre</option>
            <option value="2">2: dispersion</option>
          </Select>
          <Input label="Only values at or above (optional)" value={v.threshold ?? ""} onChange={(e) => set("threshold", e.target.value)} placeholder="e.g. 3 × noise" />
        </>
      ) : null}
      {op === "moment" || op === "slab" || op === "stats" ? (
        <div className={s.two}>
          <Input label="From channel" value={v.c0 ?? ""} onChange={(e) => set("c0", e.target.value)} placeholder="1" inputMode="numeric" />
          <Input label="To channel" value={v.c1 ?? ""} onChange={(e) => set("c1", e.target.value)} placeholder={String(channels)} inputMode="numeric" />
        </div>
      ) : null}
      {op === "cutout" ? (
        <div className={s.two}>
          <Input label="x from" value={v.x0 ?? ""} onChange={(e) => set("x0", e.target.value)} inputMode="numeric" />
          <Input label="x to" value={v.x1 ?? ""} onChange={(e) => set("x1", e.target.value)} inputMode="numeric" />
          <Input label="y from" value={v.y0 ?? ""} onChange={(e) => set("y0", e.target.value)} inputMode="numeric" />
          <Input label="y to" value={v.y1 ?? ""} onChange={(e) => set("y1", e.target.value)} inputMode="numeric" />
        </div>
      ) : null}
      {op === "align" ? (
        <>
          <Select label="Align onto the grid of" value={ref} onChange={(e) => setRef(e.target.value)}>
            <option value="">Choose a file…</option>
            {candidates.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </Select>
          <Select label="Method" value={v.method ?? "interp"} onChange={(e) => set("method", e.target.value)}>
            <option value="interp">Bilinear interpolation</option>
            <option value="nearest">Nearest neighbour</option>
            <option value="exact">Flux-conserving (slow)</option>
          </Select>
        </>
      ) : null}
      {op === "convolve" ? (
        <>
          <p className={s.note}>Current beam: {beamText(dataset)}</p>
          <>
            <Input label="Target major FWHM (arcsec)" value={v.tmaj ?? ""} onChange={(e) => set("tmaj", e.target.value)} inputMode="decimal" />
            <Input label="Target minor FWHM (arcsec)" value={v.tmin ?? ""} onChange={(e) => set("tmin", e.target.value)} inputMode="decimal" />
            <Input label="Position angle (deg, north through east)" value={v.tpa ?? "0"} onChange={(e) => set("tpa", e.target.value)} inputMode="decimal" />
          </>
        </>
      ) : null}
      {takesMasks && masks && masks.length > 0 ? <MaskPolicies masks={masks} choice={policy} onChange={setPolicy} /> : null}
      {carryOp && masks && masks.length > 0 ? <MaskCarry masks={masks} state={carry} onChange={setCarry} op={carryOp} required={carryRequired} /> : null}
      <Button variant="primary" disabled={busy || badWeight(policy) || (carryOp !== null && !!masks && carryUndecided(masks, carry, carryRequired)) || (op === "align" && !ref) || (op === "convolve" && !(Number(v.tmaj) > 0 && Number(v.tmin) > 0))} onClick={go}>
        {busy ? "Working…" : remote ? "Create a job on the host" : op === "stats" ? "Compute statistics" : "Create the new file"}
      </Button>
      <QueueNote reason={queued} />
      {remote ? <p className={s.note}>This file lives on a host. The analysis becomes a Slurm job whose script you read and submit; the result stays on the host and is registered afterwards{carryOp && masks && masks.length > 0 ? ", with the masks you chose to carry (the job writes them beside its result; none is recorded if a file is missing)" : ""}.</p> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {stats ? <StatsView st={stats} /> : null}
    </div>
  );
}

export function StatsView({ st }: { st: StatsSummary }) {
  return (
    <div className={s.stats} role="status">
      <dl className={s.facts}>
        <dt>Channels</dt>
        <dd className="mono">{st.channels[0] + 1} to {st.channels[1] + 1}</dd>
        <dt>Pixels used</dt>
        <dd className="mono">{st.finite.toLocaleString("en-US")}{st.excluded_by_mask ? ` (${st.excluded_by_mask.toLocaleString("en-US")} more left out by masks)` : ""}</dd>
        <dt>No value (NaN or BLANK)</dt>
        <dd className="mono">{st.no_value.toLocaleString("en-US")}</dd>
        <dt>Mean</dt>
        <dd className="mono">{fmt(st.mean)}</dd>
        <dt>Standard deviation</dt>
        <dd className="mono">{fmt(st.std)}</dd>
        <dt>Minimum, maximum</dt>
        <dd className="mono">{fmt(st.min)}, {fmt(st.max)}</dd>
        <dt>Peak</dt>
        <dd className="mono">{st.peak ? `${fmt(st.peak.value)} at x ${st.peak.x + 1}, y ${st.peak.y + 1}, channel ${st.peak.channel + 1}` : EM}</dd>
      </dl>
      {st.masks && st.masks.length > 0 ? (
        <ul className={s.note} aria-label="Masks applied" data-testid="stats-masks">
          {st.masks.map((m) => (
            <li key={m.id}>{maskTypeLabel(m.type as never)} mask “{m.name}”: policy {m.policy}{m.weight !== undefined ? ` (factor ${m.weight})` : ""}; {m.pixels_flagged_with_value.toLocaleString("en-US")} pixels with a value flagged, {m.pixels_excluded.toLocaleString("en-US")} left out. {m.tier ? <TierBadge tier={m.tier} /> : null}</li>
          ))}
        </ul>
      ) : null}
      <p className={s.note}>{st.policy}. Noise per channel (robust, from the median absolute deviation) is in the result for each of the {st.per_channel.length} channels.</p>
    </div>
  );
}

/** How a derived dataset was made: operation, options, sources and the policy it kept. */
export function DerivedPanel({ dataset, datasets }: { dataset: Dataset; datasets: Dataset[] }) {
  const [d, setD] = useState<Derived | null>(null);
  useEffect(() => {
    setD(null);
    getDerived(dataset.id).then(setD).catch(() => setD(null));
  }, [dataset.id]);
  if (!d) return null;
  const names = d.sources.map((id) => datasets.find((x) => x.id === id)?.name ?? `file ${id}`);
  return (
    <div className={s.wrap}>
      <dl className={s.facts}>
        <dt>Made by</dt>
        <dd>{d.op === "managed-copy" ? "managed copy (byte for byte)" : d.op === "transfer" ? "downloaded copy (the host file is an external reference)" : d.op}</dd>
        <dt>From</dt>
        <dd className="mono">{names.join(", ")}</dd>
        <dt>Options</dt>
        <dd className="mono">{Object.entries(d.params.options ?? {}).map(([k, x]) => `${k}=${x}`).join(" ") || EM}</dd>
        {d.params.controls ? (
          <>
            <dt>Controls</dt>
            <dd><ControlsView controls={d.params.controls} /></dd>
          </>
        ) : null}
        {d.params.masks && d.params.masks.length > 0 ? (
          <>
            <dt>Masks applied</dt>
            <dd data-testid="derived-masks">{d.params.masks.map((m, i) => <div key={i}>{maskTypeLabel(m.type as never)} “{m.name}”: {m.policy}{m.weight !== undefined ? ` ×${m.weight}` : ""} <TierBadge tier={m.tier} mappingVersion={m.mappingVersion} /></div>)}</dd>
          </>
        ) : null}
        {d.params.maskCarry ? (
          <>
            <dt>Masks of the source</dt>
            <dd data-testid="derived-mask-carry">
              {d.params.maskCarry.carried.map((m, i) => <div key={`c${i}`}>{maskTypeLabel(m.type as never)} “{m.name}”: carried by {d.params.maskCarry!.by} <TierBadge tier={m.tier} mappingVersion={m.mappingVersion} /></div>)}
              {d.params.maskCarry.leftBehind.map((m, i) => <div key={`l${i}`}>{maskTypeLabel(m.type as never)} “{m.name}”: left behind <TierBadge tier={m.tier} mappingVersion={m.mappingVersion} /></div>)}
              {d.params.maskCarry.recomputed.length > 0 ? <div>No-value mask: computed again from this file’s values, not carried</div> : null}
            </dd>
          </>
        ) : null}
        <dt>Software</dt>
        <dd className="mono">{Object.entries(d.software).map(([k, x]) => `${k} ${x}`).join(", ")}</dd>
      </dl>
      {d.summary.policy ? <p className={s.note}>{d.summary.policy}</p> : null}
    </div>
  );
}
