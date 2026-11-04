import type { Dataset, Problem } from "../api/catalog";
import { Button } from "../ui";
import { bytes, show } from "./format";
import { beamText, pixelScale } from "./resolution";
import { HeaderCards } from "./HeaderCards";
import s from "./DatasetInspector.module.css";

/** What the file header declares, in native FITS axis order. Nothing here is verified. */
const PROBLEM_TEXT: Record<Problem, string> = {
  modified: "This file has changed on disk since it was registered, so the header and figures made earlier may no longer describe it. Viewing is paused until it is read again.",
  missing: "This file is not at its registered path any more. Figures and boards made from it keep their pictures; nothing here can read it until it is back.",
  not_allowed: "This file is outside the source folders this program was started with, so it cannot be read.",
};

export function DatasetInspector({ d, problem, onReread }: { d: Dataset; problem?: Problem; onReread?: () => void }) {
  const h = d.header;
  return (
    <div className={s.wrap}>
      {problem ? (
        <div role="status" className={s.note}>
          <p style={{ margin: 0 }}>{PROBLEM_TEXT[problem]}</p>
          {problem === "modified" && onReread ? <Button type="button" onClick={onReread}>Read it again</Button> : null}
        </div>
      ) : null}
      <dl className={s.facts}>
        <dt>File</dt>
        <dd className="mono" title={d.path}>{d.name}</dd>
        <dt>Size</dt>
        <dd className="mono">{bytes(d.sizeBytes)}</dd>
        <dt>Brightness unit</dt>
        <dd className="mono">{show(h.bunit)}{h.bunit_status === "unresolved" ? " (unresolved)" : ""}</dd>
        <dt>Pixel scale</dt>
        <dd className="mono">{pixelScale(d)}</dd>
        <dt>Beam</dt>
        <dd className="mono">{beamText(d)}</dd>
        <dt>Frame</dt>
        <dd className="mono">{show(h.radesys)}</dd>
        <dt>Spectral frame</dt>
        <dd className="mono">{show(h.specsys)}</dd>
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
      <HeaderCards id={d.id} />
      <p className={s.note}>Pixel scale (sampling) and beam (resolution) are different things. Declared by the file, not verified. Unresolved means the header does not say; Vesqira does not guess.</p>
    </div>
  );
}
