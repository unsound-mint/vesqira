import { useEffect, useId, useMemo, useState } from "react";
import { getMaskPlane, maskTypeLabel } from "../api/masks";
import type { Mask, MaskPlane } from "../api/masks";
import { HATCH, maskPath } from "./maskHatch";
import { useMasks, useShownMasks } from "./useMasks";
import s from "./MaskOverlay.module.css";

/** A tile of a type's hatch, for the overlay and for the legend's swatch. */
function Tile({ type, id }: { type: Mask["type"]; id: string }) {
  const h = HATCH[type];
  return (
    <pattern id={id} width="6" height="6" patternUnits="userSpaceOnUse">
      {h.dots ? <circle cx="3" cy="3" r="1.1" className={s.ink} /> : <path d={h.tile} className={s.stroke} />}
    </pattern>
  );
}

/** The masks being shown for this dataset and plane, by id; the mask list is the owner's of the legend and the overlay. */
export function useShownMaskList(datasetId: number | null): Mask[] {
  const ids = useShownMasks(datasetId);
  const { masks } = useMasks(datasetId, ids.length > 0);
  return useMemo(() => (masks ?? []).filter((m) => ids.includes(m.id)), [masks, ids]);
}

/**
 * The shown masks of the plane drawn over the picture, one hatch per type, in screen pixels (north up). The mask does not replace a
 * pixel: the value is still readable under it. Where the mask file is not readable the overlay says so instead of drawing nothing.
 */
export function MaskOverlay({ list, channel, width, height, cell }: { list: Mask[]; channel: number; width: number; height: number; cell: number }) {
  const uid = useId().replace(/:/g, "");
  const [planes, setPlanes] = useState<Record<number, MaskPlane>>({});
  const [problem, setProblem] = useState<string | null>(null);
  const key = list.map((m) => m.id).join(",");
  useEffect(() => {
    const ac = new AbortController();
    setProblem(null);
    Promise.all(list.map((m) => getMaskPlane(m.id, channel, ac.signal).then((p) => [m.id, p] as const)))
      .then((got) => { if (!ac.signal.aborted) setPlanes(Object.fromEntries(got)); })
      .catch((e: unknown) => { if (!ac.signal.aborted) setProblem(e instanceof Error ? e.message : "A mask could not be read."); });
    return () => ac.abort();
  }, [key, channel]); // eslint-disable-line react-hooks/exhaustive-deps
  const drawn = useMemo(
    () => list.flatMap((m) => {
      const p = planes[m.id];
      return p && p.width === width && p.height === height ? [{ m, ...maskPath(p.data, p.width, p.height, cell) }] : [];
    }),
    [list, planes, width, height, cell],
  );
  return (
    <>
      <svg className={s.overlay} width={width * cell} height={height * cell} aria-hidden="true" data-testid="mask-overlay">
        <defs>{list.map((m) => <Tile key={m.id} type={m.type} id={`${uid}-${m.id}`} />)}</defs>
        {drawn.map(({ m, d }) => <path key={m.id} d={d} fill={`url(#${uid}-${m.id})`} data-mask-type={m.type} />)}
      </svg>
      {problem ? <p className={s.problem} role="alert">{problem}</p> : null}
    </>
  );
}

/** What each hatch means, in words: the type, the mask's name and how many pixels it flags. Always shown with the overlay. */
export function MaskLegend({ list }: { list: Mask[] }) {
  const uid = useId().replace(/:/g, "");
  if (list.length === 0) return null;
  return (
    <ul className={s.legend} aria-label="Masks shown" data-testid="mask-legend">
      {list.map((m) => (
        <li key={m.id}>
          <svg width="18" height="12" aria-hidden="true"><defs><Tile type={m.type} id={`${uid}-${m.id}`} /></defs><rect width="18" height="12" fill={`url(#${uid}-${m.id})`} className={s.swatch} /></svg>
          <span>{maskTypeLabel(m.type)}: {m.name}</span>
          <span className={s.count}>{m.flagged.toLocaleString("en-US")} pixels{m.stale ? ", stale" : ""}</span>
        </li>
      ))}
    </ul>
  );
}
