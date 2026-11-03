import type { Dataset } from "../api/catalog";
import { bytes, show } from "./format";
import s from "./DatasetInspector.module.css";

/** What the file header declares, in native FITS axis order. Nothing here is verified. */
export function DatasetInspector({ d }: { d: Dataset }) {
  const h = d.header;
  return (
    <div className={s.wrap}>
      <dl className={s.facts}>
        <dt>File</dt>
        <dd className="mono" title={d.path}>{d.name}</dd>
        <dt>Size</dt>
        <dd className="mono">{bytes(d.sizeBytes)}</dd>
        <dt>Brightness unit</dt>
        <dd className="mono">{show(h.bunit)}{h.bunit_status === "unresolved" ? " (unresolved)" : ""}</dd>
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
      <p className={s.note}>Declared by the file, not verified. Unresolved means the header does not say; Vesqira does not guess.</p>
    </div>
  );
}
