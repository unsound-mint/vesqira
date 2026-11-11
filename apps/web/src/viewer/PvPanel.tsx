import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, MouseEvent } from "react";
import { Button, Input, Select } from "../ui";
import { getPv } from "../api/channelmaps";
import type { PvDiagram } from "../api/channelmaps";
import { ApiError } from "../api/http";
import { lut } from "./colormaps";
import { lineValid, pathLength } from "./region";
import { paintPixels, rampCss } from "./channelGrid";
import type { LineShape } from "../api/planes";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import s from "./PvPanel.module.css";

const isAbort = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const NODATA: [[number, number, number], [number, number, number]] = [[27, 34, 44], [85, 98, 117]];
const num = (v: number) => Number(v.toPrecision(5)).toString();

/** Where a pointer is on the diagram: the sample along the path and the channel row, from the plot's own geometry. */
export function cellAt(fx: number, fy: number, offsets: number, channels: number, flip: boolean): { o: number; c: number } {
  const o = Math.min(offsets - 1, Math.max(0, Math.floor(fx * offsets)));
  const row = Math.min(channels - 1, Math.max(0, Math.floor(fy * channels))); // row 0 is the top of the plot, which holds the highest spectral value
  return { o, c: flip ? row : channels - 1 - row };
}

/** Whether channel 0 is drawn at the top: the spectral value always increases upwards, as in a conventional diagram. */
export const axisFlipped = (axis: ArrayLike<number>): boolean => axis.length > 1 && Number.isFinite(axis[0]) && Number.isFinite(axis[axis.length - 1]) && axis[axis.length - 1]! < axis[0]!;

/** Text of one cell for the readout: offset, spectral value and the value, with how many samples were averaged. */
export function cellText(d: PvDiagram, o: number, c: number, bunit: string | null): string {
  const unit = d.head.offset.unit === "arcsec" ? "arcsec" : "pixels";
  const sp = d.head.spectral;
  const a = d.axis[c]!;
  const val = d.values[c * d.head.offsets + o]!;
  const n = d.counts[c * d.head.offsets + o]!;
  const width = d.head.width.cross_samples;
  return `offset ${num(d.offsets[o]!)} ${unit} · ${sp.axis ?? "channel"} ${Number.isFinite(a) ? `${num(a)}${sp.unit ? ` ${sp.unit}` : ""}` : "undefined"} (channel ${d.head.first_channel + c + 1}) · ${Number.isFinite(val) ? `${num(val)}${bunit ? ` ${bunit}` : ""}` : "no value"} · ${n} of ${width} samples across`;
}

function PvPlot({ d, v }: { d: PvDiagram; v: Viewer }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [cur, setCur] = useState<{ o: number; c: number } | null>(null);
  const no = d.head.offsets, nc = d.head.channels_returned;
  const flip = axisFlipped(d.axis);
  const stats = d.head.stats;
  const lo = stats.p01 ?? stats.min ?? null, hi = stats.p99 ?? stats.max ?? null;
  const scale = lo !== null && hi !== null && lo < hi ? { lo, hi } : null;
  const bunit = v.view?.bunit ?? null;

  useEffect(() => {
    const c = canvas.current;
    if (!c || !scale) return;
    c.width = no;
    c.height = nc;
    // the plot reads upward: row 0 of the data is drawn at the bottom unless the axis runs the other way
    const rows = new Float32Array(no * nc);
    for (let r = 0; r < nc; r++) rows.set(d.values.subarray((flip ? r : nc - 1 - r) * no, (flip ? r : nc - 1 - r) * no + no), (nc - 1 - r) * no);
    const px = paintPixels(rows, no, nc, scale, lut(v.colormap), v.stretch, NODATA);
    c.getContext("2d")?.putImageData(new ImageData(px as Uint8ClampedArray<ArrayBuffer>, no, nc), 0, 0);
  }, [d, scale?.lo, scale?.hi, v.colormap, v.stretch, flip]); // eslint-disable-line react-hooks/exhaustive-deps

  function pick(e: MouseEvent<HTMLDivElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    setCur(cellAt((e.clientX - r.left) / r.width, (e.clientY - r.top) / r.height, no, nc, flip));
  }
  function key(e: KeyboardEvent) {
    const step = e.shiftKey ? 10 : 1;
    const dx = e.key === "ArrowRight" ? step : e.key === "ArrowLeft" ? -step : 0;
    const dy = e.key === "ArrowUp" ? step : e.key === "ArrowDown" ? -step : 0; // up is increasing spectral value
    if (!dx && !dy) return;
    e.preventDefault();
    const from = cur ?? { o: Math.floor(no / 2), c: Math.floor(nc / 2) };
    const dc = flip ? -dy : dy;
    setCur({ o: Math.min(no - 1, Math.max(0, from.o + dx)), c: Math.min(nc - 1, Math.max(0, from.c + dc)) });
  }

  const sp = d.head.spectral;
  const yLo = flip ? d.axis[nc - 1]! : d.axis[0]!, yHi = flip ? d.axis[0]! : d.axis[nc - 1]!;
  const row = cur ? (flip ? cur.c : nc - 1 - cur.c) : 0;
  return (
    <figure className={s.fig}>
      <div
        className={s.plot}
        style={{ aspectRatio: `${Math.max(no, 1)} / ${Math.max(nc, 1)}`, maxHeight: "22rem" }}
        tabIndex={0}
        role="img"
        aria-label={`Position-velocity diagram: ${no} positions along the path by ${nc} channels. Arrow keys move the cursor.`}
        onMouseMove={pick}
        onMouseLeave={() => setCur(null)}
        onKeyDown={key}
        data-testid="pv-plot"
      >
        {scale ? <canvas ref={canvas} className={s.canvas} /> : <span className={s.empty}>No sample has a value.</span>}
        {cur ? <span className={s.cursor} style={{ left: `${((cur.o + 0.5) / no) * 100}%`, top: `${((row + 0.5) / nc) * 100}%` }} aria-hidden="true" /> : null}
      </div>
      <div className={s.axes}>
        <span className={s.mono}>{num(d.offsets[0]!)}</span>
        <span>offset along the path ({d.head.offset.unit}), from the first vertex</span>
        <span className={s.mono}>{num(d.offsets[no - 1]!)}</span>
      </div>
      <div className={s.axes}>
        <span className={s.mono}>{Number.isFinite(yLo) ? num(yLo) : EM}</span>
        <span>{sp.axis ?? "channel"}{sp.unit ? ` (${sp.unit})` : ""}, increasing upwards</span>
        <span className={s.mono}>{Number.isFinite(yHi) ? num(yHi) : EM}</span>
      </div>
      <p className={s.readout} role="status" aria-live="polite">{cur ? cellText(d, cur.o, cur.c, bunit) : "Point at the diagram, or focus it and use the arrow keys, to read a value."}</p>
      {scale ? (
        <div className={s.bar}><span>{num(scale.lo)}{bunit ? ` ${bunit}` : ""}</span><span className={s.ramp} style={{ background: rampCss(v.colormap) }} /><span>{num(scale.hi)}{bunit ? ` ${bunit}` : ""}</span></div>
      ) : null}
    </figure>
  );
}

/** The path vertices as text, one "x, y" per line with pixel numbers from 1: the keyboard route to the path. */
const toText = (l: LineShape | null) => (l ? l.points.map((p) => `${p[0] + 1}, ${p[1] + 1}`).join("\n") : "");
export function parseVertices(text: string): [number, number][] | null {
  const out: [number, number][] = [];
  for (const line of text.split("\n")) {
    if (!line.trim()) continue;
    const m = /^\s*(-?\d+)\s*[,; ]\s*(-?\d+)\s*$/.exec(line);
    if (!m) return null;
    const x = Number(m[1]) - 1, y = Number(m[2]) - 1;
    if (x < 0 || y < 0) return null;
    out.push([x, y]);
  }
  return out.length >= 2 ? out : null;
}

/** Position-velocity diagram along the line drawn on the map (ADR 0049): path, width, interpolation, spectral axis, plot and readout. */
export function PvPanel({ v }: { v: Viewer }) {
  const { path, setPath } = v;
  const channels = v.view?.channels ?? 0;
  const [text, setText] = useState("");
  const [textError, setTextError] = useState<string | null>(null);
  const [width, setWidth] = useState("1");
  const [widthUnit, setWidthUnit] = useState<"pixel" | "arcsec">("pixel");
  const [interp, setInterp] = useState<"linear" | "nearest">("linear");
  const [first, setFirst] = useState("1");
  const [last, setLast] = useState("");
  const [unit, setUnit] = useState("");
  const [convention, setConvention] = useState("radio");
  const [result, setResult] = useState<PvDiagram | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const ac = useRef<AbortController | null>(null);
  const targets = v.offers?.spectral?.targets ?? [];
  const target = targets.find((t) => t.unit === unit);
  const needsConvention = Boolean(unit && target?.needs_convention);

  useEffect(() => {
    setText(toText(path));
    if (path) {
      setWidth(String(path.width));
      setWidthUnit(path.widthUnit);
    }
  }, [path?.points, path?.width, path?.widthUnit]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setLast(channels > 0 ? String(channels) : "");
    setFirst("1");
    setResult(null);
    setError(null);
    ac.current?.abort();
  }, [v.datasetId, channels]);
  useEffect(() => () => ac.current?.abort(), []); // closing the panel cancels a running slice

  const w = Number(width);
  const widthBad = !Number.isFinite(w) || w <= 0 || w > 100000;
  const range = { first: Number(first), last: Number(last) };
  const rangeBad = !Number.isInteger(range.first) || !Number.isInteger(range.last) || range.first < 1 || range.last < range.first || range.last > channels;

  const apply = () => {
    const pts = parseVertices(text);
    if (!pts) return setTextError("Give at least two vertices, one per line as “x, y”, with pixel numbers from 1.");
    setTextError(null);
    setPath({ shape: "line", points: pts, width: widthBad ? 1 : w, widthUnit });
  };

  const compute = useCallback(() => {
    if (!lineValid(path) || widthBad || rangeBad || v.datasetId === null) return;
    ac.current?.abort();
    const c = new AbortController();
    ac.current = c;
    setRunning(true);
    setError(null);
    getPv(v.datasetId, {
      points: path.points, width: w, widthUnit, interpolation: interp, first: range.first - 1, last: range.last - 1,
      ...(unit ? { unit } : {}), ...(unit && needsConvention ? { convention } : {}),
    }, c.signal)
      .then((d) => { if (ac.current === c) setResult(d); })
      .catch((e: unknown) => { if (!isAbort(e)) { setResult(null); setError(e instanceof ApiError ? e.message : "The diagram could not be computed."); } })
      .finally(() => { if (ac.current === c) setRunning(false); });
  }, [path, w, widthUnit, interp, range.first, range.last, unit, convention, needsConvention, widthBad, rangeBad, v.datasetId]);

  const facts = useMemo(() => (result ? [
    `${result.head.path.samples} positions along ${Number((result.head.path.length_pixels ?? 0).toFixed(1))} pixels, about one pixel apart; offsets are ${result.head.offset.method}.`,
    `Width: ${result.head.width.note}; ${result.head.width.method}.`,
    `${result.head.interpolation.mode === "linear" ? "Interpolated: " : ""}${result.head.interpolation.method}.`,
    ...(result.head.outside_samples > 0 ? [`${result.head.outside_samples} samples fell outside the image and hold no value.`] : []),
    result.head.spectral.refused ? `Spectral axis: ${result.head.spectral.refused}` : result.head.spectral.basis ? `Spectral axis converted: ${result.head.spectral.basis}${result.head.spectral.specsys ? ` Reference frame ${result.head.spectral.specsys}, unchanged.` : ""}` : `Spectral axis in the file's unit${result.head.spectral.specsys ? `, reference frame ${result.head.spectral.specsys}` : ""}.`,
    "Rows are channels (the axis values are labelled at the ends); columns are samples along the path. No-value samples are hatched, never zero.",
  ] : []), [result]);

  if (!v.view) return <p className={s.note}>{EM}</p>;
  if (v.interp) return <p className={s.note}>A position-velocity cut averages values across channels and along a width, which is not defined for angles of an axial map.</p>;
  if (channels < 2) return <p className={s.note}>This file has no channel axis of more than one channel, so there is no velocity axis to cut.</p>;

  return (
    <div className={s.wrap}>
      <p className={s.note}>{path ? `Path of ${path.points.length} vertices, ${Number(pathLength(path.points).toFixed(1))} pixels long.` : "Choose the Line tool in the Region section and drag on the image, or enter the vertices below."}</p>
      <div className={s.field}>
        <label htmlFor="pv-vertices" className={s.label}>Vertices (x, y per line; pixel numbers from 1)</label>
        <textarea id="pv-vertices" className={s.area} rows={3} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
      </div>
      {textError ? <p role="alert" className={s.err}>{textError}</p> : null}
      <div className={s.fields}>
        <Input label="Width" inputMode="decimal" value={width} onChange={(e) => setWidth(e.target.value)} />
        <Select label="Width unit" value={widthUnit} onChange={(e) => setWidthUnit(e.target.value as "pixel" | "arcsec")}>
          <option value="pixel">pixels</option>
          <option value="arcsec">arcsec</option>
        </Select>
        <Select label="Sampling" value={interp} onChange={(e) => setInterp(e.target.value as "linear" | "nearest")}>
          <option value="linear">Bilinear interpolation</option>
          <option value="nearest">Nearest pixel (no interpolation)</option>
        </Select>
        <Select label="Spectral axis" value={unit} onChange={(e) => setUnit(e.target.value)}>
          <option value="">{v.offers?.spectral?.native_unit ? `The file's unit (${v.offers.spectral.native_unit})` : "The file's axis"}</option>
          {targets.map((t) => <option key={t.unit} value={t.unit} disabled={!t.enabled} title={t.reason ?? undefined}>{t.unit}{t.enabled ? "" : ` (${t.reason ?? "not available"})`}</option>)}
        </Select>
        {needsConvention ? (
          <Select label="Velocity convention" value={convention} onChange={(e) => setConvention(e.target.value)}>
            <option value="radio">Radio</option>
            <option value="optical">Optical</option>
            <option value="relativistic">Relativistic</option>
          </Select>
        ) : null}
        <Input label="First channel" inputMode="numeric" value={first} onChange={(e) => setFirst(e.target.value)} />
        <Input label="Last channel" inputMode="numeric" value={last} onChange={(e) => setLast(e.target.value)} />
      </div>
      {widthBad ? <p role="alert" className={s.err}>The width is a positive number.</p> : null}
      {rangeBad ? <p role="alert" className={s.err}>Channel numbers are whole numbers from 1 to {channels}, the last not before the first.</p> : null}
      <div className={s.actions}>
        <Button type="button" onClick={apply}>Use these vertices</Button>
        <Button type="button" variant="primary" onClick={compute} disabled={running || !lineValid(path) || widthBad || rangeBad}>{running ? "Computing…" : "Compute the diagram"}</Button>
        {running ? <Button type="button" onClick={() => ac.current?.abort()}>Cancel</Button> : null}
        <Button type="button" onClick={() => { setPath(null); setResult(null); }} disabled={!path}>Clear the line</Button>
      </div>
      {running ? <p className={s.note} role="status">Reading the cube along the path…</p> : null}
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {result ? (
        <>
          <PvPlot d={result} v={v} />
          <ul className={s.facts}>{facts.map((f, i) => <li key={i}>{f}</li>)}</ul>
        </>
      ) : null}
    </div>
  );
}
