import { useState } from "react";
import { Button, Select } from "../ui";
import type { Dataset } from "../api/catalog";
import type { InterpretationRequest } from "../api/catalog";
import type { AxialSummary } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import s from "./OrientationPanels.module.css";

/** A reliability candidate suggested by file name only: the user confirms it, and the server checks the grid. */
export function suggestCompanion(name: string, others: Dataset[]): Dataset | null {
  if (/reliability/i.test(name)) return null;
  return others.find((d) => /reliability/i.test(d.name) && d.name.replace(/reliability[_-]?/i, "") === name) ?? null;
}

/** What the user says this single-plane map is. Declared by the user, stored in the catalog, never written to the file. */
export function InterpretationPanel({ v, dataset, datasets, declare }: { v: Viewer; dataset: Dataset; datasets: Dataset[]; declare: (id: number, b: InterpretationRequest) => Promise<string | null> }) {
  const cur = dataset.interpretation;
  const [kind, setKind] = useState<"scalar" | "axial">(cur ? "axial" : "scalar");
  const [unit, setUnit] = useState<"" | "deg" | "rad">(cur?.unit ?? "");
  const [rel, setRel] = useState<string>(cur?.reliabilityId ? String(cur.reliabilityId) : "");
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (!v.view) return <p className={s.note}>{EM}</p>;
  if (v.view.channels !== 1) return <p className={s.note}>Orientation maps are single planes; this file has {v.view.channels} channels.</p>;

  const others = datasets.filter((d) => d.id !== dataset.id);
  const suggestion = suggestCompanion(dataset.name, others);
  const st = v.plane?.head.stats;

  async function apply() {
    setBusy(true);
    const body: InterpretationRequest = kind === "scalar" ? { kind: "none" } : { kind: "axial_orientation", unit: unit as "deg" | "rad", reliabilityId: rel ? Number(rel) : null };
    setMsg(await declare(dataset.id, body));
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      <Select label="Treat this map as" value={kind} onChange={(e) => setKind(e.target.value as "scalar" | "axial")}>
        <option value="scalar">Scalar values</option>
        <option value="axial">Axial orientation (angles)</option>
      </Select>
      {kind === "axial" ? (
        <>
          <Select label="Angle unit (required)" value={unit} onChange={(e) => setUnit(e.target.value as "" | "deg" | "rad")}>
            <option value="">Choose…</option>
            <option value="deg">Degrees</option>
            <option value="rad">Radians</option>
          </Select>
          {st && st.min !== null && st.max !== null ? (
            <p className={s.note}>This plane’s values run from {Number(st.min.toPrecision(6))} to {Number(st.max.toPrecision(6))}. That is only a hint: Vesqira never infers the unit.</p>
          ) : null}
          <Select label="Reliability map (optional)" value={rel} onChange={(e) => setRel(e.target.value)}>
            <option value="">None</option>
            {others.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </Select>
          {suggestion && rel !== String(suggestion.id) ? (
            <p className={s.note}>
              By file name, <span className="mono">{suggestion.name}</span> may be its reliability map.{" "}
              <Button type="button" onClick={() => setRel(String(suggestion.id))}>Use it</Button>
            </p>
          ) : null}
          <p className={s.note}>The reliability map must share this map’s grid; its range is not assumed to be 0 to 1.</p>
        </>
      ) : null}
      <Button type="button" variant="primary" disabled={busy || (kind === "axial" && unit === "")} onClick={apply}>{kind === "scalar" ? "Use as scalar" : "Declare axial"}</Button>
      {msg ? <p role="alert" className={s.err}>{msg}</p> : null}
      {cur?.stale ? <p role="alert" className={s.warn}>The file changed since you declared it. Declare it again to confirm the unit still applies.</p> : null}
      <p className={s.note}>
        {cur ? `Declared by you as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}.` : "Not declared: shown as an ordinary scalar map."} Kept with the catalog; the file is never changed. The file does not declare an angle convention. Vesqira assumes the one the VGT pipeline uses (from the +x pixel axis towards +y, counter-clockwise on screen); check it against your pipeline.
      </p>
    </div>
  );
}

function Row({ label, a, pixelsLabel = "Pixels" }: { label: string; a: AxialSummary | null; pixelsLabel?: string }) {
  if (!a) return null;
  return (
    <>
      <dt>{label}</dt>
      <dd className="mono">{a.defined && a.mean_deg !== null ? `${a.mean_deg.toFixed(2)}°` : "undefined"}</dd>
      <dt>Resultant R</dt>
      <dd className="mono">{a.resultant === null ? EM : a.resultant.toPrecision(4)}</dd>
      <dt>{pixelsLabel}</dt>
      <dd className="mono">{a.n.toLocaleString("en-US")}</dd>
      {!a.defined && a.reason ? (
        <>
          <dt>Why</dt>
          <dd>{a.reason}</dd>
        </>
      ) : null}
    </>
  );
}

/** Doubled-angle statistics for the whole map or the region. Never an arithmetic mean of angles. */
export function OrientationStats({ v }: { v: Viewer }) {
  const a = v.axial;
  if (!v.interp) return <p className={s.note}>Declare the map as axial orientation to get orientation statistics.</p>;
  if (v.axialError) return <p role="alert" className={s.err}>{v.axialError}</p>;
  if (!a) return <p className={s.note}>Computing…</p>;
  return (
    <div className={s.wrap}>
      <p className={s.note}>{v.region ? `Region: ${a.pixels.toLocaleString("en-US")} pixels` : `Whole map: ${a.pixels.toLocaleString("en-US")} pixels`}. {a.angles_excluded.toLocaleString("en-US")} without a value are excluded.</p>
      <dl className={s.facts}>
        <Row label="Mean orientation" a={a.unweighted} />
      </dl>
      {a.weighted ? (
        <dl className={s.facts}>
          <Row label="Weighted by reliability" a={a.weighted} pixelsLabel="Pixels (incl. zero weight)" />
          <dt>Unknown weight</dt>
          <dd className="mono">{a.weighted.unknown_weight_excluded.toLocaleString("en-US")} excluded</dd>
        </dl>
      ) : null}
      {a.values_fit_radians ? <p role="status" className={s.warn}>Every value lies within 0 to π (3.14159). If these are radians, the declared unit (degrees) is wrong and the statistics above are not valid.</p> : null}
      {a.outside_zero_to_180 > 0 ? <p role="status" className={s.warn}>{a.outside_zero_to_180.toLocaleString("en-US")} values lie outside 0 to 180° in the declared unit; they are wrapped modulo 180° (a −90° to 90° convention is equivalent). If that is not expected, check the unit.</p> : null}
      <p className={s.note}>
        Doubled-angle method (astropy circular statistics): 0° and 180° are the same orientation. R near 1 means well aligned; below 1e-6 the orientations cancel and no mean exists. Zero reliability is a value and keeps its pixel with no weight; unknown reliability excludes it.
      </p>
    </div>
  );
}
