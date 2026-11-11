import { useEffect, useMemo, useRef, useState } from "react";
import { toShape } from "../api/regions";
import { sameRegion } from "./region";
import { Basis, Select, Switch } from "../ui";
import { COLORMAPS, lut } from "./colormaps";
import type { ColormapId } from "./colormaps";
import { RANGES, STRETCHES, limits, normalize } from "./render";
import type { RangeMode, Stretch } from "./render";
import { Histogram } from "./Histogram";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import { axialText, degrees, limitText, precisionNote, provenanceNote, spectralText, unitLabel, valueText } from "./format";
import s from "./Panels.module.css";

/** What the other map holds at the pixel the pointer maps to: the same pixel on one grid, `at` (from the worker's transform) otherwise. */
type OtherAt = { x: number; y: number } | "outside" | "pending";
function otherValue(o: Viewer, probe: { x: number; y: number }, at?: OtherAt): string {
  if (at === "outside") return "outside this map's footprint";
  if (at === "pending") return "computing the link…";
  const p = at ?? probe;
  if (!o.plane || p.x >= o.plane.head.width || p.y >= o.plane.head.height) return EM;
  const v = o.plane.data[p.y * o.plane.head.width + p.x] ?? null;
  return o.interp ? axialText(v, o.interp.unit) : valueText(v, o.view?.bunit ?? null);
}

const sig = (v: number, digits = 9) => v.toLocaleString("en-US", { maximumSignificantDigits: digits, useGrouping: false });
const CONVENTION_LABEL: Record<string, string> = { radio: "radio", optical: "optical", relativistic: "relativistic" };

/** Show the readout in another unit or frame. Only what the file allows is enabled; the reason for the rest is written out. */
function ConversionControls({ v }: { v: Viewer }) {
  const { offers, conv, setConv } = v;
  if (!offers) return null;
  const spec = offers.spectral;
  const specTarget = spec?.targets.find((t) => t.unit === conv.spectral);
  const needsConvention = !!specTarget?.needs_convention;
  const reasons = (list: { reason: string | null; enabled: boolean }[]) => [...new Set(list.filter((t) => !t.enabled && t.reason).map((t) => t.reason as string))];
  const specReasons = spec ? reasons(spec.targets) : [];
  const valueReasons = v.interp ? [] : reasons(offers.value.targets);
  const skyOff = offers.sky ? reasons(offers.sky.targets) : [];
  const anyValue = !v.interp && offers.value.targets.length > 0;
  if (!spec && !offers.sky && !anyValue && !offers.value.reason) return null;
  return (
    <div className={s.convert}>
      <p className={s.convertHead}>Also show as <span className={s.convertHint}>(adds a row for each choice; the native rows above stay as the file gives them)</span></p>
      {spec ? (
        <>
          <Select label="Spectral unit" value={conv.spectral} onChange={(e) => setConv({ ...conv, spectral: e.target.value })}>
            <option value="">Native ({spec.native_unit ?? "unresolved"})</option>
            {spec.targets.map((t) => (
              <option key={t.unit} value={t.unit} disabled={!t.enabled}>{t.unit}{t.enabled ? "" : " (unavailable)"}</option>
            ))}
          </Select>
          {needsConvention ? (
            <Select label="Velocity convention (your choice; the file declares none)" value={conv.convention} onChange={(e) => setConv({ ...conv, convention: e.target.value })}>
              {Object.entries(CONVENTION_LABEL).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
            </Select>
          ) : null}
          {specReasons.map((r) => <p key={r} className={s.note}>{r}.</p>)}
        </>
      ) : null}
      {offers.sky ? (
        <>
          <Select label="Sky frame" value={conv.frame} onChange={(e) => setConv({ ...conv, frame: e.target.value })}>
            <option value="">Native ({offers.sky.source ?? offers.sky.frame ?? "unresolved"})</option>
            {offers.sky.targets.map((t) => <option key={t.id} value={t.id} disabled={!t.enabled}>{t.label}{t.enabled ? "" : " (unavailable)"}</option>)}
          </Select>
          {skyOff.map((r) => <p key={r} className={s.note}>{r}.</p>)}
        </>
      ) : null}
      {anyValue ? (
        <>
          <Select label="Value unit" value={conv.to} onChange={(e) => setConv({ ...conv, to: e.target.value })}>
            <option value="">Native ({unitLabel(offers.value.unit, offers.value.unit_status)})</option>
            {offers.value.targets.map((t) => <option key={t.unit} value={t.unit} disabled={!t.enabled}>{t.unit}{t.enabled ? "" : " (unavailable)"}</option>)}
          </Select>
          {valueReasons.map((r) => <p key={r} className={s.note}>{r}.</p>)}
        </>
      ) : !v.interp && offers.value.reason ? <p className={s.note}>Value unit: {offers.value.reason}.</p> : null}
      {spec ? <p className={s.note}>{spec.reference_frame_note}</p> : null}
    </div>
  );
}

/** The converted rows, each with the basis that made the conversion valid, or the reason it was refused. */
function ConvertedRows({ v }: { v: Viewer }) {
  const r = v.convertedNow;
  const { conv } = v;
  if (v.convError) return <p className={s.note} role="status">{v.convError}</p>;
  if (!r) return null;
  const sp = r.spectral, sk = r.sky, va = r.value;
  const basis: string[] = [];
  if (sp?.basis) basis.push(sp.basis);
  if (sk?.basis) basis.push(sk.basis);
  if (va?.basis) basis.push(va.basis);
  if (sk?.frame_note) basis.push(sk.frame_note);
  return (
    <>
      {sp ? (
        <>
          <dt>{`As ${conv.spectral}`}</dt>
          <dd className="mono">
            {sp.converted ? `${sig(sp.converted.value)} ${sp.converted.unit}` : EM}
            {sp.converted && sp.convention ? ` (${sp.convention} convention, ${sp.convention_source === "user" ? "chosen by you" : "the file's"})` : ""}
            {sp.refused ? <span className={s.refused}>not converted: {sp.refused}</span> : null}
          </dd>
        </>
      ) : null}
      {sk ? (
        <>
          <dt>{sk.converted ? `${sk.converted.lon_name} (${sk.converted.frame})` : "Sky frame"}</dt>
          <dd className="mono">{sk.converted ? degrees(sk.converted.lon) : EM}{sk.refused ? <span className={s.refused}>not converted: {sk.refused}</span> : null}</dd>
          {sk.converted ? (
            <>
              <dt>{`${sk.converted.lat_name} (${sk.converted.frame})`}</dt>
              <dd className="mono">{degrees(sk.converted.lat)}</dd>
            </>
          ) : null}
        </>
      ) : null}
      {va ? (
        <>
          <dt>{`Value as ${conv.to}`}</dt>
          <dd className="mono">
            {va.converted ? `${valueText(va.converted.value, va.converted.unit)}${va.converted.unit_status === "user" ? " (declared by you)" : ""}` : EM}
            {va.refused ? <span className={s.refused}>not converted: {va.refused}</span> : null}
          </dd>
        </>
      ) : null}
      {basis.length ? (
        <>
          <dt>Basis</dt>
          <dd>{basis.map((b) => <span key={b} className={s.basis}>{b}</span>)}</dd>
        </>
      ) : null}
    </>
  );
}

/** Pixel, world and spectral readout for the pointer (or pinned) pixel. World values come from the worker's WCS. */
/** What the Frame row says about how its frame was obtained, short for the row and in full for the Basis; null when the header declares it. */
function frameCaveat(status: string | undefined, assumed: boolean): { short: string; long: string } | null {
  if (status === "unresolved") {
    return assumed
      ? { short: "assumed", long: "The header declares no celestial frame, so the FITS standard default applies. The frame is assumed, not read from the file." }
      : { short: "unresolved", long: "The header declares no celestial frame and none was assumed: sky positions have no stated frame." };
  }
  if (status === "inferred") return { short: "inferred from EQUINOX", long: "The header names no frame; it was inferred from EQUINOX." };
  return null;
}

export function CoordinateReadout({ v, other }: { v: Viewer; other?: { name: string; v: Viewer; at?: OtherAt } | null }) {
  const { view, probe, plane } = v;
  // a readout is shown only for the pixel and channel it was computed for; otherwise it is still loading
  const coord = v.coord && probe && plane && v.coord.x === probe.x && v.coord.y === probe.y && v.coord.channel === plane.head.channel ? v.coord : null;
  if (!view) return <p className={s.note}>{EM}</p>;
  const sky = view.sky;
  const spec = view.spectral;
  const frameNote = sky.kind === "celestial" ? frameCaveat(sky.frame_status, v.offers?.sky?.frame_status === "assumed" && !!v.offers.sky.source) : null;
  return (
    <div className={s.wrap}>
      <dl className={s.facts} aria-live="off">
        <dt>Pixel</dt>
        <dd className="mono">{probe ? `x ${probe.x + 1}, y ${probe.y + 1}` : EM}</dd>
        <dt>Value</dt>
        <dd className="mono">{plane && probe ? (plane.head.preview ? "preview only, full plane loading" : v.interp ? axialText(v.pixelValue, v.interp.unit) : valueText(v.pixelValue, view.bunit)) : EM}</dd>
        {v.interp && v.interp.reliabilityId !== null ? (
          <>
            <dt>Reliability</dt>
            <dd className="mono">{probe && v.reliability ? valueText(v.reliability.data[probe.y * v.reliability.head.width + probe.x] ?? null, null) : EM}</dd>
          </>
        ) : null}
        {other && probe ? (
          <>
            <dt title={other.name}>Other map</dt>
            <dd className="mono">{otherValue(other.v, probe, other.at)}</dd>
          </>
        ) : null}
        {sky.kind === "celestial" ? (
          <>
            <dt>{sky.lon_name}</dt>
            <dd className="mono">{coord?.sky ? degrees(coord.sky.lon) : EM}</dd>
            <dt>{sky.lat_name}</dt>
            <dd className="mono">{coord?.sky ? degrees(coord.sky.lat) : EM}</dd>
            <dt>Frame</dt>
            <dd className="mono">
              {sky.frame ?? (sky.frame_status === "unresolved" && v.offers?.sky?.frame_status === "assumed" ? v.offers.sky.source : null) ?? EM}
              {frameNote ? ` (${frameNote.short})` : ""}
            </dd>
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
        <ConvertedRows v={v} />
      </dl>
      <ConversionControls v={v} />
      {view.bunit_status === "unresolved" || view.bunit_status === "user" || v.coordError ? (
        <p className={s.note}>
          {view.bunit_status === "unresolved" ? "Brightness unit unresolved: the header has no BUNIT. " : ""}
          {view.bunit_status === "user" ? `Brightness unit (${view.bunit}) declared by you: the header has no BUNIT. ` : ""}
          {v.coordError ?? ""}
        </p>
      ) : null}
      <Basis summary="Pixel numbers follow FITS (start at 1). Click the image to pin a pixel; Esc releases it." tone="note">
        {frameNote ? <p>{frameNote.long}</p> : null}
      </Basis>
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
  // What the histogram counts: the plane, the region drawn now, or a saved region. A choice that no longer exists means the plane.
  const [histOf, setHistOf] = useState("plane");
  const savedList = v.savedRegions.saved;
  const drawnIsSaved = v.region !== null && savedList.some((r) => sameRegion(toShape(r), v.region));
  const histScope = useMemo(() => {
    const p = v.plane;
    if (!p) return null;
    const mk = (name: string, shape: NonNullable<typeof v.region>) => ({ name, shape, width: p.head.width, height: p.head.height, preview: !!p.head.preview });
    if (histOf === "drawn" && v.region && !drawnIsSaved) return mk("drawn region", v.region);
    const saved = histOf.startsWith("s") ? savedList.find((r) => `s${r.id}` === histOf) : undefined;
    return saved ? mk(saved.name, toShape(saved)) : null;
  }, [histOf, v.region, drawnIsSaved, savedList, v.plane]);
  const histValue = histScope ? histOf : "plane";

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
      {v.plane && stats && !v.plane.head.preview ? (
        <>
          {v.region || savedList.length > 0 ? (
            <Select label="Histogram of" value={histValue} onChange={(e) => setHistOf(e.target.value)}>
              <option value="plane">Plane</option>
              {v.region && !drawnIsSaved ? <option value="drawn">Region: drawn region</option> : null}
              {savedList.map((r) => <option key={r.id} value={`s${r.id}`}>Region: {r.name}</option>)}
            </Select>
          ) : null}
          <Histogram data={v.plane.data} stats={stats} limits={lim} unit={unit} scope={histScope} onChange={(lo, hi) => { v.setCustomLimits([lo, hi]); v.setRange("custom"); }} />
        </>
      ) : null}
      <div className={s.legend}>
        <canvas ref={bar} className={s.bar} width={256} height={1} role="img" aria-label={`${v.colormap} colour bar from low to high`} />
        <div className={s.ends}>
          <span className="mono">{lim ? limitText(lim[0], unit) : EM}</span>
          <span className="mono">{lim ? limitText(lim[1], unit) : EM}</span>
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
