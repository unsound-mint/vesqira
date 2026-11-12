import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { ChevronDown, ChevronUp, Download } from "lucide-react";
import type { Run } from "../api/runs";
import type { Job } from "../api/jobs";
import { KIND } from "../api/metrics";
import { addToBoard, listBoards } from "../api/boards";
import type { Board, Figure } from "../api/boards";
import { convertRunValues } from "../api/runConvert";
import { deleteRunView, listRunViews, saveRunView } from "../api/runViews";
import type { RunView } from "../api/runViews";
import { Button, Checkbox, FilterBox, Input, ScrollTable, Select } from "../ui";
import { runVocabulary } from "../catalog/vocabulary";
import { evaluateRuns, queriedKeys } from "../catalog/runQuery";
import { cellOf, columnChoices, contextIndex, FIXED_COLUMNS, NOT_RECORDED, sortRuns } from "../catalog/runTable";
import type { ColumnId } from "../catalog/runTable";
import { buildPlot, conversionFrom, metricKeys, paramKeys, withRefusals } from "../catalog/runPlot";
import type { ConvertOffer } from "../catalog/runPlot";
import { download, pngWithText, resolveVars, runsCsv, svgWithProvenance } from "../catalog/runExport";
import type { PlotProvenance } from "../catalog/runExport";
import { drawnRunIds, saveRunFigure } from "./crossRunFigure";
import { RunsPlot } from "./RunsPlot";
import { useConvertibility } from "./useConvertibility";
import { effectiveColumns, specOf, stateFrom } from "./runsViewState";
import type { RunsViewState } from "./runsViewState";
import s from "./RunsView.module.css";

const STEP = 300;
const num = (x: number) => String(Number(x.toPrecision(6)));

/** The drawing's own size in user units, from its viewBox attribute. */
function viewBoxOf(el: SVGSVGElement): { width: number; height: number } {
  const [, , width = 720, height = 400] = (el.getAttribute("viewBox") ?? "").split(/\s+/).map(Number);
  return { width, height };
}

/** The label of a column: a fixed one, a parameter, or a result with its unit when every run records it in the same one. */
function columnLabel(id: ColumnId, runs: Run[]): string {
  if (id.startsWith("p:")) return id.slice(2);
  if (id.startsWith("m:")) {
    const key = id.slice(2).toLowerCase();
    const units = new Set<string>();
    for (const r of runs) for (const m of r.metrics) if (m.key.toLowerCase() === key && m.value !== null) units.add(m.unit);
    const u = [...units];
    return `${id.slice(2)}${u.length === 1 && u[0] ? ` (${u[0]})` : u.length > 1 ? " (units differ)" : ""}`;
  }
  return FIXED_COLUMNS.find((c) => c.id === id)?.label ?? id;
}

/**
 * Every run as a table (any parameter or result as a column, sortable, newest first by default) and one result drawn against
 * a parameter or another result. One query drives both, the filter box the sidebar list also reads. A run that did not
 * record something shows "not recorded" and sorts last; nothing is ranked or called best.
 */
export function RunsView({ runs, jobs, hostName, state, onState, onOpenRun, onBack }: {
  runs: Run[];
  jobs: Job[];
  hostName: (id: number) => string | undefined;
  state: RunsViewState;
  onState: (patch: Partial<RunsViewState>) => void;
  onOpenRun: (id: number) => void;
  onBack?: () => void;
}) {
  const ctxOf = useMemo(() => contextIndex(runs, jobs, hostName), [runs, jobs, hostName]);
  const vocabulary = useMemo(() => runVocabulary(runs, ctxOf), [runs, ctxOf]);
  const ev = useMemo(() => evaluateRuns(runs, state.query, ctxOf), [runs, state.query, ctxOf]);
  const active = useMemo(() => runs.filter((r) => !r.archived).length, [runs]);
  const columns = useMemo(() => effectiveColumns(state.columns, state.query, runs), [state.columns, state.query, runs]);
  const choices = useMemo(() => columnChoices(runs), [runs]);

  const hidden = ev.notRecorded.length;
  const rows = useMemo(() => {
    const first = sortRuns(ev.matched, state.sort.column, state.sort.dir, ctxOf);
    if (!state.showMissing) return first;
    return [...first, ...sortRuns(ev.notRecorded.map((x) => x.run), state.sort.column, state.sort.dir, ctxOf)];
  }, [ev, state.sort, state.showMissing, ctxOf]);
  const missingWords = useMemo(() => new Map(ev.notRecorded.map((x) => [x.run.id, x.words])), [ev]);

  const [limit, setLimit] = useState(STEP);
  const shownRows = rows.slice(0, limit);
  useEffect(() => setLimit(STEP), [state.query, state.sort]);

  // ---- selection: a point selects its row, a row its point
  const rowRefs = useRef(new Map<number, HTMLTableRowElement>());
  const select = (id: number | null) => {
    if (id !== null) {
      const at = rows.findIndex((r) => r.id === id);
      if (at >= limit) setLimit(at + 1 + STEP);
    }
    onState({ selected: id });
  };
  useEffect(() => {
    if (state.selected !== null) rowRefs.current.get(state.selected)?.scrollIntoView?.({ block: "nearest" });
  }, [state.selected, limit]);
  const selectedRun = state.selected === null ? undefined : runs.find((r) => r.id === state.selected);

  // ---- saved views
  const [views, setViews] = useState<RunView[]>([]);
  const [viewErr, setViewErr] = useState<string | null>(null);
  const [applied, setApplied] = useState("");
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");
  useEffect(() => {
    let live = true;
    listRunViews().then((r) => live && setViews(r.views)).catch(() => undefined);
    return () => { live = false; };
  }, []);
  async function saveView(e: FormEvent) {
    e.preventDefault();
    try {
      const v = await saveRunView(name, specOf(state));
      setViews((vs) => [...vs.filter((x) => x.id !== v.id), v].sort((a, b) => a.name.localeCompare(b.name)));
      setApplied(String(v.id));
      setNaming(false);
      setName("");
      setViewErr(null);
    } catch (err) {
      setViewErr(err instanceof Error ? err.message : "Could not save the view.");
    }
  }
  async function removeView() {
    const v = views.find((x) => String(x.id) === applied);
    if (!v) return;
    try {
      await deleteRunView(v.id);
      setViews((vs) => vs.filter((x) => x.id !== v.id));
      setApplied("");
      setViewErr(null);
    } catch (err) {
      setViewErr(err instanceof Error ? err.message : "Could not delete the view.");
    }
  }

  // ---- columns
  const [colsOpen, setColsOpen] = useState(false);
  const queried = new Set(queriedKeys(state.query));
  const toggleColumn = (id: ColumnId) => onState({ columns: state.columns.includes(id) ? state.columns.filter((c) => c !== id) : [...state.columns, id] });

  // ---- plot
  const plot = state.plot;
  const xKey = plot.x;
  const drawnModel = useMemo(() => (plot.y !== "" && xKey !== "" ? buildPlot(rows, plot.y, { kind: plot.xKind, key: xKey }, plot.by, state.conversion) : null), [rows, plot.y, plot.xKind, xKey, plot.by, state.conversion]);
  // whether each offered conversion will work is asked of the worker in the background; the plot is drawn without waiting for it
  const convertible = useConvertibility(drawnModel?.offers ?? []);
  const refusals = Object.fromEntries((drawnModel?.offers ?? []).flatMap((o) => { const c = convertible(o); return c.state === "no" ? [[o.unit, c.reason] as const] : []; }));
  const model = useMemo(() => (drawnModel ? withRefusals(drawnModel, refusals) : null), [drawnModel, JSON.stringify(refusals)]); // eslint-disable-line react-hooks/exhaustive-deps
  const mKeys = useMemo(() => metricKeys(runs), [runs]);
  const pKeys = useMemo(() => paramKeys(runs), [runs]);
  const drawn = model ? new Set(model.panels.flatMap((p) => p.series.flatMap((x) => x.points.map((q) => q.runId)))) : new Set<number>();
  const footer = `${state.query.trim() === "" ? "all runs" : `query: ${state.query.trim()}`} · ${drawn.size} runs`;
  const svgRef = useRef<SVGSVGElement>(null);
  const [exportErr, setExportErr] = useState<string | null>(null);

  const provenance = (): PlotProvenance => ({ tool: "Vesqira cross-run plot", query: state.query, runIds: [...drawn].sort((a, b) => a - b), y: plot.y, x: `${plot.xKind}:${xKey}`, by: plot.by, exportedAt: new Date().toISOString(), ...(model?.conversion ? { conversion: { y: plot.y, ...model.conversion } } : {}) });
  const svgText = () => {
    const el = svgRef.current;
    if (!el) return null;
    const css = getComputedStyle(document.documentElement);
    let raw = new XMLSerializer().serializeToString(el).replace(/ tabindex="0"/, "").replace(/ data-testid="[^"]*"/g, "");
    const box = viewBoxOf(el);
    raw = raw.replace('width="100%"', `width="${box.width}" height="${box.height}"`); // a file has a size of its own; "100%" is the page's
    if (!/^<svg[^>]*\sxmlns="/.test(raw)) raw = raw.replace("<svg", '<svg xmlns="http://www.w3.org/2000/svg"');
    // the serializer writes the SVG namespace itself; the exported file needs it to open outside the page
    return svgWithProvenance(resolveVars(raw, (n) => css.getPropertyValue(n).trim()), provenance());
  };
  const exportSvg = () => {
    const t = svgText();
    if (t) download("vesqira-runs-plot.svg", new Blob([`<?xml version="1.0" encoding="UTF-8"?>\n${t}`], { type: "image/svg+xml" }));
  };
  async function exportPng() {
    const t = svgText();
    const el = svgRef.current;
    if (!t || !el) return;
    try {
      const box = viewBoxOf(el);
      const img = new Image(box.width, box.height);
      img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(t)}`;
      await img.decode();
      const scale = 2;
      const canvas = document.createElement("canvas");
      canvas.width = box.width * scale;
      canvas.height = box.height * scale;
      const g = canvas.getContext("2d");
      if (!g) throw new Error("no canvas");
      g.drawImage(img, 0, 0, canvas.width, canvas.height);
      const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, "image/png"));
      if (!blob) throw new Error("no image");
      const bytes = pngWithText(new Uint8Array(await blob.arrayBuffer()), "vesqira-provenance", JSON.stringify(provenance()));
      download("vesqira-runs-plot.png", new Blob([bytes as BlobPart], { type: "image/png" }));
      setExportErr(null);
    } catch (e) {
      setExportErr(`The browser could not draw the PNG (${e instanceof Error ? e.message : "no reason given"}); the SVG has the same figure.`);
    }
  }
  // the converted numbers always sit beside the recorded ones: the result's own column is added if the table does not show it
  const csvColumns = state.conversion && !columns.some((c) => c.toLowerCase() === `m:${state.conversion!.y}`.toLowerCase()) ? [...columns, `m:${state.conversion.y}`] : columns;
  const exportCsv = () => download("vesqira-runs.csv", new Blob([runsCsv(rows, csvColumns, ctxOf, { query: state.query, total: active, at: new Date().toISOString(), notRecorded: state.showMissing ? hidden : 0, conversion: model?.conversion ? state.conversion : null })], { type: "text/csv;charset=utf-8" }));

  // ---- saving the plot as a board figure (ADR 0065): drawn from the runs the filter matches, with the conversion that is shown
  const [figTitle, setFigTitle] = useState("");
  const [figBusy, setFigBusy] = useState(false);
  const [figMsg, setFigMsg] = useState<string | null>(null);
  const [savedFig, setSavedFig] = useState<Figure | null>(null);
  const [boards, setBoards] = useState<Board[]>([]);
  const [onBoard, setOnBoard] = useState("");
  async function saveFigure() {
    if (plot.y === "" || xKey === "") return;
    setFigBusy(true);
    setFigMsg(null);
    try {
      const figModel = withRefusals(buildPlot(ev.matched, plot.y, { kind: plot.xKind, key: xKey }, plot.by, state.conversion), refusals);
      if (drawnRunIds(figModel).length === 0) throw new Error("None of the runs the filter matches records both values, so there is nothing to save.");
      const f = await saveRunFigure(figModel, { query: state.query, y: plot.y, x: xKey, xKind: plot.xKind, by: plot.by }, state.conversion, { title: figTitle.trim() || `${plot.y} against ${xKey}`, caption: "" });
      setSavedFig(f);
      setOnBoard("");
      listBoards().then(setBoards).catch(() => undefined);
    } catch (e) {
      setSavedFig(null);
      setFigMsg(e instanceof Error ? e.message : "The figure could not be saved.");
    } finally {
      setFigBusy(false);
    }
  }
  async function putOnBoard(id: string) {
    setOnBoard(id);
    if (id === "" || !savedFig) return;
    try {
      await addToBoard(Number(id), savedFig.id);
      setFigMsg(`Added to the board “${boards.find((b) => String(b.id) === id)?.title ?? ""}”.`);
    } catch (e) {
      setFigMsg(e instanceof Error ? e.message : "It could not be added to the board.");
    }
  }

  // ---- unit conversion of a panel that differs only by unit (the worker converts; the browser never does)
  const [converting, setConverting] = useState<string | null>(null);
  const [convertMsg, setConvertMsg] = useState<string | null>(null);
  async function convert(offer: ConvertOffer) {
    setConverting(offer.unit);
    setConvertMsg(null);
    try {
      const res = await convertRunValues(offer.to, offer.values.map((v) => ({ value: v.value, uncertainty: v.uncertainty, from: offer.unit })));
      if (res.refused || !res.results) {
        setConvertMsg(`Not converted: ${res.reason ?? "the worker refused."}`);
        return;
      }
      onState({ conversion: conversionFrom(plot.y, offer, res, state.conversion) });
    } catch (e) {
      setConvertMsg(e instanceof Error ? e.message : "The conversion could not be asked for.");
    } finally {
      setConverting(null);
    }
  }

  // ---- table helpers
  const toggleSort = (column: ColumnId) => onState({ sort: state.sort.column === column ? { column, dir: state.sort.dir === 1 ? -1 : 1 } : { column, dir: 1 } });
  const aria = (c: ColumnId) => (state.sort.column === c ? (state.sort.dir === 1 ? "ascending" : "descending") : "none");
  const mark = (c: ColumnId) => (state.sort.column === c ? (state.sort.dir === 1 ? <ChevronUp size={12} aria-hidden="true" /> : <ChevronDown size={12} aria-hidden="true" />) : null);

  return (
    <div className={s.root}>
      <div className={s.page}>
        <header className={s.head}>
          {onBack ? <Button variant="ghost" onClick={onBack}>Back</Button> : null}
          <h2 className={s.title}>Runs</h2>
          <span className={s.sub} role="status">{`${ev.matched.length}${state.showMissing && hidden > 0 ? ` + ${hidden} not recorded` : ""} of ${active} runs`}</span>
        </header>

        {runs.length === 0 ? <p className={s.note}>No runs yet. Import a run folder from the Runs section of the explorer.</p> : (
          <FilterBox label="Filter runs" placeholder="Filter: am>0.3  block_size:16..64  status:completed" value={state.query} onChange={(q) => onState({ query: q })} vocabulary={vocabulary} shown={ev.matched.length} total={active} noun="runs" />
        )}

        {hidden > 0 ? (
          <p className={s.banner} role="status">
            {`${hidden} run${hidden === 1 ? "" : "s"} ${state.showMissing ? "listed after the matches" : "left out"}: ${hidden === 1 ? "it did" : "they did"} not record ${[...new Set(ev.notRecorded.flatMap((x) => x.words))].join(", ")}, so the filter cannot say if ${hidden === 1 ? "it matches" : "they match"}. `}
            <button type="button" className={s.link} onClick={() => onState({ showMissing: !state.showMissing })}>{state.showMissing ? "Hide them" : "Show them, last"}</button>
          </p>
        ) : null}
        {ev.unitDiffers.length > 0 ? <p className={s.banner} role="status">{`${ev.unitDiffers.length} run${ev.unitDiffers.length === 1 ? " records" : "s record"} ${[...new Set(ev.unitDiffers.flatMap((x) => x.words))].join(", ")} in another unit, and no conversion is invented: left out.`}</p> : null}

        <div className={s.tools}>
          <Button aria-expanded={colsOpen} aria-controls="runs-columns" onClick={() => setColsOpen(!colsOpen)}>Columns</Button>
          <Select label="Saved view" value={applied} onChange={(e) => {
            setApplied(e.target.value);
            const v = views.find((x) => String(x.id) === e.target.value);
            if (v) onState(stateFrom(v.spec, { selected: state.selected }));
          }}>
            <option value="">(none)</option>
            {views.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}
          </Select>
          {naming ? (
            <form className={s.nameForm} onSubmit={saveView}>
              <Input label="View name" value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
              <Button type="submit" variant="primary" disabled={name.trim() === ""}>Save</Button>
              <Button onClick={() => { setNaming(false); setViewErr(null); }}>Cancel</Button>
            </form>
          ) : (
            <>
              <Button onClick={() => { setNaming(true); setName(views.find((x) => String(x.id) === applied)?.name ?? ""); }}>Save view…</Button>
              <Button disabled={applied === ""} onClick={() => void removeView()}>Delete view</Button>
            </>
          )}
          <span className={s.grow} />
          <Button icon={Download} onClick={exportCsv} disabled={rows.length === 0}>Export table (CSV)</Button>
        </div>
        {viewErr ? <p role="alert" className={s.err}>{viewErr}</p> : null}
        {colsOpen ? (
          <div id="runs-columns" className={s.columns} role="group" aria-label="Columns of the table">
            <fieldset className={s.group}>
              <legend>Run</legend>
              {FIXED_COLUMNS.map((c) => <Checkbox key={c.id} label={c.label} checked={state.columns.includes(c.id)} onChange={() => toggleColumn(c.id)} />)}
            </fieldset>
            <fieldset className={s.group}>
              <legend>Parameters</legend>
              {choices.params.length === 0 ? <span className={s.note}>No run records a parameter.</span> : choices.params.map((p) => {
                const id = `p:${p.key}`;
                return <Checkbox key={id} label={`${p.key} (${p.count})`} checked={state.columns.includes(id) || queried.has(p.key.toLowerCase())} disabled={queried.has(p.key.toLowerCase())} onChange={() => toggleColumn(id)} />;
              })}
            </fieldset>
            <fieldset className={s.group}>
              <legend>Results</legend>
              {choices.metrics.length === 0 ? <span className={s.note}>No run records a result.</span> : choices.metrics.map((m) => {
                const id = `m:${m.key}`;
                return <Checkbox key={id} label={`${m.key}${m.units.length === 1 && m.units[0] ? ` (${m.units[0]})` : m.units.length > 1 ? " (units differ)" : ""} (${m.count})`} checked={state.columns.includes(id) || queried.has(m.key.toLowerCase())} disabled={queried.has(m.key.toLowerCase())} onChange={() => toggleColumn(id)} />;
              })}
            </fieldset>
            <p className={s.note}>A column for something the filter asks about is always shown.</p>
          </div>
        ) : null}

        <section className={s.plotBox} aria-label="Results across runs">
          <div className={s.plotTools}>
            <Select label="Result (y)" value={plot.y} onChange={(e) => { setConvertMsg(null); onState({ plot: { ...plot, y: e.target.value }, conversion: null }); }}>
              <option value="">(choose a result)</option>
              {mKeys.map((k) => <option key={k} value={k}>{k}</option>)}
            </Select>
            <Select label="Against (x)" value={xKey ? `${plot.xKind}:${xKey}` : ""} onChange={(e) => {
              const v = e.target.value;
              onState({ plot: { ...plot, xKind: v.startsWith("metric:") ? "metric" : "param", x: v.slice(v.indexOf(":") + 1) } });
            }}>
              <option value="">(choose)</option>
              <optgroup label="Parameters">{pKeys.map((k) => <option key={k} value={`param:${k}`}>{k}</option>)}</optgroup>
              <optgroup label="Results">{mKeys.filter((k) => k !== plot.y).map((k) => <option key={k} value={`metric:${k}`}>{k}</option>)}</optgroup>
            </Select>
            <Select label="Colour and shape by" value={plot.by} onChange={(e) => onState({ plot: { ...plot, by: e.target.value } })}>
              <option value="">(none)</option>
              {pKeys.map((k) => <option key={k} value={k}>{k}</option>)}
            </Select>
            <span className={s.grow} />
            <Button disabled={!model || drawn.size === 0} onClick={exportSvg}>Export plot (SVG)</Button>
            <Button disabled={!model || drawn.size === 0} onClick={() => void exportPng()}>Export plot (PNG)</Button>
          </div>
          {exportErr ? <p role="alert" className={s.err}>{exportErr}</p> : null}
          {model && (model.offers.some((o) => convertible(o).state !== "pending") || model.conversion) ? (
            <div className={s.convert} role="group" aria-label="Unit conversion">
              {model.offers.map((o) => {
                const c = convertible(o);
                if (c.state === "pending") return null; // not known yet: no button that might then go away
                const runs = `${o.values.length} run${o.values.length === 1 ? "" : "s"} recorded ${plot.y} in ${o.unit}`;
                if (c.state === "yes") {
                  return (
                    <p key={o.unit} className={s.note}>
                      {`${runs}, which differs from the main panel only by unit. `}
                      <Button disabled={converting !== null} onClick={() => void convert(o)}>{`Convert to ${o.to}`}</Button>
                    </p>
                  );
                }
                return <p key={o.unit} className={s.note}>{c.state === "no" ? `${runs}, not ${o.to}, and it is not converted: ${c.reason}.` : `${runs}; whether it can be converted to ${o.to} could not be checked.`}</p>;
              })}
              {model.conversion ? (
                <p className={s.note}>
                  {`Converted to ${model.conversion.to}: ${model.conversion.from.map((f) => `${f.count} from ${f.unit} (x ${num(f.factor)})`).join(", ")}. Plain unit conversion, no equivalency; what each run recorded is kept in the CSV beside the converted values. `}
                  <Button variant="ghost" onClick={() => onState({ conversion: null })}>Undo conversion</Button>
                </p>
              ) : null}
            </div>
          ) : null}
          {convertMsg ? <p role="alert" className={s.err}>{convertMsg}</p> : null}
          {model && drawn.size > 0 ? (
            <div className={s.saveRow} role="group" aria-label="Save the plot as a figure">
              <Input label="Figure title" value={figTitle} placeholder={`${plot.y} against ${xKey}`} maxLength={200} onChange={(e) => setFigTitle(e.target.value)} />
              <Button disabled={figBusy} onClick={() => void saveFigure()}>{figBusy ? "Saving…" : "Save as board figure"}</Button>
              <span className={s.note}>{`Draws the ${ev.matched.length} run${ev.matched.length === 1 ? "" : "s"} the filter matches${state.showMissing && hidden > 0 ? " (the runs listed after them are not drawn)" : ""}; a reviewer sees the picture and the filter words, not the runs.`}</span>
            </div>
          ) : null}
          {savedFig ? (
            <p role="status" className={s.note}>
              {`Saved as the figure “${savedFig.title}”. `}
              {boards.length > 0 ? (
                <Select label="Add to board" value={onBoard} onChange={(e) => void putOnBoard(e.target.value)}>
                  <option value="">(choose a board)</option>
                  {boards.map((b) => <option key={b.id} value={b.id}>{b.title}</option>)}
                </Select>
              ) : "Make a board to put it on."}
            </p>
          ) : null}
          {figMsg ? <p role={savedFig && figMsg.startsWith("Added") ? "status" : "alert"} className={savedFig && figMsg.startsWith("Added") ? s.note : s.err}>{figMsg}</p> : null}
          {!model ? <p className={s.note}>Choose a result and what to draw it against. The plot shows the runs in the table, not a ranking; nothing is called best.</p>
            : drawn.size === 0 ? <p className={s.note}>{`None of the ${rows.length} runs in the table records both ${plot.y} and ${xKey}, so there is nothing to draw.`}</p>
            : <RunsPlot ref={svgRef} model={model} selected={state.selected} onSelect={select} onOpen={onOpenRun} footer={footer} />}
          {model && model.excluded.length > 0 && drawn.size > 0 ? (
            <details className={s.excluded}>
              <summary>{`${model.excluded.length} of ${rows.length} runs in the table are not drawn`}</summary>
              <ul>{model.excluded.slice(0, 50).map((x) => <li key={x.runId}>{`${x.name}: ${x.reason}`}</li>)}{model.excluded.length > 50 ? <li>{`and ${model.excluded.length - 50} more`}</li> : null}</ul>
            </details>
          ) : null}
          {selectedRun ? (
            <p className={s.selected} role="status">
              {`Selected: ${selectedRun.name}`}
              <Button onClick={() => onOpenRun(selectedRun.id)}>Open run</Button>
              <Button variant="ghost" onClick={() => select(null)}>Clear selection</Button>
            </p>
          ) : null}
        </section>

        {runs.length > 0 && rows.length === 0 ? <p className={s.note}>No run matches. Remove a filter above to widen the list.</p> : null}
        {rows.length > 0 ? (
          <ScrollTable label="Runs table" className={s.scroll}>
            <table className={s.table}>
              <caption className={s.cap}>One row per run. A blank value reads “{NOT_RECORDED}” and sorts last; the plot above draws the same rows.</caption>
              <thead>
                <tr>
                  <th scope="col"><span className={s.vh}>Select</span></th>
                  <th scope="col" aria-sort={aria("name")}><button type="button" className={s.sortBtn} onClick={() => toggleSort("name")}>Run{mark("name")}</button></th>
                  {columns.map((c) => (
                    <th key={c} scope="col" aria-sort={aria(c)}>
                      <button type="button" className={s.sortBtn} onClick={() => toggleSort(c)}>{columnLabel(c, runs)}{mark(c)}</button>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {shownRows.map((r) => {
                  const on = r.id === state.selected;
                  const ctx = ctxOf(r);
                  return (
                    <tr key={r.id} ref={(el) => { if (el) rowRefs.current.set(r.id, el); else rowRefs.current.delete(r.id); }} aria-selected={on} data-selected={on || undefined} onClick={() => select(on ? null : r.id)}>
                      <td><button type="button" className={s.pick} aria-pressed={on} aria-label={`Select ${r.name}`} onClick={(e) => { e.stopPropagation(); select(on ? null : r.id); }} /></td>
                      <th scope="row" className={s.name}>
                        <button type="button" className={s.open} onClick={(e) => { e.stopPropagation(); onOpenRun(r.id); }}>{r.name}</button>
                        {r.archived ? <span className={s.tag}>archived</span> : null}
                        {missingWords.has(r.id) ? <span className={s.tag} title={`Did not record ${missingWords.get(r.id)!.join(", ")}`}>{`no ${missingWords.get(r.id)!.join(", ")}`}</span> : null}
                      </th>
                      {columns.map((c) => {
                        const cell = cellOf(r, c, ctx);
                        if (cell.kind === "missing") return <td key={c} className={s.nr}>{NOT_RECORDED}</td>;
                        const m = cell.kind === "num" ? cell.metric : undefined;
                        return (
                          <td key={c} className="mono" title={m ? [m.definition, m.uncertainty !== null ? `uncertainty: ${m.uncertaintyKind === "" ? "kind not stated" : KIND[m.uncertaintyKind] ?? m.uncertaintyKind}` : ""].filter(Boolean).join("; ") || undefined : undefined}>
                            {cell.text}
                            {m && m.uncertainty !== null ? <span className={s.unc}>{` ± ${num(m.uncertainty)}${m.uncertaintyKind === "" ? " (kind not stated)" : ""}`}</span> : null}
                          </td>
                        );
                      })}
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {rows.length > shownRows.length ? <p className={s.more}><button type="button" className={s.link} onClick={() => setLimit(limit + STEP)}>{`Show ${Math.min(STEP, rows.length - shownRows.length)} more of ${rows.length - shownRows.length} runs`}</button></p> : null}
          </ScrollTable>
        ) : null}
      </div>
    </div>
  );
}
