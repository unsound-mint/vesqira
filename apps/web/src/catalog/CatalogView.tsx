import { useEffect, useState } from "react";
import { ApiError, catalogRows } from "../api/catalog";
import type { CatalogDataset, CatalogRows } from "../api/catalog";
import { Button, ScrollTable } from "../ui";
import { bytes, EM } from "./format";
import s from "./CatalogView.module.css";

const PAGE = 100;

/** The role a declared column plays, shown in its header so the table and the declaration cannot disagree. */
export function roles(d: CatalogDataset): Record<string, string> {
  const c = d.catalogDeclaration;
  const out: Record<string, string> = {};
  if (!c) return out;
  out[c.lon] = "longitude";
  out[c.lat] = "latitude";
  if (c.angle) {
    out[c.angle.column] = "angle";
    if (c.angle.sigmaColumn) out[c.angle.sigmaColumn] = "angle uncertainty";
  }
  for (const v of c.values) out[v] ??= "shown";
  return out;
}

/** The rows of a point catalog, a page at a time (ADR 0043). Cells are what the file says; a missing value is a dash, never 0. */
export function CatalogView({ dataset, problem }: { dataset: CatalogDataset; problem?: string }) {
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<CatalogRows | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const h = dataset.header;
  const byName = new Map(h.columns.map((c) => [c.name, c]));
  const role = roles(dataset);

  useEffect(() => {
    setOffset(0);
  }, [dataset.id, dataset.modifiedAt]);
  useEffect(() => {
    let live = true;
    setErr(null);
    catalogRows(dataset.id, offset, PAGE)
      .then((p) => live && setPage(p))
      .catch((e: unknown) => {
        if (!live) return;
        setPage(null);
        setErr(e instanceof ApiError ? e.message : "Could not read the rows.");
      });
    return () => {
      live = false;
    };
  }, [dataset.id, dataset.modifiedAt, offset]);

  const total = page?.total ?? h.rows;
  const last = Math.min(offset + PAGE, total);
  return (
    <section className={s.wrap} aria-label={`Catalog: ${dataset.name}`}>
      <header className={s.head}>
        <h2 className={s.title}>{dataset.name}</h2>
        <p className={s.meta}>
          {h.format} · {h.rows.toLocaleString("en-US")} rows · {h.columns.length} columns · {bytes(dataset.sizeBytes)} · a table of sources, no image
        </p>
      </header>
      {problem ? <p role="status" className={s.note}>{problem}</p> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {page ? (
        <>
          <ScrollTable label="Catalog rows" className={s.scroller}>
            <table className={s.table}>
              <caption className={s.caption}>Rows {(offset + 1).toLocaleString("en-US")} to {last.toLocaleString("en-US")} of {total.toLocaleString("en-US")}. A dash is a missing value, not zero. Numbers are shown as read: a leading zero or a trailing 0 is not kept.</caption>
              <thead>
                <tr>
                  {page.lines ? <th scope="col" className={s.num}>Line</th> : <th scope="col" className={s.num}>Row</th>}
                  {page.columns.map((c) => {
                    const info = byName.get(c);
                    return (
                      <th scope="col" key={c} title={info ? `${info.dtype}${info.unit ? `, unit ${info.unit} (declared by the file)` : ", no unit declared"}${info.scalar ? "" : ", array column: not shown"}` : undefined}>
                        <span className={s.colName}>{c}</span>
                        <span className={s.colMeta}>{info?.unit ? info.unit : info?.dtype ?? ""}{role[c] ? ` · ${role[c]}` : ""}</span>
                      </th>
                    );
                  })}
                </tr>
              </thead>
              <tbody>
                {page.rows.map((r, i) => (
                  <tr key={offset + i}>
                    <th scope="row" className={s.num}>{page.lines ? page.lines[i] : offset + i + 1}</th>
                    {r.map((v, j) => (
                      <td key={j} className={typeof v === "number" ? s.numeric : undefined}>{v === null || v === undefined ? <span className={s.missing} title="missing value">{EM}</span> : String(v)}</td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollTable>
          <nav className={s.pager} aria-label="Pages of rows">
            <Button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>Previous {PAGE}</Button>
            <Button type="button" disabled={last >= total} onClick={() => setOffset(offset + PAGE)}>Next {PAGE}</Button>
          </nav>
        </>
      ) : err ? null : <p className={s.note}>Reading rows…</p>}
    </section>
  );
}
