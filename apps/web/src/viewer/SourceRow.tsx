import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { ApiError, catalogRows } from "../api/catalog";
import type { CatalogRows } from "../api/catalog";
import { IconButton } from "../ui";
import s from "./SourceRow.module.css";

/** The row a person picked on the map or in the list: which catalog overlay, which table row (1-based), and where it was drawn (picture coordinates). */
export type PickedSource = { overlayId: string; row: number };

type State = { status: "loading" } | { status: "ready"; data: CatalogRows } | { status: "error"; message: string };

/** One row of a catalog as the file gave it (a missing value is shown as missing, never 0) and the line it came from. */
export function useCatalogRow(datasetId: number, row: number): State {
  const [state, setState] = useState<State>({ status: "loading" });
  useEffect(() => {
    let live = true;
    setState({ status: "loading" });
    catalogRows(datasetId, row - 1, 1)
      .then((data) => live && setState({ status: "ready", data }))
      .catch((e: unknown) => live && setState({ status: "error", message: e instanceof ApiError ? e.message : "The row could not be read." }));
    return () => {
      live = false;
    };
  }, [datasetId, row]);
  return state;
}

const cellText = (v: unknown): string => (v === null || v === undefined ? "missing" : String(v));

/** The values of one catalog row, as a definition list (also what the keyboard list shows for its selected row). */
export function SourceValues({ datasetId, row }: { datasetId: number; row: number }) {
  const st = useCatalogRow(datasetId, row);
  if (st.status === "loading") return <p className={s.note} role="status">Reading row {row}…</p>;
  if (st.status === "error") return <p className={s.error} role="alert">{st.message}</p>;
  const { columns, rows, lines } = st.data;
  const cells = rows[0];
  if (!cells) return <p className={s.error} role="alert">Row {row} is not in the catalog.</p>;
  return (
    <>
      <dl className={s.values}>
        {columns.map((c, i) => (
          <div key={c} className={s.pair}>
            <dt>{c}</dt>
            <dd data-missing={cells[i] === null || cells[i] === undefined ? "true" : undefined}>{cellText(cells[i])}</dd>
          </div>
        ))}
      </dl>
      <p className={s.note}>Row {row}{lines && lines[0] !== undefined && lines[0] !== null ? `, line ${lines[0]} of the file` : ""}.</p>
    </>
  );
}

/**
 * A small non-modal popover beside a clicked marker with its row. It does not take focus (the viewer keeps its keys); Escape
 * in the viewer, the close button, or picking another marker closes it. `at` is the marker's position in screen pixels inside
 * the picture's frame, kept inside it.
 */
export function SourcePopover({ datasetId, row, name, at, frame, onClose }: {
  datasetId: number; row: number; name: string; at: { x: number; y: number }; frame: { w: number; h: number }; onClose: () => void;
}) {
  const WIDTH = 240;
  const left = Math.max(4, Math.min(frame.w - WIDTH - 4, at.x + 12));
  const top = Math.max(4, Math.min(Math.max(4, frame.h - 120), at.y + 12));
  return (
    <div className={s.popover} style={{ left, top, width: WIDTH }} role="dialog" aria-label={`${name}, row ${row}`} data-testid="source-popover">
      <div className={s.head}>
        <span className={s.title}>{name}</span>
        <IconButton icon={X} label="Close the row" onClick={onClose} />
      </div>
      <SourceValues datasetId={datasetId} row={row} />
    </div>
  );
}
