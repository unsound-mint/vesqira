import { useEffect, useMemo, useState } from "react";
import { Button, Checkbox, Input, Select } from "../ui";
import { newRequestId } from "../api/http";
import { QueueNote, useQueueReason } from "../ui/QueueNote";
import { listFamilyCandidates, runFamily } from "../api/family";
import type { FamilyCandidate, FamilyResult } from "../api/family";
import { getPlane } from "../api/planes";
import type { Plane } from "../api/planes";
import { isImage } from "../api/catalog";
import type { Dataset } from "../api/catalog";
import { lut } from "../viewer/colormaps";
import type { RampId } from "../viewer/colormaps";
import { rampCss } from "../viewer/channelGrid";
import { axialDegrees } from "../viewer/render";
import { FamilyMap } from "./FamilyMap";
import { FamilyMapsCleanup } from "./FamilyMapsCleanup";
import type { Cursor } from "./FamilyMap";
import { AXIAL_DIFFERENCE_RANGE, optionLabel, sharedRange, spreadRange, symmetricRange, valueText } from "./familyScale";
import { EM } from "../catalog/format";
import s from "./Family.module.css";

/** Members chosen elsewhere (a sweep, a recognised family) and the names to show for them. */
export type FamilyPreset = { nonce: number; ids: number[]; labels?: Record<number, string>; referenceId?: number; title?: string; excluded?: { label: string; reason: string }[] };

const MAX = 12;
const num = (v: number | null | undefined, digits = 4) => (v === null || v === undefined ? EM : String(Number(v.toPrecision(digits))));

/** Maps that differ in one parameter, side by side on one scale, with how well they agree (ADR 0070). Nothing is resampled: maps on different grids are refused. */
export function FamilyView({ datasets, preset, onCatalogChanged }: { datasets: Dataset[]; preset: FamilyPreset | null; onCatalogChanged?: () => void }) {
  const images = useMemo(() => datasets.filter(isImage).filter((d) => !d.archived), [datasets]);
  const [ids, setIds] = useState<number[]>([]);
  const [labels, setLabels] = useState<Record<number, string>>({});
  const [referenceId, setReferenceId] = useState<number | null>(null);
  const [spread, setSpread] = useState<"std" | "mad">("std");
  const [channel, setChannel] = useState("1");
  const [filter, setFilter] = useState("");
  const [cands, setCands] = useState<FamilyCandidate[]>([]);
  const [busy, setBusy] = useState(false);
  const [rid, setRid] = useState<string | null>(null);
  const queued = useQueueReason(busy, rid);
  const [err, setErr] = useState<string | null>(null);
  const [reasons, setReasons] = useState<string[]>([]);
  const [excluded, setExcluded] = useState<{ label: string; reason: string }[]>([]);
  const [result, setResult] = useState<{ r: FamilyResult; channel: number; names: string[]; axial: boolean } | null>(null);

  useEffect(() => {
    listFamilyCandidates().then((x) => setCands(Array.isArray(x) ? x : [])).catch(() => setCands([]));
  }, [datasets.length]);
  useEffect(() => {
    if (!preset) return;
    setIds(preset.ids.slice(0, MAX));
    setLabels(preset.labels ?? {});
    setReferenceId(preset.referenceId ?? preset.ids[0] ?? null);
    setExcluded(preset.excluded ?? []);
    setResult(null);
    setErr(null);
  }, [preset?.nonce]); // eslint-disable-line react-hooks/exhaustive-deps

  const byId = useMemo(() => new Map(images.map((d) => [d.id, d])), [images]);
  const chosen = ids.map((id) => byId.get(id)).filter((d): d is NonNullable<typeof d> => !!d);
  const axial = chosen.length > 0 && chosen.every((d) => d.interpretation?.kind === "axial_orientation");
  const anyCube = chosen.some((d) => byIdNaxis(d) >= 3);
  const nameOf = (id: number) => labels[id] || byId.get(id)?.name || `file ${id}`;
  const ref = referenceId !== null && ids.includes(referenceId) ? referenceId : ids[0] ?? null;
  const toggle = (id: number) => setIds((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : cur.length < MAX ? [...cur, id] : cur));
  const chNum = Number(channel);
  const channelOk = !anyCube || (Number.isInteger(chNum) && chNum >= 1);
  const shown = images.filter((d) => !filter.trim() || d.name.toLowerCase().includes(filter.trim().toLowerCase()));

  function useCandidate(c: FamilyCandidate, withSource: boolean) {
    const members = c.members.slice().sort((a, b) => (Number(a.value) - Number(b.value)) || a.value.localeCompare(b.value));
    const next = members.map((m) => m.datasetId);
    const lab: Record<number, string> = {};
    for (const m of members) lab[m.datasetId] = optionLabel(c.varying, m.value);
    if (withSource && byId.has(c.sourceId)) { next.unshift(c.sourceId); lab[c.sourceId] = `${byId.get(c.sourceId)!.name} (the source, as it is)`; }
    setIds(next.slice(0, MAX));
    setLabels(lab);
    setReferenceId(next[0] ?? null);
    setExcluded([]);
    setResult(null);
    setErr(null);
  }

  async function go() {
    if (ids.length < 2 || ref === null) return;
    const id = newRequestId();
    setRid(id);
    setBusy(true);
    setErr(null);
    setReasons([]);
    setResult(null);
    try {
      const r = await runFamily({ datasetIds: ids, referenceId: ref, spread, labels: ids.map(nameOf), ...(anyCube ? { channel: chNum - 1 } : {}) }, undefined, id);
      setResult({ r, channel: anyCube ? chNum - 1 : 0, names: ids.map(nameOf), axial });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "The comparison did not work.");
      setReasons(((e as { reasons?: string[] }).reasons) ?? []);
    }
    setBusy(false);
  }

  return (
    <div className={s.root}>
      <h2 className={s.h}>{preset?.title ?? "Family of maps"}</h2>
      <p className={s.note}>Maps that differ in one parameter, side by side on one colour scale, with how well they agree. They must share a grid: nothing is resampled here. Choose scalar maps, or maps declared as axial orientations; the two are not mixed. The maps must be in one place: all on this computer, or all on one host, where they are compared without being copied.</p>
      {excluded.length > 0 ? (
        <section aria-label="Left out of this family" className={s.block}>
          <h3 className={s.h3}>Left out of this family, not compared</h3>
          <ul className={s.cands}>{excluded.map((x, i) => <li key={i}><strong>{x.label}</strong> <span className={s.note}>{x.reason}</span></li>)}</ul>
        </section>
      ) : null}
      {cands.length > 0 ? (
        <section aria-label="Families found in your results" className={s.block}>
          <h3 className={s.h3}>Found in your results</h3>
          <ul className={s.cands}>
            {cands.map((c, i) => {
              const src = byId.get(c.sourceId)?.name ?? `file ${c.sourceId}`;
              const fixed = Object.entries(c.fixed).map(([k, v]) => `${k} ${v}`).join(", ");
              return (
                <li key={i}>
                  <span>{c.op === "smooth_spectral" ? "Spectral smoothings" : "Convolutions"} of <strong>{src}</strong> that differ in {c.varying} ({c.members.map((m) => m.value).join(", ")}){fixed ? `; the same ${fixed}` : ""}</span>
                  <Button onClick={() => useCandidate(c, false)}>Use these</Button>
                  {byId.has(c.sourceId) ? <Button onClick={() => useCandidate(c, true)}>Use these and the source</Button> : null}
                </li>
              );
            })}
          </ul>
        </section>
      ) : null}
      <section aria-label="Maps to compare" className={s.block}>
        <h3 className={s.h3}>Maps ({ids.length} of at most {MAX})</h3>
        <Input label="Filter the list" value={filter} onChange={(e) => setFilter(e.target.value)} />
        <ul className={s.pick}>
          {shown.map((d) => (
            <li key={d.id}>
              <Checkbox label={`${d.name}${d.interpretation ? " (orientation)" : ""}`} checked={ids.includes(d.id)} disabled={!ids.includes(d.id) && ids.length >= MAX} onChange={() => toggle(d.id)} />
            </li>
          ))}
          {shown.length === 0 ? <li className={s.note}>No map matches.</li> : null}
        </ul>
        {ids.length > 0 ? (
          <div className={s.row}>
            <Select label="Compare each with" value={ref ?? ""} onChange={(e) => setReferenceId(Number(e.target.value))}>
              {ids.map((id) => <option key={id} value={id}>{nameOf(id)}</option>)}
            </Select>
            {!axial ? (
              <Select label="Spread across the maps" value={spread} onChange={(e) => setSpread(e.target.value as "std" | "mad")}>
                <option value="std">Standard deviation (n − 1)</option>
                <option value="mad">Median absolute deviation</option>
              </Select>
            ) : null}
            {anyCube ? <Input label="Channel (1-based, the same for every cube)" value={channel} inputMode="numeric" onChange={(e) => setChannel(e.target.value)} aria-invalid={!channelOk} /> : null}
            <Button variant="primary" disabled={busy || ids.length < 2 || !channelOk} onClick={go}>{busy ? "Working…" : "Compare"}</Button>
          </div>
        ) : null}
        <QueueNote reason={queued} />
        {axial ? <p className={s.note}>Orientation maps: 0° and 180° are the same orientation, so no plain difference is taken. Each map is compared by its axial difference (0° to 90°) and the alignment measure AM = ⟨cos 2Δ⟩.</p> : null}
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
        {reasons.length > 0 ? <ul className={s.note}>{reasons.map((r) => <li key={r}>{r}</li>)}</ul> : null}
      </section>
      {result ? <FamilyResultView result={result.r} channel={result.channel} names={result.names} axial={result.axial} datasets={byId} /> : null}
      <FamilyMapsCleanup refreshKey={result} onChanged={(gone) => { setResult((r) => (r && r.r.maps.some((m) => gone.includes(m.dataset.id)) ? null : r)); /* its maps no longer exist */ onCatalogChanged?.(); }} />
    </div>
  );
}

type Loaded = { members: (Plane | null)[]; diffs: Map<number, Plane>; spread: Plane | null };

function Legend({ id, range, unit, label }: { id: RampId; range: [number, number] | null; unit: string; label: string }) {
  return (
    <div className={s.legend} role="img" aria-label={range ? `${label}: ${num(range[0])} to ${num(range[1])} ${unit}` : `${label}: no values`}>
      <span className={s.ramp} style={{ background: rampCss(id) }} />
      <span className="mono">{range ? `${num(range[0])}` : EM}</span>
      <span className={s.legendLabel}>{label}{unit ? ` (${unit})` : ""}</span>
      <span className="mono">{range ? `${num(range[1])}` : EM}</span>
    </div>
  );
}

const byIdNaxis = (d: Dataset | undefined) => (d && isImage(d) ? d.header.naxis : 2);

/** The panels, the agreement table and the statement of what was computed. */
export function FamilyResultView({ result, channel, names, axial, datasets }: { result: FamilyResult; channel: number; names: string[]; axial: boolean; datasets: Map<number, Dataset> }) {
  const sum = result.summary;
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [cursor, setCursor] = useState<Cursor | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    setLoaded(null);
    setProblem(null);
    (async () => {
      try {
        const members = await Promise.all(result.datasetIds.map((id) => getPlane(id, (byIdNaxis(datasets.get(id))) >= 3 ? channel : 0, ac.signal).catch(() => null)));
        const diffs = new Map<number, Plane>();
        let spread: Plane | null = null;
        await Promise.all(result.maps.map(async (m) => {
          const p = await getPlane(m.dataset.id, 0, ac.signal);
          if (m.role === "spread") spread = p; else diffs.set(m.member, p);
        }));
        if (!ac.signal.aborted) setLoaded({ members, diffs, spread });
      } catch (e) {
        if (!ac.signal.aborted) setProblem(e instanceof Error ? e.message : "Could not load the maps.");
      }
    })();
    return () => ac.abort();
  }, [result, channel, datasets]);

  const toDegrees = (id: number) => (datasets.get(id)?.interpretation?.unit === "rad" ? 180 / Math.PI : 1);
  const refAt = sum.reference;
  const memberRange = useMemo(() => (axial || !loaded ? null : sharedRange(loaded.members.filter((p): p is Plane => !!p).map((p) => p.head.stats))), [loaded, axial]);
  const diffRange = useMemo(() => (!loaded ? null : axial ? AXIAL_DIFFERENCE_RANGE : symmetricRange([...loaded.diffs.values()].map((p) => p.head.stats))), [loaded, axial]);
  const spreadR = useMemo(() => (!loaded || !loaded.spread ? null : spreadRange([loaded.spread.head.stats])), [loaded]);
  const luts = useMemo(() => ({ viridis: lut("viridis"), twilight: lut("twilight"), diverging: lut("diverging") }), []);
  const unit = sum.unit;

  const at = (p: Plane | null | undefined) => (p && cursor && cursor.x < p.head.width && cursor.y < p.head.height ? p.data[cursor.y * p.head.width + cursor.x] : undefined);
  const memberText = (i: number) => {
    const v = at(loaded?.members[i]);
    return axial ? (v === undefined || !Number.isFinite(v) ? "no value" : `${num(axialDegrees(v, toDegrees(result.datasetIds[i]!)))} °`) : valueText(v, unit);
  };
  const matrix = (axial ? sum.pairwise_am : sum.pairwise_rms) ?? [];

  return (
    <div className={s.result}>
      {sum.warnings.map((w) => <p key={w} className={s.warn} role="status">{w}</p>)}
      {problem ? <p role="alert" className={s.err}>{problem}</p> : null}
      {!loaded && !problem ? <p className={s.note} role="status">Loading the maps…</p> : null}
      {loaded ? (
        <>
          <section aria-label="The maps" className={s.block}>
            <h3 className={s.h3}>The maps, on one scale</h3>
            {axial ? <Legend id="twilight" range={[0, 180]} unit="°" label="orientation, cyclic: 0° and 180° are the same" /> : <Legend id="viridis" range={memberRange} unit={unit} label="all maps" />}
            <div className={s.grid}>
              {loaded.members.map((p, i) => p ? (
                <FamilyMap key={result.datasetIds[i]} title={`${names[i]}${i === refAt ? " (reference)" : ""}`} plane={p} lut={axial ? luts.twilight : luts.viridis} range={axial ? null : memberRange}
                  axial={axial ? { toDegrees: toDegrees(result.datasetIds[i]!) } : undefined} cursor={cursor} onCursor={setCursor} />
              ) : <p key={i} className={s.err}>{names[i]} could not be loaded.</p>)}
            </div>
          </section>
          <section aria-label="Agreement" className={s.block}>
            <h3 className={s.h3}>Agreement with {names[refAt]}</h3>
            {axial ? <Legend id="viridis" range={AXIAL_DIFFERENCE_RANGE} unit="°" label="axial difference" /> : <Legend id="diverging" range={diffRange} unit={unit} label="member minus reference" />}
            <div className={s.grid}>
              {sum.comparison_maps.map((i) => {
                const p = loaded.diffs.get(i);
                return p ? <FamilyMap key={i} title={`${names[i]}, ${axial ? "axial difference" : "minus reference"}`} plane={p} lut={axial ? luts.viridis : luts.diverging} range={diffRange} cursor={cursor} onCursor={setCursor} /> : null;
              })}
            </div>
            {loaded.spread ? (
              <>
                <Legend id="viridis" range={spreadR} unit={unit} label={axial ? "circular spread across the maps" : `spread across the maps (${sum.spread === "mad" ? "median absolute deviation" : "standard deviation"})`} />
                <div className={s.grid}>
                  <FamilyMap title="Spread across the maps" plane={loaded.spread} lut={luts.viridis} range={spreadR} cursor={cursor} onCursor={setCursor} />
                </div>
              </>
            ) : null}
          </section>
          <section aria-label="Values at the cursor" className={s.block}>
            <h3 className={s.h3}>{cursor ? `Values at pixel ${cursor.x + 1}, ${cursor.y + 1}` : "Values at the cursor"}</h3>
            {!cursor ? <p className={s.note}>Point at a map, or focus one and use the arrow keys: the same pixel is read in every panel.</p> : (
              <table className={s.table}>
                <thead><tr><th scope="col">Map</th><th scope="col">Value</th><th scope="col">{axial ? "Axial difference from the reference" : "Minus the reference"}</th></tr></thead>
                <tbody>
                  {names.map((n, i) => (
                    <tr key={i}>
                      <th scope="row">{n}{i === refAt ? " (reference)" : ""}</th>
                      <td className="mono">{memberText(i)}</td>
                      <td className="mono">{i === refAt ? EM : valueText(at(loaded.diffs.get(i)), unit)}</td>
                    </tr>
                  ))}
                  <tr><th scope="row">Spread across the maps</th><td className="mono" colSpan={2}>{valueText(at(loaded.spread), unit)}</td></tr>
                </tbody>
              </table>
            )}
          </section>
        </>
      ) : null}
      <section aria-label="Agreement numbers" className={s.block}>
        <h3 className={s.h3}>How well they agree, over the pixels both maps have</h3>
        <table className={s.table}>
          <thead>
            <tr>
              <th scope="col">Map</th><th scope="col">Pixels compared</th>
              {axial ? (<><th scope="col">Mean axial difference (°)</th><th scope="col">rms (°)</th><th scope="col">Within 10°</th><th scope="col">AM</th></>) : (<><th scope="col">Mean</th><th scope="col">Median</th><th scope="col">rms</th><th scope="col">Range</th></>)}
            </tr>
          </thead>
          <tbody>
            {sum.members.map((m) => (
              <tr key={m.index}>
                <th scope="row">{names[m.index]}{m.reference ? " (reference)" : ""}</th>
                {m.reference ? <td colSpan={axial ? 5 : 5}>{EM}</td> : axial ? (
                  <><td className="mono">{(m.pixels_compared ?? 0).toLocaleString("en-US")}</td><td className="mono">{num(m.mean_abs_deg)}</td><td className="mono">{num(m.rms_deg)}</td><td className="mono">{m.within_10_deg === null || m.within_10_deg === undefined ? EM : `${(m.within_10_deg * 100).toFixed(1)}%`}</td><td className="mono">{num(m.am)}</td></>
                ) : (
                  <><td className="mono">{(m.pixels_compared ?? 0).toLocaleString("en-US")}</td><td className="mono">{num(m.mean)}</td><td className="mono">{num(m.median)}</td><td className="mono">{num(m.rms)}</td><td className="mono">{m.min === null || m.min === undefined ? EM : `${num(m.min)} to ${num(m.max)}`} {unit}</td></>
                )}
              </tr>
            ))}
          </tbody>
        </table>
        {axial ? <p className={s.note}>AM = ⟨cos 2Δ⟩: 1 for parallel, 0 for unrelated, −1 for perpendicular. {sum.orientations_cancel_pixels ? `${sum.orientations_cancel_pixels.toLocaleString("en-US")} pixels have orientations that cancel, so their spread is undefined and left blank.` : ""}</p> : null}
        {matrix.length > 0 ? (
          <details>
            <summary>{axial ? "AM between every pair of maps" : "rms difference between every pair of maps"}</summary>
            <table className={s.table}>
              <thead><tr><th scope="col" />{names.map((n, i) => <th key={i} scope="col">{n}</th>)}</tr></thead>
              <tbody>{matrix.map((row, i) => <tr key={i}><th scope="row">{names[i]}</th>{row.map((v, j) => <td key={j} className="mono">{num(v)}</td>)}</tr>)}</tbody>
            </table>
          </details>
        ) : null}
        <p className={s.note}>{sum.policy}</p>
        <p className={s.note}>The comparison and spread maps are saved in your files as derived datasets ({result.maps.map((m) => m.dataset.name).join(", ")}), each with this record.</p>
      </section>
    </div>
  );
}

