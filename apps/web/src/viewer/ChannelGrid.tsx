import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Input, Select } from "../ui";
import type { Dataset } from "../api/catalog";
import { createFigure } from "../api/boards";
import type { Figure } from "../api/boards";
import { getChannelGrid, getPanel } from "../api/channelmaps";
import type { ChannelGridPlan, GridPanel } from "../api/channelmaps";
import { ApiError } from "../api/http";
import type { Plane } from "../api/planes";
import { lut } from "./colormaps";
import type { ColormapId } from "./colormaps";
import { composeGridFigure, defaultColumns, gridCaption, gridFigure, loadedText, paintPixels, panelLabel, rampCss, blobToBase64 } from "./channelGrid";
import type { Stretch } from "./render";
import type { Viewer } from "./useViewer";
import { EM } from "../catalog/format";
import s from "./ChannelGrid.module.css";

const isAbort = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const NODATA: [[number, number, number], [number, number, number]] = [[27, 34, 44], [85, 98, 117]];
const GRID_COLORMAPS: { id: ColormapId; label: string }[] = [{ id: "viridis", label: "Viridis" }, { id: "inferno", label: "Inferno" }, { id: "gray", label: "Gray" }];
const STRETCH_LABEL: Record<Stretch, string> = { linear: "linear", sqrt: "square root", asinh: "inverse hyperbolic sine" };

type Loaded = { plane: Plane; slab: { excluded_samples: number; pixels_without_value: number; samples: number } | null };

/** Whether `ref` has come into view. Without IntersectionObserver (old browsers, tests) everything counts as in view. */
function useInView(ref: React.RefObject<Element | null>): boolean {
  const [seen, setSeen] = useState(typeof IntersectionObserver === "undefined");
  useEffect(() => {
    const el = ref.current;
    if (seen || !el) return;
    if (typeof IntersectionObserver === "undefined") { // checked again here: it can vanish after the first render (a webview, a test teardown)
      setSeen(true);
      return;
    }
    const io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && setSeen(true), { rootMargin: "200px" });
    io.observe(el);
    return () => io.disconnect();
  }, [ref, seen]);
  return seen;
}

/**
 * One panel. It reads its plane (or slab) through the same bounded plane path as the viewer, only once it has scrolled into
 * view, and paints it with the grid's shared scale. A panel that was read is kept by the grid, so scrolling back costs nothing.
 */
function Panel({ panel, plan, datasetId, shared, cached, onLoaded, colormap, stretch, label, request }: {
  panel: GridPanel; plan: ChannelGridPlan; datasetId: number; shared: { lo: number; hi: number } | null; cached: Loaded | undefined;
  onLoaded: (i: number, l: Loaded, canvas: HTMLCanvasElement | null) => void; colormap: ColormapId; stretch: Stretch; label: string; request: number;
}) {
  const box = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const near = useInView(box);
  const want = near || request > 0; // "Load all" asks every panel to read
  const [error, setError] = useState<string | null>(null);
  const [data, setData] = useState<Loaded | undefined>(cached);

  useEffect(() => {
    if (!want || data) return;
    const ac = new AbortController();
    getPanel(datasetId, panel, plan.step, ac.signal)
      .then((p) => {
        const l = { plane: { head: p.head, data: p.data }, slab: p.slab as Loaded["slab"] };
        setData(l);
        onLoaded(panel.index, l, null);
      })
      .catch((e: unknown) => !isAbort(e) && setError(e instanceof ApiError ? e.message : "This panel could not be read."));
    return () => ac.abort(); // a panel that scrolled away, or a plan that changed, stops its read
  }, [want, data, datasetId, panel, plan.step]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const c = canvas.current;
    if (!c || !data || !shared) return;
    const { plane } = data;
    c.width = plane.head.width;
    c.height = plane.head.height;
    c.getContext("2d")?.putImageData(new ImageData(paintPixels(plane.data, plane.head.width, plane.head.height, shared, lut(colormap), stretch, NODATA) as Uint8ClampedArray<ArrayBuffer>, plane.head.width, plane.head.height), 0, 0);
    onLoaded(panel.index, data, c);
  }, [data, shared?.lo, shared?.hi, colormap, stretch]); // eslint-disable-line react-hooks/exhaustive-deps

  const none = data && data.plane.head.stats.finite_count === 0;
  return (
    <figure className={s.panel} ref={box} style={{ aspectRatio: `${plan.panel_width} / ${plan.panel_height}` }}>
      <canvas ref={canvas} className={s.canvas} role="img" aria-label={`${label}, ${plan.panel_width} by ${plan.panel_height} pixels`} />
      <figcaption className={s.label}>{label}</figcaption>
      {!data && !error ? <span className={s.state}>{want ? "Loading…" : EM}</span> : null}
      {error ? <span className={s.state} role="alert">{error}</span> : null}
      {none ? <span className={s.state}>No value in any pixel</span> : null}
      {panel.flagged ? <span className={s.flag} title="The file flags a channel of this panel; its data are shown.">▲ flagged</span> : null}
    </figure>
  );
}

/** A grid of channel maps (ADR 0049): consecutive or every k-th channel over a range, one shared colour scale, labelled spectral coordinates. */
export function ChannelGrid({ v, dataset }: { v: Viewer; dataset: Dataset }) {
  const channels = v.view?.channels ?? 0;
  const [first, setFirst] = useState("1");
  const [last, setLast] = useState("");
  const [stride, setStride] = useState("1");
  const [average, setAverage] = useState("1");
  const [unit, setUnit] = useState("");
  const [convention, setConvention] = useState("radio");
  const [colormap, setColormap] = useState<ColormapId>("viridis");
  const [stretch, setStretch] = useState<Stretch>("linear");
  const [columns, setColumns] = useState<number | null>(null);
  const [custom, setCustom] = useState<{ lo: string; hi: string }>({ lo: "", hi: "" });
  const [limits, setLimits] = useState<[number, number] | null>(null);
  const [plan, setPlan] = useState<ChannelGridPlan | null>(null);
  const [planError, setPlanError] = useState<string | null>(null);
  const [planning, setPlanning] = useState(false);
  const [loadAll, setLoadAll] = useState(0);
  const loaded = useRef(new Map<number, Loaded>());
  const canvases = useRef(new Map<number, HTMLCanvasElement>());
  const [count, setCount] = useState(0);
  const [pixels, setPixels] = useState(0);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState<Figure | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);

  // the unit the person chose for readouts is the grid's too, when it is on offer
  const targets = v.offers?.spectral?.targets ?? [];
  const native = v.offers?.spectral?.native_unit ?? v.view?.spectral?.unit ?? null;
  useEffect(() => {
    setLast(channels > 0 ? String(Math.min(channels, 12)) : "");
    setFirst("1");
    setStride("1");
    setAverage("1");
    setUnit("");
    setLimits(null);
    setCustom({ lo: "", hi: "" });
  }, [dataset.id, channels]);
  useEffect(() => {
    if (v.conv.spectral && targets.some((t) => t.unit === v.conv.spectral && t.enabled)) {
      setUnit(v.conv.spectral);
      setConvention(v.conv.convention || "radio");
    }
  }, [dataset.id, v.offers]); // eslint-disable-line react-hooks/exhaustive-deps

  const nums = { first: Number(first), last: Number(last), stride: Number(stride), average: Number(average) };
  const bad = [nums.first, nums.last, nums.stride, nums.average].some((n) => !Number.isInteger(n)) || nums.first < 1 || nums.last < nums.first || nums.last > channels || nums.stride < 1 || nums.average < 1 || nums.average > nums.stride
    ? "Channel numbers are whole numbers from 1, the last not before the first, and the channels averaged per panel not more than the stride."
    : null;
  const target = targets.find((t) => t.unit === unit);
  const needsConvention = Boolean(unit && target?.needs_convention);

  // the plan: which channels each panel holds, their labels, and the scale they share. A new request stops the old one.
  useEffect(() => {
    loaded.current = new Map();
    canvases.current = new Map();
    setCount(0);
    setPixels(0);
    setSaved(null);
    setLoadAll(0);
    setPlan(null);
    if (bad || channels < 2) return;
    const ac = new AbortController();
    setPlanning(true);
    const t = setTimeout(() => {
      getChannelGrid(dataset.id, { first: nums.first - 1, last: nums.last - 1, stride: nums.stride, average: nums.average, unit, convention: needsConvention ? convention : "", limits }, ac.signal)
        .then((p) => { setPlan(p); setPlanError(null); })
        .catch((e: unknown) => { if (!isAbort(e)) { setPlanError(e instanceof ApiError ? e.message : "The channel maps could not be planned."); } })
        .finally(() => setPlanning(false));
    }, 200);
    return () => { clearTimeout(t); ac.abort(); setPlanning(false); };
  }, [dataset.id, nums.first, nums.last, nums.stride, nums.average, unit, convention, needsConvention, limits, channels, bad]); // eslint-disable-line react-hooks/exhaustive-deps

  const lo = plan?.scale.lo ?? null, hi = plan?.scale.hi ?? null;
  const shared = lo !== null && hi !== null ? { lo, hi } : null;
  const cols = columns ?? (plan ? defaultColumns(plan.panel_count) : 1);
  const bunit = v.view?.bunit ?? null;
  const labels = useMemo(() => (plan ? plan.panels.map((p) => panelLabel(p, plan)) : []), [plan]);

  const onLoaded = useCallback((i: number, l: Loaded, c: HTMLCanvasElement | null) => {
    if (c) canvases.current.set(i, c);
    if (!loaded.current.has(i)) {
      loaded.current.set(i, l);
      setCount(loaded.current.size);
      setPixels((n) => n + l.plane.data.length);
    }
  }, []);

  function applyLimits() {
    const a = Number(custom.lo), b = Number(custom.hi);
    if (custom.lo.trim() === "" || custom.hi.trim() === "" || !Number.isFinite(a) || !Number.isFinite(b) || !(a < b)) {
      setPlanError("Scale limits are two numbers, the lower below the upper.");
      return;
    }
    setLimits([a, b]);
  }

  async function save() {
    if (!plan || !shared) return;
    setSaving(true);
    setSaveError(null);
    try {
      const built = gridFigure({ datasetId: dataset.id, plan, first: nums.first - 1, last: nums.last - 1, stride: nums.stride, average: nums.average, columns: cols, unit, convention, colormap, stretch, bunit });
      if (!built) throw new Error("There is no shared scale to save: the sampled panels hold no finite pixel.");
      if (loaded.current.size < plan.panel_count) throw new Error("Load every panel before saving, so the figure shows what its description states.");
      const aspect = plan.panel_width / plan.panel_height;
      const blob = await composeGridFigure({
        title: `${dataset.name}, channel maps ${nums.first}–${nums.last}`,
        panels: plan.panels.map((p) => ({ label: labels[p.index]!, source: canvases.current.get(p.index) ?? null })),
        columns: cols, aspect, lut: lut(colormap), scale: shared, unit: bunit,
        captions: [dataset.name, ...gridCaption(plan, { stretch: STRETCH_LABEL[stretch], bunit, overlaysOnBase: v.scene?.overlays.filter((o) => o.visible && o.id !== "regions" && o.id !== "orientation").length ?? 0 }), "Header values are declared by the file, not verified."],
      });
      setSaved(await createFigure({ datasetId: dataset.id, title: `${dataset.name}, channel maps ${nums.first}–${nums.last}`, caption: "", view: built.view, previewPng: await blobToBase64(blob), channelGrid: built.spec }));
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : "Could not save the figure.");
    } finally {
      setSaving(false);
    }
  }

  if (v.interp) return <div className={s.wrap}><p className={s.note}>Channel maps average and compare values across channels, which is not defined for the angles of an axial orientation map.</p></div>;
  if (!v.view) return <div className={s.wrap}><p className={s.note}>Reading the cube layout…</p></div>;
  if (channels < 2) return <div className={s.wrap}><p className={s.note}>This file has no channel axis of more than one channel, so there are no channel maps to show.</p></div>;

  return (
    <div className={s.wrap} aria-label="Channel maps">
      <form className={s.controls} onSubmit={(e) => e.preventDefault()} aria-label="Channel map settings">
        <Input label="First channel" inputMode="numeric" value={first} onChange={(e) => setFirst(e.target.value)} />
        <Input label="Last channel" inputMode="numeric" value={last} onChange={(e) => setLast(e.target.value)} />
        <Input label="Every k-th channel" inputMode="numeric" value={stride} onChange={(e) => setStride(e.target.value)} />
        <Input label="Channels averaged per panel" inputMode="numeric" value={average} onChange={(e) => setAverage(e.target.value)} />
        <Select label="Spectral labels" value={unit} onChange={(e) => setUnit(e.target.value)}>
          <option value="">{native ? `The file's unit (${native})` : "Channel numbers"}</option>
          {targets.map((t) => <option key={t.unit} value={t.unit} disabled={!t.enabled} title={t.reason ?? undefined}>{t.unit}{t.enabled ? "" : ` (${t.reason ?? "not available"})`}</option>)}
        </Select>
        {needsConvention ? (
          <Select label="Velocity convention" value={convention} onChange={(e) => setConvention(e.target.value)}>
            <option value="radio">Radio</option>
            <option value="optical">Optical</option>
            <option value="relativistic">Relativistic</option>
          </Select>
        ) : null}
        <Select label="Colour map" value={colormap} onChange={(e) => setColormap(e.target.value as ColormapId)}>
          {GRID_COLORMAPS.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
        </Select>
        <Select label="Stretch" value={stretch} onChange={(e) => setStretch(e.target.value as Stretch)}>
          {(Object.keys(STRETCH_LABEL) as Stretch[]).map((k) => <option key={k} value={k}>{STRETCH_LABEL[k]}</option>)}
        </Select>
        <Select label="Panels per row" value={String(cols)} onChange={(e) => setColumns(Number(e.target.value))}>
          {[1, 2, 3, 4, 5, 6, 8].map((n) => <option key={n} value={n}>{n}</option>)}
        </Select>
        <Input label={`Scale from${bunit ? ` (${bunit})` : ""}`} inputMode="decimal" value={custom.lo} placeholder={lo !== null ? String(Number(lo.toPrecision(4))) : ""} onChange={(e) => setCustom({ ...custom, lo: e.target.value })} />
        <Input label="Scale to" inputMode="decimal" value={custom.hi} placeholder={hi !== null ? String(Number(hi.toPrecision(4))) : ""} onChange={(e) => setCustom({ ...custom, hi: e.target.value })} />
        <div className={s.actions}>
          <Button type="button" onClick={applyLimits}>Set the scale</Button>
          <Button type="button" onClick={() => { setLimits(null); setCustom({ lo: "", hi: "" }); }} disabled={limits === null}>Sample the scale</Button>
        </div>
      </form>
      {bad ? <p role="alert" className={s.err}>{bad}</p> : null}
      {planError ? <p role="alert" className={s.err}>{planError}</p> : null}
      {planning && !plan ? <p className={s.note}>Planning the panels…</p> : null}
      {plan ? (
        <>
          <p className={s.status} role="status">
            {loadedText(count, plan.panel_count, pixels)} · cap {Math.round((plan.caps.pixels_total ?? 0) * 4 / 1048576)} MiB · panels read when they scroll into view
            {count < plan.panel_count ? <> · <button type="button" className={s.link} onClick={() => setLoadAll((n) => n + 1)}>Load all</button></> : null}
          </p>
          <div className={s.grid} style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }} data-testid="channel-grid">
            {plan.panels.map((p) => (
              <Panel key={`${p.index}:${plan.step}:${first}:${last}:${stride}:${average}`} panel={p} plan={plan} datasetId={dataset.id} shared={shared} cached={loaded.current.get(p.index)} onLoaded={onLoaded} colormap={colormap} stretch={stretch} label={labels[p.index]!} request={loadAll} />
            ))}
          </div>
          {shared ? (
            <div className={s.bar} aria-label="Colour scale shared by every panel">
              <span className={s.ramp} style={{ background: rampCss(colormap) }} />
              <span className={s.ends}><span>{Number(shared.lo.toPrecision(5))}{bunit ? ` ${bunit}` : ""}</span><span>{Number(shared.hi.toPrecision(5))}{bunit ? ` ${bunit}` : ""}</span></span>
            </div>
          ) : null}
          <ul className={s.caption}>
            {gridCaption(plan, { stretch: STRETCH_LABEL[stretch], bunit, overlaysOnBase: v.scene?.overlays.filter((o) => o.visible && o.id !== "regions" && o.id !== "orientation").length ?? 0 }).map((l, i) => <li key={i}>{l}</li>)}
          </ul>
          <div className={s.actions}>
            <Button variant="primary" onClick={save} disabled={saving || !shared}>{saving ? "Saving…" : "Save as figure"}</Button>
          </div>
          {saveError ? <p role="alert" className={s.err}>{saveError}</p> : null}
          {saved ? <p role="status" className={s.note}>Saved: “{saved.title}”. Add it to a board from the Boards view.</p> : null}
        </>
      ) : null}
    </div>
  );
}
