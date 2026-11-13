import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { Crosshair, Download, Minus, Plus, Scan } from "lucide-react";
import type { Dataset } from "../api/catalog";
import type { SkyFrame } from "../api/sky";
import { Button, IconButton, Select } from "../ui";
import { clampFov, fitView, pan, zoom } from "./projection";
import type { View } from "./projection";
import { buildScene, frameName, pathData, positionText, SIZE } from "./skyScene";
import { saveSkyImage, captionOf } from "./exportSky";
import { useSkySettings } from "./settings";
import { contactNotice, SURVEYS, surveyById } from "./surveys";
import { useFootprints, useSkyCursor } from "./useSky";
import type { SkyItem } from "./useSky";
import type { BackgroundApi, BackgroundStatus } from "./AladinView";
import s from "./SkyPanel.module.css";

// Loaded only once the background is on: it brings Aladin Lite (see aladinLoader.ts).
const AladinView = lazy(() => import("./AladinView").then((m) => ({ default: m.AladinView })));

const EM = "—";
const fovText = (f: number) => (f >= 1 ? `${f.toFixed(1)}°` : f >= 1 / 60 ? `${(f * 60).toFixed(1)}′` : `${(f * 3600).toFixed(1)}″`);

function itemNote(it: SkyItem): string {
  if (it.state === "loading") return "reading…";
  if (it.state === "ready") return it.fromStatus && it.fromStatus !== "declared" ? `frame ${it.fromStatus}: ${it.fromFrame ?? ""}` : (it.fromFrame ?? "");
  return it.reason ?? "no footprint";
}

/**
 * Sky context (ADR 0072): where the registered datasets lie on the sky, with the open dataset's cursor, on a plain coordinate grid
 * and, if the person turns it on, a public survey image behind them. It never draws from a pixel and never converts a coordinate:
 * every outline and the cursor position are world coordinates the worker computed from the dataset's celestial WCS in the panel's
 * frame. The survey image is context only: it is labelled as such, is not sampled into any number and is off by default.
 */
export function SkyPanel({ datasets, datasetId, probe, onOpen }: {
  datasets: Dataset[];
  /** The dataset open in the viewer, or null (a virtual subset has no sky route of its own, so it is null too). */
  datasetId: number | null;
  /** The viewer's pixel (hover, or pinned by a click), 0-based, in the open dataset's own grid. */
  probe: { x: number; y: number } | null;
  onOpen: (id: number) => void;
}) {
  const { settings, set } = useSkySettings();
  const frame = settings.frame;
  const survey = surveyById(settings.survey);
  const { items, omitted } = useFootprints(datasets, frame);
  const bg = settings.background;
  const icrs = useFootprints(datasets, "icrs", bg && frame !== "icrs");
  const [asking, setAsking] = useState(false);
  const [status, setStatus] = useState<BackgroundStatus>({ state: "connecting" });
  const [api, setApi] = useState<BackgroundApi | null>(null);
  const [withBackground, setWithBackground] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const selected = items.find((i) => i.id === datasetId) ?? null;
  const cursorId = selected?.state === "ready" ? selected.id : null;
  const cursor = useSkyCursor(cursorId, probe, frame);
  const cursorIcrs = useSkyCursor(bg && frame !== "icrs" ? cursorId : null, probe, "icrs");

  // the view: fitted to the open dataset (or to everything when it has no outline), until the person moves it
  const [custom, setCustom] = useState<{ key: string; v: View } | null>(null);
  const [fitAll, setFitAll] = useState(false);
  const [nonce, setNonce] = useState(0);
  const fitFor = (list: SkyItem[]): View | null => {
    const one = !fitAll ? list.find((i) => i.id === datasetId)?.outline : null;
    return fitView(one ? [one.polygon] : list.flatMap((i) => (i.outline ? [i.outline.polygon] : [])));
  };
  const fitted = useMemo(() => fitFor(items), [items, datasetId, fitAll]); // eslint-disable-line react-hooks/exhaustive-deps
  const viewKey = `${frame}|${datasetId}|${fitAll}|${nonce}`;
  const view = (custom && custom.key === viewKey ? custom.v : null) ?? fitted;
  const move = (f: (v: View) => View) => view && setCustom({ key: viewKey, v: f(view) });
  const aladinItems = frame === "icrs" ? items : icrs.items;
  const aladinFit = { view: fitFor(aladinItems), key: `${viewKey}|${aladinItems.find((i) => i.id === datasetId)?.state ?? ""}` };

  const scene = useMemo(
    () => (view ? buildScene(items, view, frame, datasetId, cursor.state === "ready" ? { lon: cursor.lon!, lat: cursor.lat! } : null) : null),
    [items, view, frame, datasetId, cursor],
  );
  const shown = bg && status.state === "ready";
  const withOutline = items.filter((i) => i.state === "ready").length;

  // a background that was on but is no longer wanted forgets its state and its picture
  useEffect(() => {
    if (!bg) {
      setStatus({ state: "connecting" });
      setApi(null);
      setWithBackground(false);
    }
  }, [bg]);

  const chart = useRef<HTMLDivElement>(null);
  const onKey = (e: React.KeyboardEvent) => {
    const k = e.key;
    if (k === "ArrowLeft") move((v) => pan(v, -0.2, 0));
    else if (k === "ArrowRight") move((v) => pan(v, 0.2, 0));
    else if (k === "ArrowUp") move((v) => pan(v, 0, 0.2));
    else if (k === "ArrowDown") move((v) => pan(v, 0, -0.2));
    else if (k === "+" || k === "=") move((v) => zoom(v, 1 / 1.5));
    else if (k === "-") move((v) => zoom(v, 1.5));
    else return;
    e.preventDefault();
  };

  async function save() {
    if (!scene || !view) return;
    setSaveError(null);
    try {
      const include = withBackground && shown;
      const bgUrl = include ? ((await api?.capture()) ?? null) : null;
      if (include && !bgUrl) throw new Error("The survey view could not be captured, so nothing was saved.");
      const centre = positionText(frame, view.lon, view.lat);
      await saveSkyImage({ scene, lines: captionOf({ frame: frameName(frame), centre: `${centre.names[0]} ${centre.deg[0]}, ${centre.names[1]} ${centre.deg[1]}`, fov: fovText(view.fov), selected: selected?.name ?? null, survey: include ? survey : null }) }, bgUrl);
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : "The image could not be saved.");
    }
  }

  const where = cursor.state === "ready" ? positionText(frame, cursor.lon!, cursor.lat!) : null;
  return (
    <div className={s.wrap} data-testid="sky-panel">
      <p className={s.lede}>Where your datasets lie on the sky, from each dataset&apos;s celestial WCS. Context only: no measurement uses it.</p>
      <div className={s.bar}>
        <Select label="Frame" value={frame} onChange={(e) => set({ frame: e.target.value as SkyFrame })}>
          <option value="icrs">ICRS (RA, Dec)</option>
          <option value="galactic">Galactic (l, b)</option>
        </Select>
        <div className={s.tools} role="group" aria-label="View">
          {!shown ? <IconButton icon={Minus} label="Zoom out" onClick={() => move((v) => zoom(v, 1.5))} disabled={!view} /> : null}
          {!shown ? <IconButton icon={Plus} label="Zoom in" onClick={() => move((v) => zoom(v, 1 / 1.5))} disabled={!view} /> : null}
          <IconButton icon={Crosshair} label="Fit the open dataset" onClick={() => { setFitAll(false); setCustom(null); setNonce((n) => n + 1); }} disabled={!selected?.outline} />
          <IconButton icon={Scan} label="Fit all datasets" onClick={() => { setFitAll(true); setCustom(null); setNonce((n) => n + 1); }} disabled={withOutline === 0} />
        </div>
      </div>

      <div className={s.stage}>
        <div className={s.chart} ref={chart}>
          {scene && view ? (
            <svg viewBox={`0 0 ${SIZE} ${SIZE}`} className={shown ? s.svgHidden : s.svg} role="group" aria-label={`Sky map in ${frameName(frame)}, ${fovText(clampFov(view.fov))} across. Arrow keys pan, plus and minus zoom.`} tabIndex={shown ? -1 : 0} onKeyDown={onKey}>
              <defs><clipPath id="sky-clip"><rect width={SIZE} height={SIZE} /></clipPath></defs>
              <g clipPath="url(#sky-clip)">
                {scene.grid.runs.map((r, i) => <path key={i} d={pathData(r, false)} className={s.grid} />)}
                {scene.footprints.map((f) => (
                  <path key={f.id} d={pathData(f.path, true)} className={f.selected ? s.footSelected : s.foot} role="button" tabIndex={0} data-dataset-id={f.id} aria-label={`Open ${f.name}`} aria-pressed={f.selected}
                    onClick={() => onOpen(f.id)} onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(f.id); } }}>
                    <title>{f.name}</title>
                  </path>
                ))}
                {scene.cursor ? (
                  <g className={s.cursor} aria-hidden="true">
                    <circle cx={scene.cursor[0]} cy={scene.cursor[1]} r={5} />
                    <path d={`M${scene.cursor[0] - 9} ${scene.cursor[1]}H${scene.cursor[0] + 9}M${scene.cursor[0]} ${scene.cursor[1] - 9}V${scene.cursor[1] + 9}`} />
                  </g>
                ) : null}
              </g>
              {scene.grid.labels.map((l, i) => <text key={i} x={l.at[0] + 3} y={l.at[1] - 3} className={s.tick} aria-hidden="true">{l.text}</text>)}
              <text x={6} y={14} className={s.frameText} aria-hidden="true">{scene.frameLabel}</text>
              <text x={SIZE - 6} y={14} textAnchor="end" className={s.frameText} aria-hidden="true">{fovText(view.fov)}</text>
            </svg>
          ) : (
            <p className={s.empty}>{items.some((i) => i.state === "loading") ? "Reading footprints…" : "No dataset has a footprint to draw."}</p>
          )}
          {bg ? (
            <Suspense fallback={null}>
              <div className={shown ? s.bgOn : s.bgOff}>
                <AladinView survey={survey} frame={frame} fit={aladinFit} items={aladinItems} selectedId={datasetId} cursor={frame === "icrs" ? (cursor.state === "ready" ? { lon: cursor.lon!, lat: cursor.lat! } : null) : (cursorIcrs.state === "ready" ? { lon: cursorIcrs.lon!, lat: cursorIcrs.lat! } : null)} onPick={onOpen} onStatus={setStatus} onApi={setApi} />
              </div>
            </Suspense>
          ) : null}
        </div>
        {shown ? (
          <p className={s.context} role="note">
            Background: {survey.name} ({survey.id}), {survey.provider}. Context only: it is not data from your files and is never used in a measurement.
          </p>
        ) : (
          <p className={s.context}>Plain coordinate grid. No survey image{bg ? "" : ": the background is off"}.</p>
        )}
        {omitted > 0 ? <p className={s.note}>{omitted} more registered {omitted === 1 ? "dataset is" : "datasets are"} not shown here.</p> : null}
      </div>

      <dl className={s.facts} aria-live="polite">
        <dt>Cursor</dt>
        <dd className="mono">
          {where ? (
            <>
              {where.names[0]} {where.deg[0]}, {where.names[1]} {where.deg[1]}
              {where.sexagesimal ? <span className={s.sexa}>{where.sexagesimal[0]}, {where.sexagesimal[1]}</span> : null}
            </>
          ) : cursor.state === "loading" ? "reading…" : EM}
        </dd>
        <dt>From</dt>
        <dd>{selected ? selected.name : EM}</dd>
      </dl>
      {selected && selected.state !== "ready" && selected.state !== "loading" ? <p className={s.note}>{selected.name}: {selected.reason}. The cursor has no sky position here.</p> : null}
      {probe === null && selected?.state === "ready" ? <p className={s.note}>Move over the map in the viewer, or click to pin a pixel; its position appears here as a world coordinate.</p> : null}
      {selected?.state === "ready" && selected.fromStatus && selected.fromStatus !== "declared" ? (
        <p className={s.note}>
          {selected.name} is in {selected.fromFrame}; how that is known: {selected.fromStatus === "corrected" ? "your correction" : selected.fromStatus === "assumed" ? "assumed, the FITS default, because the header states no RADESYS or EQUINOX" : "taken from EQUINOX"}.
        </p>
      ) : null}

      <ul className={s.list} aria-label="Datasets on the sky">
        {items.map((it) => (
          <li key={it.id} className={s.row}>
            <button type="button" className={s.pick} aria-pressed={it.id === datasetId} onClick={() => onOpen(it.id)}>{it.name}</button>
            <span className={s.state}>{itemNote(it)}</span>
          </li>
        ))}
        {items.length === 0 ? <li className={s.note}>No dataset is registered yet.</li> : null}
      </ul>

      <section className={s.bgBox} aria-label="Survey background">
        <h3 className={s.bgHead}>Survey background</h3>
        {!bg && !asking ? (
          <>
            <p className={s.note}>Off. Nothing leaves this computer, and the panel works fully offline. The footprints are drawn on a plain coordinate grid.</p>
            <Button onClick={() => setAsking(true)}>Choose a survey image…</Button>
          </>
        ) : null}
        {!bg && asking ? (
          <div className={s.ask} role="group" aria-label="Contact a server">
            <Select label="Survey" value={settings.survey} onChange={(e) => set({ survey: e.target.value })}>
              {SURVEYS.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
            </Select>
            <p className={s.notice}>{contactNotice(survey)}</p>
            <div className={s.actions}>
              <Button variant="primary" onClick={() => { set({ background: true }); setAsking(false); }}>Contact {survey.host} and turn on</Button>
              <Button onClick={() => setAsking(false)}>Cancel</Button>
            </div>
          </div>
        ) : null}
        {bg ? (
          <>
            <Select label="Survey" value={settings.survey} onChange={(e) => set({ survey: e.target.value })}>
              {SURVEYS.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
            </Select>
            <p className={s.status} role="status">
              {status.state === "connecting" ? `Contacting ${survey.host}…` : status.state === "ready" ? `Showing ${survey.name} from ${survey.host}.` : `Not shown: ${status.reason}. The footprints stay on the plain grid.`}
            </p>
            <p className={s.notice}>{contactNotice(survey)}</p>
            <Button onClick={() => set({ background: false })}>Turn off</Button>
          </>
        ) : null}
      </section>

      <div className={s.save}>
        {shown ? (
          <label className={s.check}>
            <input type="checkbox" checked={withBackground} onChange={(e) => setWithBackground(e.target.checked)} />
            <span>Include the survey image in the saved file</span>
          </label>
        ) : null}
        <Button icon={Download} onClick={() => void save()} disabled={!scene}>Save sky image (PNG)</Button>
        {saveError ? <p className={s.error} role="alert">{saveError}</p> : null}
      </div>
    </div>
  );
}
