import { useEffect, useRef } from "react";
import { Select, Switch } from "../ui";
import { COLORMAPS, lut } from "./colormaps";
import type { ColormapId } from "./colormaps";
import { RANGES, STRETCHES, limits, normalize } from "./render";
import type { RangeMode, Stretch } from "./render";
import { Histogram } from "./Histogram";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { axialText, degrees, precisionNote, provenanceNote, spectralText, valueText } from "./format";
import s from "./Panels.module.css";

function otherValue(o: Viewer, p: { x: number; y: number }): string {
  if (!o.plane || p.x >= o.plane.head.width || p.y >= o.plane.head.height) return EM;
  const v = o.plane.data[p.y * o.plane.head.width + p.x] ?? null;
  return o.interp ? axialText(v, o.interp.unit) : valueText(v, o.view?.bunit ?? null);
}

/** Pixel, world and spectral readout for the pointer (or pinned) pixel. World values come from the worker's WCS. */
export function CoordinateReadout({ v, other }: { v: Viewer; other?: { name: string; v: Viewer } | null }) {
  const { view, probe, plane } = v;
  // a readout is shown only for the pixel and channel it was computed for; otherwise it is still loading
  const coord = v.coord && probe && plane && v.coord.x === probe.x && v.coord.y === probe.y && v.coord.channel === plane.head.channel ? v.coord : null;
  if (!view) return <p className={s.note}>{EM}</p>;
  const sky = view.sky;
  const spec = view.spectral;
  return (
    <div className={s.wrap}>
      <dl className={s.facts} aria-live="off">
        <dt>Pixel</dt>
        <dd className="mono">{probe ? `x ${probe.x + 1}, y ${probe.y + 1}` : EM}</dd>
        <dt>Value</dt>
        <dd className="mono">{plane && probe ? (plane.head.preview ? "preview only, full plane loading" : v.interp ? axialText(v.pixelValue, v.interp.unit) : valueText(v.pixelValue, view.bunit)) : EM}</dd>
        {v.interp ? (
          <>
            <dt>Reliability</dt>
            <dd className="mono">{v.interp.reliabilityId === null ? "no map declared" : probe && v.reliability ? valueText(v.reliability.data[probe.y * v.reliability.head.width + probe.x] ?? null, null) : EM}</dd>
          </>
        ) : null}
        {other && probe ? (
          <>
            <dt title={other.name}>Other map</dt>
            <dd className="mono">{otherValue(other.v, probe)}</dd>
          </>
        ) : null}
        {sky.kind === "celestial" ? (
          <>
            <dt>{sky.lon_name}</dt>
            <dd className="mono">{coord?.sky ? degrees(coord.sky.lon) : EM}</dd>
            <dt>{sky.lat_name}</dt>
            <dd className="mono">{coord?.sky ? degrees(coord.sky.lat) : EM}</dd>
            <dt>Frame</dt>
            <dd className="mono">{sky.frame ?? EM}{sky.frame_status === "unresolved" ? " (unresolved)" : sky.frame_status === "inferred" ? " (inferred from EQUINOX)" : ""}</dd>
          </>
        ) : (
          <>
            <dt>Sky</dt>
            <dd>No celestial WCS: image pixels only</dd>
          </>
        )}
        {spec ? (
          <>
            <dt>{spec.name}</dt>
            <dd className="mono">{spec.resolved ? spectralText(coord?.spectral?.value ?? plane?.head.spectral_value ?? null, spec.unit) : "unresolved"}{spec.resolved ? provenanceNote(spec.unit_status) : ""}</dd>
            <dt>Spectral frame</dt>
            <dd className="mono">{spec.specsys ?? "unresolved"}</dd>
            {spec.velocity === "unresolved" ? (
              <>
                <dt>Velocity</dt>
                <dd>unresolved{spec.resolved ? `: ${spec.note ?? "no velocity convention declared"}` : ""}</dd>
              </>
            ) : null}
          </>
        ) : null}
      </dl>
      <p className={s.note}>
        {view.bunit_status === "unresolved" ? "Brightness unit unresolved: the header has no BUNIT. " : ""}
        {view.bunit_status === "user" ? `Brightness unit (${view.bunit}) declared by you: the header has no BUNIT. ` : ""}
        {v.coordError ?? "Pixel numbers follow FITS (start at 1). Click the image to pin a pixel; Esc releases it."}
      </p>
    </div>
  );
}

/** Colour mapping, stretch and range, with a legend that always says what "no value" looks like. */
export function DisplayControls({ v }: { v: Viewer }) {
  const bar = useRef<HTMLCanvasElement>(null);
  const stats = v.plane?.head.stats ?? null;
  const lim = stats ? limits(stats, v.range, v.customLimits) : null;
  const unit = v.view?.bunit ?? null;
  const axial = v.interp !== null;

  useEffect(() => {
    const c = bar.current;
    const ctx = c?.getContext("2d");
    if (!c || !ctx) return;
    const table = lut(axial ? "twilight" : v.colormap);
    const img = ctx.createImageData(c.width, 1);
    for (let x = 0; x < c.width; x++) {
      // the bar shows what a value at this position on the axis looks like after the stretch
      const i = Math.round(normalize(x / (c.width - 1), 0, 1, axial ? "linear" : v.stretch) * 255) * 3;
      img.data.set([table[i]!, table[i + 1]!, table[i + 2]!, 255], x * 4);
    }
    ctx.putImageData(img, 0, 0);
  }, [v.colormap, v.stretch, axial]);

  if (axial) {
    return (
      <div className={s.wrap}>
        <div className={s.legend}>
          <canvas ref={bar} className={s.bar} width={256} height={1} role="img" aria-label="Cyclic colour bar from 0 to 180 degrees; the ends have the same colour" />
          <div className={s.ends}>
            <span className="mono">0°</span>
            <span className="mono">90°</span>
            <span className="mono">180°</span>
          </div>
        </div>
        <Switch label="Orientation lines" checked={v.showLines} onChange={(e) => v.setShowLines(e.target.checked)} />
        <p className={s.note}>
          Axial orientation: 0° and 180° are the same orientation, so the colours wrap. Angles run from the image +x axis towards +y (counter-clockwise on
          screen). The file declares no angle convention; Vesqira assumes the VGT pipeline’s. Line opacity follows the reliability map relative to its own 99th percentile.
        </p>
        <div className={s.nodata}>
          <span className={s.swatch} aria-hidden="true" />
          <span>No value{stats ? `: ${stats.nonfinite_count.toLocaleString("en-US")} pixels` : ""}. Hatched, never drawn as an angle.</span>
        </div>
      </div>
    );
  }

  return (
    <div className={s.wrap}>
      <Select label="Colormap" value={v.colormap} onChange={(e) => v.setColormap(e.target.value as ColormapId)}>
        {COLORMAPS.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
      </Select>
      <Select label="Stretch" value={v.stretch} onChange={(e) => v.setStretch(e.target.value as Stretch)}>
        {STRETCHES.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
      </Select>
      <Select label="Range" value={v.range} onChange={(e) => {
        const mode = e.target.value as RangeMode;
        if (mode === "custom" && !v.customLimits && stats) v.setCustomLimits(limits(stats, v.range));   // start from what is shown now
        v.setRange(mode);
      }}>
        {RANGES.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
      </Select>
      {v.plane && stats && !v.plane.head.preview ? <Histogram data={v.plane.data} stats={stats} limits={lim} unit={unit} onChange={(lo, hi) => { v.setCustomLimits([lo, hi]); v.setRange("custom"); }} /> : null}
      <div className={s.legend}>
        <canvas ref={bar} className={s.bar} width={256} height={1} role="img" aria-label={`${v.colormap} colour bar from low to high`} />
        <div className={s.ends}>
          <span className="mono">{lim ? valueText(lim[0], unit) : EM}</span>
          <span className="mono">{lim ? valueText(lim[1], unit) : EM}</span>
        </div>
        <div className={s.nodata}>
          <span className={s.swatch} aria-hidden="true" />
          <span>
            No value (NaN or BLANK){stats ? `: ${stats.nonfinite_count.toLocaleString("en-US")} of ${(stats.finite_count + stats.nonfinite_count).toLocaleString("en-US")} pixels` : ""}. Hatched, never drawn as zero.{stats && stats.overflow_count > 0 ? ` ${stats.overflow_count.toLocaleString("en-US")} of them are finite in the file but too large for float32.` : ""}
          </span>
        </div>
      </div>
      {v.view?.bunit_status === "user" ? <p className={s.note}>The unit ({v.view.bunit}) is the one you declared; the file has no BUNIT.</p> : null}
      <p className={s.note}>{v.range === "custom" ? "Limits set by hand; values outside them are drawn at the ends of the colour bar." : "Limits come from the plane’s finite pixels only."}{v.plane?.head.scaled ? " BSCALE/BZERO applied." : ""}{precisionNote(v.plane?.head.source_bitpix ?? null)}</p>
    </div>
  );
}
