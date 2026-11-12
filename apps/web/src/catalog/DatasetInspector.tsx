import type { ImageDataset, Problem } from "../api/catalog";
import { ruleOf } from "../api/declared";
import { Button } from "../ui";
import { tierText } from "../api/tier";
import { bytes, show } from "./format";
import { beamText, pixelScale } from "./resolution";
import { HeaderCards } from "./HeaderCards";
import { correctedUnit, fileStatesItsUnit } from "./unit";
import s from "./DatasetInspector.module.css";

/** What the file header declares, in native FITS axis order. Nothing here is verified. */
const PROBLEM_TEXT: Record<Problem, string> = {
  modified: "This file has changed on disk since it was registered, so the header and figures made earlier may no longer describe it. Viewing is paused until it is read again.",
  missing: "This file is not at its registered path any more. Figures and boards made from it keep their pictures; nothing here can read it until it is back.",
  not_allowed: "This file is outside the source folders this program was started with, so it cannot be read.",
};

/** The unit as the file gave it, or as you declared it (said so), or unresolved: the three are never mixed up. */
function unitText(d: ImageDataset): string {
  const corrected = correctedUnit(d);
  if (corrected) return `${corrected} (corrected by you; the file says ${show(d.header.bunit)})`;
  if (fileStatesItsUnit(d)) return show(d.header.bunit);
  if (d.interpretation) return `${show(d.header.bunit)} (orientation map: values are angles, not a brightness)`;
  const u = d.unitDeclaration;
  if (!u) return `${show(d.header.bunit)} (unresolved)`;
  const rule = ruleOf(u.declaredBy);
  const who = rule !== null ? `your rule ${rule}` : "you";
  return u.stale ? `${show(d.header.bunit)} (unresolved; ${rule !== null ? `your rule ${rule}` : "you"} declared ${u.unit} for an earlier version of the file, not applied)` : `${u.unit} (declared by ${who}, not in the file)`;
}

/** How far the unit is established (ADR 0055): verified when the file's BUNIT was read, else the declaration's tier, else unresolved. */
function unitTier(d: ImageDataset): string {
  if (correctedUnit(d)) return `${tierText("declared")}: your correction of the file's BUNIT`;
  if (fileStatesItsUnit(d)) return `${tierText("verified")}: BUNIT is in the file and was read (not checked against the data)`;
  const u = d.unitDeclaration;
  if (d.interpretation || !u || u.stale) return tierText("unresolved");
  return tierText(u.tier, u.mappingVersion, u.declaredBy === "user" ? null : u.declaredBy);
}

/** A value of the sheet. It is cut at the edge of the column, so the whole text is its tooltip. */
function Val({ text, title }: { text: string; title?: string }) {
  return <dd className="mono" title={title ?? text}>{text}</dd>;
}

export function DatasetInspector({ d, problem, onReread }: { d: ImageDataset; problem?: Problem; onReread?: () => void }) {
  const h = d.header;
  return (
    <div className={s.wrap}>
      {problem ? (
        <div role="status" className={s.note}>
          <p style={{ margin: 0 }}>{PROBLEM_TEXT[problem]}{problem === "modified" && d.locations?.some((l) => l.link === "verified") ? " A verified copy of the earlier version is still read from (see Locations)." : ""}</p>
          {problem === "modified" && onReread ? <Button type="button" onClick={onReread}>Read it again</Button> : null}
        </div>
      ) : null}
      <dl className={s.facts}>
        <dt>File</dt>
        <Val text={d.name} title={d.path} />
        <dt>Size</dt>
        <Val text={bytes(d.sizeBytes)} />
        <dt>Brightness unit</dt>
        <Val text={unitText(d)} />
        <dt>Unit tier</dt>
        <Val text={unitTier(d)} />
        <dt>Pixel scale</dt>
        <Val text={pixelScale(d)} />
        <dt>Beam</dt>
        <Val text={beamText(d)} />
        <dt>Frame</dt>
        <Val text={show(h.radesys)} />
        <dt>Spectral frame</dt>
        <Val text={show(h.specsys)} />
      </dl>
      <div className={s.scroller} role="region" aria-label="Axes table" tabIndex={0}>
      <table className={s.axes}>
        <caption>Axes as declared in the header, in native FITS order</caption>
        <thead>
          <tr>
            <th scope="col">#</th>
            <th scope="col">Type</th>
            <th scope="col">Length</th>
            <th scope="col">Unit</th>
            <th scope="col">Reference value</th>
            <th scope="col">Step</th>
          </tr>
        </thead>
        <tbody>
          {h.axes.map((a) => (
            <tr key={a.axis}>
              <th scope="row">{a.axis}</th>
              <td>{show(a.ctype)}</td>
              <td>{a.length}</td>
              <td>{show(a.cunit)}</td>
              <td>{a.crval_status === "unresolved" ? <span className={s.unres}>unresolved</span> : show(a.crval)}</td>
              <td>{a.cdelt_status === "unresolved" ? <span className={s.unres}>unresolved</span> : show(a.cdelt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      </div>
      {d.corrections && !d.corrections.stale && Object.keys(d.corrections.state.wcs ?? {}).length > 0 ? (
        <p role="note" className={s.note}>The rows above are what the file says. Your corrections of {Object.keys(d.corrections.state.wcs ?? {}).sort().join(", ")} are in force for every view and readout (see Corrections below).</p>
      ) : null}
      <HeaderCards id={d.id} />
      <p className={s.note}>Pixel scale (sampling) and beam (resolution) are different things. Declared by the file, not verified. Unresolved means the header does not say; Vesqira does not guess.</p>
    </div>
  );
}
