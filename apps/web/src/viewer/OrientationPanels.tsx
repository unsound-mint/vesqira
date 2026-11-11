import { useState } from "react";
import { Button, Select } from "../ui";
import type { Dataset } from "../api/catalog";
import type { AngleFrame, AngleQuantity, InterpretationRequest } from "../api/catalog";
import type { AxialSummary } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import s from "./OrientationPanels.module.css";

/** What each choice means, in the words shown under the selector. Declared by the person; the file says none of it. */
export const FRAME_TEXT: Record<AngleFrame, string> = {
  pixel: "Pixel grid: measured counter-clockwise from the file’s first axis (+x) towards its second (+y), as stored. Vesqira relates it to the sky with the file’s WCS at each pixel.",
  sky: "Sky: position angle east of north (IAU) in the file’s own celestial frame.",
};
export const QUANTITY_TEXT: Record<AngleQuantity, string> = {
  b_field: "B-field orientation: the angle of the field itself.",
  gradient: "Gradient: perpendicular to B, as raw velocity gradients.",
  dust_polarization: "Dust polarization: the E-vector of emission, perpendicular to B.",
  starlight_polarization: "Starlight polarization: parallel to B.",
  unspecified: "Unspecified: you do not know. The map is then refused for comparison with other datasets.",
};
const QUANTITY_LABEL: Record<AngleQuantity, string> = {
  b_field: "B-field orientation",
  gradient: "Gradient",
  dust_polarization: "Dust polarization",
  starlight_polarization: "Starlight polarization",
  unspecified: "Unspecified (I do not know)",
};

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
  const [frame, setFrame] = useState<"" | AngleFrame>(cur?.frame ?? "");
  const [quantity, setQuantity] = useState<"" | AngleQuantity>(cur?.quantity ?? "");
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (!v.view) return <p className={s.note}>{EM}</p>;
  if (v.view.channels !== 1) return <p className={s.note}>Orientation maps are single planes; this file has {v.view.channels} channels.</p>;

  const others = datasets.filter((d) => d.id !== dataset.id);
  const suggestion = suggestCompanion(dataset.name, others);
  const st = v.plane?.head.stats;

  async function apply() {
    setBusy(true);
    const body: InterpretationRequest = kind === "scalar" ? { kind: "none" } : { kind: "axial_orientation", unit: unit as "deg" | "rad", reliabilityId: rel ? Number(rel) : null, frame: frame || null, quantity: quantity || null };
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
          <Select label="Angle frame" value={frame} onChange={(e) => setFrame(e.target.value as "" | AngleFrame)}>
            <option value="">Not declared</option>
            <option value="pixel">Pixel grid (counter-clockwise from +x)</option>
            <option value="sky">Sky (east of north)</option>
          </Select>
          <p className={s.note}>{frame ? FRAME_TEXT[frame] : "The file does not declare which direction the angle is measured from. Left undeclared, the map cannot be aligned onto another grid."}</p>
          <Select label="What the angle is" value={quantity} onChange={(e) => setQuantity(e.target.value as "" | AngleQuantity)}>
            <option value="">Not declared</option>
            {(Object.keys(QUANTITY_LABEL) as AngleQuantity[]).map((q) => <option key={q} value={q}>{QUANTITY_LABEL[q]}</option>)}
          </Select>
          <p className={s.note}>{quantity ? QUANTITY_TEXT[quantity] : "The file does not declare what the angle describes. Left undeclared, the map is refused for comparison with other datasets; its own statistics still work."}</p>
        </>
      ) : null}
      <Button type="button" variant="primary" disabled={busy || (kind === "axial" && unit === "")} onClick={apply}>{kind === "scalar" ? "Use as scalar" : "Declare axial"}</Button>
      {msg ? <p role="alert" className={s.err}>{msg}</p> : null}
      {cur?.stale ? <p role="alert" className={s.warn}>The file changed since you declared it. Declare it again to confirm the unit still applies.</p> : null}
      <p className={s.note}>
        {cur ? (cur.declaredBy === "user" ? `Declared by you as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}.` : `Proposed by ${cur.declaredBy === "vgt-run-adapter" ? `the run import (${cur.declaredBy})` : `the analysis that made this map (${cur.declaredBy})`} as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}: not from the file. Choose below and apply to override.`) : "Not declared: shown as an ordinary scalar map."} Kept with the catalog; the file is never changed. The file does not declare the unit, the frame or what the angle is.
      </p>
      {cur ? <p className={s.note}>{declarationNote(cur)}</p> : null}
    </div>
  );
}

/** Who says what the angle is, in words: the person, the VGT run import from its pipeline's convention, or an analysis keeping its input's. */
export function declarationNote(cur: NonNullable<Dataset["interpretation"]>): string {
  const f = cur.frame ? FRAME_TEXT[cur.frame] : "Frame: not declared.";
  const q = cur.quantity ? QUANTITY_TEXT[cur.quantity] : "Quantity: not declared.";
  if (cur.declaredBy === "user") return `${f} ${q}`;
  if (cur.declaredBy === "vgt-run-adapter") {
    return `${f} ${q} Proposed by the VGT run import from the pipeline’s convention (ψ = ½ atan2(U,Q) + 90°); not from the file. Override it if your run differs.`;
  }
  return `${f} ${q} Proposed by ${cur.declaredBy} from the map it was made from.`;
}

/** Degrees in [0, 180): a mean that rounds to 180.00 is the same orientation as 0.00, and is shown as 0. */
export function axialMeanText(d: number): string {
  const t = d.toFixed(2);
  return `${t === "180.00" ? "0.00" : t}°`;
}

function Row({ label, a, pixelsLabel = "Pixels" }: { label: string; a: AxialSummary | null; pixelsLabel?: string }) {
  if (!a) return null;
  return (
    <>
      <dt>{label}</dt>
      <dd className="mono">{a.defined && a.mean_deg !== null ? axialMeanText(a.mean_deg) : "undefined"}</dd>
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
