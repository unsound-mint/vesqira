import { useState } from "react";
import type { GeometryEntry } from "../scene/geometry";
import { MARKER_STRIDE } from "../api/overlays";
import { valueColor } from "../scene/overlays";
import type { SceneOverlay } from "../scene/scene";
import { SourceValues } from "./SourceRow";
import type { PickedSource } from "./SourceRow";
import s from "./CatalogSources.module.css";

type CatalogOverlay = Extract<SceneOverlay, { kind: "catalog" }>;
const PAGE = 50;
const num = (v: number) => String(Number(v.toPrecision(4)));

/** One drawn row of a catalog overlay, as the worker placed it. */
export type Source = { row: number; x: number; y: number; angle: number | null; value: number | null };

/** The drawn rows of a ready catalog geometry, in row order. */
export function sourcesOf(entry: GeometryEntry | undefined): Source[] {
  if (!entry || entry.status !== "ready" || entry.g.kind !== "catalog") return [];
  const m = entry.g.markers;
  const out: Source[] = [];
  for (let i = 0; i + MARKER_STRIDE <= m.length; i += MARKER_STRIDE) {
    out.push({ row: m[i + 5]!, x: m[i]!, y: m[i + 1]!, angle: Number.isFinite(m[i + 2]!) ? m[i + 2]! : null, value: Number.isFinite(m[i + 4]!) ? m[i + 4]! : null });
  }
  return out;
}

/**
 * What a catalog overlay adds to its row in the overlay list: the colour legend (the numbers at both ends, so colour is never
 * the only cue) and the list of drawn sources, reachable by keyboard, each a button that picks that row (the same row a click
 * on the map shows). The picked row's values are shown here too.
 */
export function CatalogSources({ o, entry, picked, onPick }: { o: CatalogOverlay; entry: GeometryEntry | undefined; picked: PickedSource | null; onPick: (p: PickedSource | null) => void }) {
  const [shown, setShown] = useState(PAGE);
  if (!entry || entry.status !== "ready" || entry.g.kind !== "catalog") return null;
  const head = entry.g.head;
  const sources = sourcesOf(entry);
  const k = head.color;
  const stops = [0, 0.25, 0.5, 0.75, 1].map((t) => valueColor(t, 0, 1)).join(", ");
  const mine = picked && picked.overlayId === o.id ? picked.row : null;
  return (
    <div className={s.wrap}>
      {k && k.min !== null && k.min !== undefined ? (
        <div className={s.legend} role="group" aria-label={`Colour legend: ${k.column}`}>
          <span className={s.bar} style={{ background: `linear-gradient(to right, ${stops})` }} aria-hidden="true" />
          <span className={s.ends}>
            <span>{num(k.min)}</span>
            <span className={s.what}>{k.column}{k.unit ? ` (${k.unit})` : ""}</span>
            <span>{num(k.max ?? k.min)}</span>
          </span>
        </div>
      ) : null}
      <details className={s.details}>
        <summary>{sources.length} {sources.length === 1 ? "source" : "sources"} drawn: list</summary>
        {sources.length === 0 ? <p className={s.note}>No row is drawn.</p> : (
          <>
            <ul className={s.list} aria-label="Sources drawn by this overlay">
              {sources.slice(0, shown).map((r) => (
                <li key={r.row}>
                  <button type="button" className={s.pick} aria-pressed={mine === r.row} onClick={() => onPick(mine === r.row ? null : { overlayId: o.id, row: r.row })}>
                    Row {r.row}
                  </button>
                  <span className={s.facts}>
                    {k ? (r.value === null ? `${k.column}: no value` : `${k.column} ${num(r.value)}`) : null}
                    {r.angle !== null ? `${k ? ", " : ""}vector ${Math.round(r.angle * 10) / 10}° in base pixels` : null}
                  </span>
                </li>
              ))}
            </ul>
            {shown < sources.length ? (
              <button type="button" className={s.more} onClick={() => setShown(shown + PAGE)}>Show {Math.min(PAGE, sources.length - shown)} more of {sources.length - shown}</button>
            ) : null}
          </>
        )}
        {mine !== null ? (
          <div className={s.detail} role="region" aria-live="polite" aria-label={`Row ${mine}`}>
            <SourceValues datasetId={o.datasetId} row={mine} />
          </div>
        ) : null}
      </details>
    </div>
  );
}
