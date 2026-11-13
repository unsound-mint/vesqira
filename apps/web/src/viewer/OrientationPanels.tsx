import { useState } from "react";
import { Basis, BasisNote, Button, Select } from "../ui";
import { TierBadge } from "../ui/TierBadge";
import { isImage } from "../api/catalog";
import { ruleOf } from "../api/declared";
import type { Dataset, ImageDataset } from "../api/catalog";
import type { AngleFrame, AngleQuantity, InterpretationRequest } from "../api/catalog";
import type { AxialSummary } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import s from "./OrientationPanels.module.css";

/** Whether a declaration was read from the file's own header: by an orientation profile ("profile:VGT.jl@1"), or by the VGT.jl reading that came before profiles. */
export const isFileHeader = (by: string) => by === "vgt-file-header" || by.startsWith("profile:");
const profileOf = (by: string) => (by.startsWith("profile:") ? by.slice("profile:".length) : "VGT.jl");
const SKY_NAME: Record<string, string> = { galactic: "Galactic", icrs: "ICRS" };

/** The frame of a declaration in words. A header may state the angle in a frame other than its grid's: then every comparison carries it through the sky. */
export function skyText(cur: NonNullable<Dataset["interpretation"]>): string {
  if (cur.frame === "sky" && cur.skyFrame) {
    return `Sky: position angle east of north in the ${SKY_NAME[cur.skyFrame] ?? cur.skyFrame} frame, as the file’s header states it; carried through the sky into this grid’s own frame wherever it is compared or drawn over another map. Its own orientation lines are not drawn on itself, because they would use the stored angle as a pixel direction; show this map as an overlay on another map’s view instead.`;
  }
  return cur.frame ? FRAME_TEXT[cur.frame] : "Frame: unresolved (not declared yet).";
}

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

/** Rows and columns of a single-plane map, or null when it is a cube or not an image. */
const planeSize = (d: Dataset): string | null => {
  if (!isImage(d)) return null;
  const a = (d as ImageDataset).header?.axes ?? [];
  if (a.length > 2 && (a[2]?.length ?? 1) > 1) return null;
  return a.length >= 2 ? `${a[0]!.length}×${a[1]!.length}` : null;
};

/** The folder (and host, when not this computer) a dataset lives in: what tells two files of the same name apart. */
export function whereText(d: Dataset): string {
  const parts = d.path.split("/");
  const folder = parts.length > 1 ? `…/${parts.slice(-2, -1)[0]}` : "";
  return `${d.hostId !== 0 ? `host ${d.hostId}, ` : ""}${folder}`.replace(/, $/, "");
}

/**
 * The maps that can be this map's reliability: single planes of the same size, never itself, an archived dataset or a catalog
 * (the server checks the sky grid itself; the size is what the catalog knows). The choice already declared stays listed even
 * when it no longer fits, so opening the form never silently drops it. A name two datasets share gets its folder (and host).
 */
export function reliabilityChoices(self: Dataset, all: Dataset[], currentId: number | null): { id: number; label: string }[] {
  const size = planeSize(self);
  const fits = all.filter((d) => d.id !== self.id && !d.archived && isImage(d) && (d.id === currentId || (size !== null && planeSize(d) === size && d.hostId === self.hostId)));
  const count = new Map<string, number>();
  for (const d of fits) count.set(d.name, (count.get(d.name) ?? 0) + 1);
  return fits.map((d) => ({ id: d.id, label: (count.get(d.name) ?? 0) > 1 ? `${d.name} (${whereText(d)})` : d.name }));
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
  const choices = reliabilityChoices(dataset, datasets, cur?.reliabilityId ?? null);
  const suggestion = suggestCompanion(dataset.name, others.filter((d) => choices.some((c) => c.id === d.id)));
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
            <BasisNote text={`This plane’s values run from ${Number(st.min.toPrecision(6))} to ${Number(st.max.toPrecision(6))}; that is only a hint, Vesqira never infers the unit.`} />
          ) : null}
          <Select label="Reliability map (optional)" value={rel} onChange={(e) => setRel(e.target.value)}>
            <option value="">None</option>
            {choices.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
          </Select>
          {suggestion && rel !== String(suggestion.id) ? (
            <p className={s.note}>
              By file name, <span className="mono">{suggestion.name}</span> may be its reliability map.{" "}
              <Button type="button" onClick={() => setRel(String(suggestion.id))}>Use it</Button>
            </p>
          ) : null}
          <BasisNote text="Only single-plane maps of this map’s size are listed; the server checks the sky grid, and the range is not assumed to be 0 to 1." />
          <Select label="Angle frame" value={frame} onChange={(e) => setFrame(e.target.value as "" | AngleFrame)}>
            <option value="">Not declared</option>
            <option value="pixel">Pixel grid (counter-clockwise from +x)</option>
            <option value="sky">Sky (east of north)</option>
          </Select>
          <BasisNote text={frame ? FRAME_TEXT[frame] : "The frame is unresolved: the file does not say which direction the angle is measured from, and Vesqira does not assume one. The map still displays; it cannot be aligned onto another grid until you declare it."} />
          <Select label="What the angle is" value={quantity} onChange={(e) => setQuantity(e.target.value as "" | AngleQuantity)}>
            <option value="">Not declared</option>
            {(Object.keys(QUANTITY_LABEL) as AngleQuantity[]).map((q) => <option key={q} value={q}>{QUANTITY_LABEL[q]}</option>)}
          </Select>
          <BasisNote text={quantity ? QUANTITY_TEXT[quantity] : "What the angle describes is unresolved: the file does not say, and Vesqira does not assume it. The map still displays and its own statistics work; it is refused for comparison with other datasets until you declare it."} />
        </>
      ) : null}
      <Button type="button" variant="primary" disabled={busy || (kind === "axial" && unit === "")} onClick={apply}>{kind === "scalar" ? "Use as scalar" : "Declare axial"}</Button>
      {msg ? <p role="alert" className={s.err}>{msg}</p> : null}
      {cur?.stale ? <p role="alert" className={s.warn}>The file changed since you declared it. Declare it again to confirm the unit still applies.</p> : null}
      <FileDeclaration dataset={dataset} busy={busy} onAdopt={async () => { setBusy(true); setMsg(await declare(dataset.id, { kind: "axial_orientation", fromFile: true })); setBusy(false); }} />
      <Basis summary={cur ? (isFileHeader(cur.declaredBy) ? `Declared by the file’s own header as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}.` : cur.declaredBy === "user" ? `Declared by you as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}.` : ruleOf(cur.declaredBy) !== null ? `Declared by your rule ${ruleOf(cur.declaredBy)} as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}; not from the file.` : `Proposed by ${cur.declaredBy === "vgt-run-adapter" ? "the run import" : "the analysis that made this map"} as axial orientation in ${cur.unit === "deg" ? "degrees" : "radians"}; not from the file.`) : "Not declared: shown as an ordinary scalar map."}>
        <p>
          {cur && isFileHeader(cur.declaredBy) ? `Read from the file’s own header by the orientation profile ${profileOf(cur.declaredBy)}; choose above and apply to replace it with your own declaration. ` : cur && ruleOf(cur.declaredBy) !== null ? `Declared by your import rule ${ruleOf(cur.declaredBy)}, not by you and not by the file. Choose above and apply to make it your own. ` : cur && cur.declaredBy !== "user" && !isFileHeader(cur.declaredBy) ? `Proposed by ${cur.declaredBy === "vgt-run-adapter" ? `the run import (${cur.declaredBy})` : `the analysis that made this map (${cur.declaredBy})`}. Choose above and apply to override. ` : ""}Kept with the catalog; the file is never changed.{cur && isFileHeader(cur.declaredBy) ? "" : " The file does not declare the unit, the frame or what the angle is."}
        </p>
        {cur ? <p>{declarationNote(cur)}</p> : null}
        {cur ? <p>How far this is established: <TierBadge tier={cur.tier} mappingVersion={cur.mappingVersion} rule={cur.declaredBy === "user" ? null : cur.declaredBy} /></p> : null}
      </Basis>
    </div>
  );
}

/**
 * What the file's own header says about its angle (VGT.jl writes PSIFRAME, PSIANGLE, VGTPROD and the run parameters), the places where the
 * declaration in force disagrees with it, and the run parameters for a file imported without a run record. Nothing here is applied
 * silently: a conflict is shown with both sides, and adopting the file's declaration is a button.
 */
export function FileDeclaration({ dataset, busy, onAdopt }: { dataset: Dataset; busy: boolean; onAdopt: () => void }) {
  const h = dataset.orientationHeader ?? dataset.vgtHeader;
  if (!h) return null;
  const cur = dataset.interpretation;
  const o = h.orientation;
  const inForce = cur !== null && cur !== undefined && isFileHeader(cur.declaredBy);
  return (
    <div className={s.wrap}>
      {h.conflicts.length > 0 ? (
        <div role="alert" className={s.warn}>
          <p>The file’s header disagrees with the declaration in force. The declaration in force stays in use; nothing was changed.</p>
          <ul>{h.conflicts.map((c) => <li key={c}>{c[0]!.toUpperCase() + c.slice(1)}.</li>)}</ul>
        </div>
      ) : null}
      {o && !inForce ? (
        <p className={s.note}>
          The file declares its angle as {o.frame === "pixel" ? "measured on the pixel grid" : `a position angle east of north in the ${SKY_NAME[o.skyFrame ?? ""] ?? ""} frame`}
          {o.quantity ? `, ${QUANTITY_LABEL[o.quantity as AngleQuantity] ?? o.quantity}` : ", without saying what the angle is"}, in {o.unit === "deg" ? "degrees" : "radians"} (read by the profile {o.profile}; declared by the file, not verified).{" "}
          <Button type="button" disabled={busy} onClick={onAdopt}>Use the file’s declaration</Button>
        </p>
      ) : null}
      {!o && h.unresolved.length > 0 ? (
        <p className={s.note}>
          {h.profile ? `The orientation profile ${h.profile} cannot place the ${h.unresolved.join(", ")} of this file from its header` : "This header was stored before orientation profiles and has not been read"}:
          {" "}{h.unresolved.length > 1 ? "they are" : "it is"} unresolved. Nothing is inferred from the WCS or the values, and the map still displays. Declare {h.unresolved.length > 1 ? "them" : "it"} above before an analysis that needs {h.unresolved.length > 1 ? "them" : "it"}.
        </p>
      ) : null}
      {h.notes.map((n) => <p key={n} className={s.note}>{n}.</p>)}
      {h.params.length > 0 ? (
        <Basis summary={`VGT keywords in the file${h.product ? ` (${h.product})` : ""}: ${h.params.length} declared`}>
          <p>Declared by the file’s header; nothing here is verified.</p>
          <dl>{h.params.map((p) => <div key={p.key}><dt className="mono">{p.key}</dt><dd>{p.value}</dd></div>)}</dl>
        </Basis>
      ) : null}
    </div>
  );
}

/** Who says what the angle is, in words: the person, the file's header read by a profile, or an analysis keeping its input's. */
export function declarationNote(cur: NonNullable<Dataset["interpretation"]>): string {
  if (isFileHeader(cur.declaredBy)) {
    return `${skyText(cur)} ${cur.quantity ? QUANTITY_TEXT[cur.quantity] : "Quantity: unresolved (the header does not say what the angle is)."} Declared by the file’s own header; not verified.`;
  }
  const f = cur.frame ? FRAME_TEXT[cur.frame] : "Frame: unresolved (not declared yet).";
  const q = cur.quantity ? QUANTITY_TEXT[cur.quantity] : "Quantity: unresolved (not declared yet).";
  if (cur.declaredBy === "user") return `${f} ${q}`;
  if (ruleOf(cur.declaredBy) !== null) return `${f} ${q} Declared by your import rule ${ruleOf(cur.declaredBy)}; not from the file.`;
  if (cur.declaredBy === "vgt-run-adapter") {
    return `${f} ${q} Proposed by an earlier version of the run import, which assumed the pipeline’s convention (ψ = ½ atan2(U,Q) + 90°) when the header said nothing: an assumption, not a statement by the file or the tool. Declare it yourself to confirm or replace it.`;
  }
  return `${f} ${q} Proposed by ${cur.declaredBy} from the map it was made from.`;
}

/** Degrees in [0, 180): a mean that rounds to 180.00 is the same orientation as 0.00, and is shown as 0. */
export function axialMeanText(d: number): string {
  const t = d.toFixed(2);
  return `${t === "180.00" ? "0.00" : t}°`;
}

/** The mean of a doubled-angle summary in words. An undefined mean says why in plain terms: no pixels, or orientations that cancel. */
export function meanText(a: AxialSummary): string {
  if (a.defined && a.mean_deg !== null) return axialMeanText(a.mean_deg);
  if (a.n === 0) return "no mean orientation (no pixel has a value)";
  if (a.resultant !== null && a.resultant < 1e-6) return "no mean orientation (the orientations cancel)";
  return "no mean orientation";
}

function Row({ label, a, pixelsLabel = "Pixels" }: { label: string; a: AxialSummary | null; pixelsLabel?: string }) {
  if (!a) return null;
  return (
    <>
      <dt>{label}</dt>
      <dd className="mono">{meanText(a)}</dd>
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
      {v.interp.reliabilityId === null ? <p className={s.note}>Reliability: no map declared, so every pixel counts equally.</p> : null}
      <Basis summary="Doubled-angle method: 0° and 180° are the same orientation, so no arithmetic mean of angles is used.">
        <p>Doubled-angle method (astropy circular statistics). R near 1 means well aligned; below 1e-6 the orientations cancel and no mean exists. Zero reliability is a value and keeps its pixel with no weight; unknown reliability excludes it.</p>
      </Basis>
    </div>
  );
}
