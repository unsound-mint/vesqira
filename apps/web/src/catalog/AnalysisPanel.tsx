import { useEffect, useState } from "react";
import { Button, Input, Select } from "../ui";
import { createAnalysisJob, getDerived, runAnalysis } from "../api/analysis";
import type { AnalysisOp, Derived, StatsSummary } from "../api/analysis";
import type { Dataset } from "../api/catalog";
import type { Job } from "../api/jobs";
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
  const remote = dataset.hostId !== 0;
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
    setErr(null);
    setStats(null);
    try {
      const req = { op, datasetId: dataset.id, referenceId: ref ? Number(ref) : undefined, params: params() };
      if (remote) onJob(await createAnalysisJob(req));
      else {
        const r = await runAnalysis(req);
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
      <Button variant="primary" disabled={busy || (op === "align" && !ref) || (op === "convolve" && !(Number(v.tmaj) > 0 && Number(v.tmin) > 0))} onClick={go}>
        {busy ? "Working…" : remote ? "Create a job on the host" : op === "stats" ? "Compute statistics" : "Create the new file"}
      </Button>
      {remote ? <p className={s.note}>This file lives on a host. The analysis becomes a Slurm job whose script you read and submit; the result stays on the host and is registered afterwards.</p> : null}
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
        <dt>Pixels with a value</dt>
        <dd className="mono">{st.finite.toLocaleString("en-US")}</dd>
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
        <dt>Software</dt>
        <dd className="mono">{Object.entries(d.software).map(([k, x]) => `${k} ${x}`).join(", ")}</dd>
      </dl>
      {d.summary.policy ? <p className={s.note}>{d.summary.policy}</p> : null}
    </div>
  );
}
