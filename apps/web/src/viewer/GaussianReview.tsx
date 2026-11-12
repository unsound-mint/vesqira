import { useEffect, useMemo, useRef, useState } from "react";
import { Basis, Button, Input, ScrollTable, Select } from "../ui";
import { QueueNote, useQueueReason } from "../ui/QueueNote";
import { newRequestId } from "../api/http";
import { getCellMask, getSigmav, makeClassMaps } from "../api/rohsa";
import type { RohsaClassMaps, RohsaReviewRequest, RohsaSigmav } from "../api/rohsa";
import { CNM_SIGMA_DEFAULT_KMS } from "./rohsa";
import { getPlane } from "../api/planes";
import type { Plane } from "../api/planes";
import { download, pngWithText } from "../catalog/runExport";
import { svgToPng } from "../runs/runPicture";
import type { Viewer } from "./useViewer";
import type { ComponentsState } from "./useComponents";
import { SigmavPlot } from "./SigmavPlot";
import type { CellRef } from "./SigmavPlot";
import { ClassMapView } from "./ClassMapView";
import { sigmavSvg } from "./sigmavExport";
import { cellRange, cellsOf, decodeMask, key, parseBins, parseEdges, provenanceOf, rangeText, weightingText } from "./sigmav";
import s from "./GaussianReview.module.css";

type Weight = NonNullable<RohsaReviewRequest["weight"]>;
const isAbort = (e: unknown) => e instanceof DOMException && e.name === "AbortError";
const num = (v: number) => String(Number(v.toPrecision(4)));

type Shown = { d: RohsaSigmav; req: RohsaReviewRequest };
type Selection = { cell: CellRef; pixels: number; components: number; mask: Uint8Array; width: number; height: number };
type Maps = { result: RohsaClassMaps; planes: Plane[] };

/**
 * The review of the whole decomposition (ADR 0046, "Reviewing the decomposition"): the sigma-v diagram, the pixels behind a cell, and
 * one map per width class. Each button is one bounded pass over the run's .dat on this computer; nothing is drawn until it is asked for.
 * A map pixel is pinned in the viewer, which opens its spectrum with the components over it.
 */
export function GaussianReview({ st, v, cut }: { st: ComponentsState; v: Viewer; cut: number | null }) {
  const run = st.run!;
  const datasetId = v.datasetId;
  const [weight, setWeight] = useState<Weight>(run.cropBunit === null ? "count" : "integrated");
  const [vb, setVb] = useState({ min: "", max: "", n: "" });
  const [sb, setSb] = useState({ min: "", max: "", n: "" });
  const [edgesText, setEdgesText] = useState("");
  const [shown, setShown] = useState<Shown | null>(null);
  const [cursor, setCursor] = useState<CellRef | null>(null);
  const [sel, setSel] = useState<Selection | null>(null);
  const [maps, setMaps] = useState<Maps | null>(null);
  const [busy, setBusy] = useState<null | "diagram" | "cell" | "maps">(null);
  const [rid, setRid] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const abort = useRef<AbortController | null>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const reason = useQueueReason(busy !== null, rid);

  const velocityBins = parseBins(vb, "Centre bins");
  const sigmaBins = parseBins(sb, "Width bins", 128);
  const binError = velocityBins.error ?? sigmaBins.error;
  const edges = edgesText.trim() === "" ? { edges: cut === null ? null : [cut], error: cut === null ? "Enter a width cut above, or width edges here." : null } : parseEdges(edgesText);

  // Another run or dataset: what was drawn belongs to the old one.
  useEffect(() => {
    abort.current?.abort();
    setShown(null);
    setSel(null);
    setMaps(null);
    setCursor(null);
    setErr(null);
    setNote(null);
    setBusy(null);
    setWeight(run.cropBunit === null ? "count" : "integrated");
  }, [datasetId, run.runId, run.cropBunit]);
  useEffect(() => () => abort.current?.abort(), []);

  const request = (extra: Partial<RohsaReviewRequest> = {}): RohsaReviewRequest => ({
    runId: run.runId,
    weight,
    ...(velocityBins.bins ? { velocityBins: velocityBins.bins } : {}),
    ...(sigmaBins.bins ? { sigmaBins: sigmaBins.bins } : {}),
    ...extra,
  });

  async function guarded<T>(kind: "diagram" | "cell" | "maps", job: (signal: AbortSignal, id: string) => Promise<T>): Promise<T | undefined> {
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    const id = newRequestId();
    setRid(id);
    setBusy(kind);
    setErr(null);
    setNote(null);
    try {
      return await job(ctl.signal, id);
    } catch (e) {
      if (!isAbort(e) && !ctl.signal.aborted) setErr(e instanceof Error && e.message ? e.message : "The review could not be computed.");
      else setNote("Cancelled: the computation was stopped and nothing was kept.");
      return undefined;
    } finally {
      if (abort.current === ctl) {
        abort.current = null;
        setBusy(null);
      }
    }
  }

  async function drawDiagram() {
    if (datasetId === null || binError) return;
    const req = request();
    const d = await guarded("diagram", (signal, id) => getSigmav(datasetId, req, signal, id));
    if (d) {
      setShown({ d, req });
      setSel(null);
      setCursor(null);
    }
  }

  async function selectCell(c: CellRef) {
    if (datasetId === null || !shown) return;
    setCursor(c);
    const req = { ...shown.req, cell: { s: c.s, v: c.v } };
    const m = await guarded("cell", async (signal, id) => {
      const r = await getCellMask(datasetId, req, signal, id);
      return { r, bits: await decodeMask(r.mask) };
    });
    if (m) setSel({ cell: c, pixels: m.r.pixels, components: m.r.components, mask: m.bits, width: m.r.mask.width, height: m.r.mask.height });
  }

  async function drawMaps() {
    if (datasetId === null || edges.edges === null) return;
    const req = request({ edges: edges.edges });
    const m = await guarded("maps", async (signal, id) => {
      const result = await makeClassMaps(datasetId, req, signal, id);
      const planes = await Promise.all(result.maps.map((x) => getPlane(x.dataset.id, 0, signal)));
      return { result, planes };
    });
    if (m) setMaps(m);
  }

  const picked = (x: number, y: number) => {
    v.setPinned({ x, y });
    st.setEnabled(true);
  };

  const exportDiagram = async (kind: "svg" | "png") => {
    if (!shown) return;
    setErr(null);
    try {
      const svg = await sigmavSvg(shown.d, run.name, cut, sel?.cell ?? null, new Date().toISOString());
      if (kind === "svg") download("vesqira-sigma-v.svg", new Blob([`<?xml version="1.0" encoding="UTF-8"?>\n${svg}`], { type: "image/svg+xml" }));
      else {
        const png = await svgToPng(svg);
        const prov = JSON.stringify(provenanceOf(shown.d, run.name, cut, new Date().toISOString()));
        download("vesqira-sigma-v.png", new Blob([pngWithText(new Uint8Array(await png.arrayBuffer()), "vesqira-provenance", prov) as BlobPart], { type: "image/png" }));
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "The picture could not be made.");
    }
  };

  const heaviest = useMemo(() => (shown ? [...cellsOf(shown.d).values()].sort((a, b) => b.weight - a.weight).slice(0, 8) : []), [shown]);
  const km = shown ? shown.d.axes.centre?.unit === "km/s" : true;
  const thinOk = run.cropBunit !== null && run.cropBunit.trim() === "K";
  const sameGrid = (m: Maps) => m.planes.every((p) => !sel || (p.head.width === sel.width && p.head.height === sel.height));

  return (
    <section className={s.wrap} aria-label="Review of the decomposition">
      <h3 className={s.h}>Review the decomposition</h3>
      <p className={s.note}>The widths and centres of all {run.nGauss} Gaussians in every pixel, weighted, and maps of them per width class. Drawing the diagram and making the maps each read the whole .dat once on this computer (a large fit takes minutes); a click on a cell then uses the index the diagram wrote and is quick.</p>
      <Select label="Weight" value={weight} onChange={(e) => setWeight(e.target.value as Weight)}>
        <option value="integrated" disabled={run.cropBunit === null}>Integrated intensity{run.cropBunit === null ? " (the crop's brightness unit is unresolved)" : ` (${run.cropBunit} km/s)`}</option>
        <option value="column_thin" disabled={!thinOk}>Optically thin column density (cm⁻²){thinOk ? "" : " (needs a brightness temperature in K)"}</option>
        <option value="count">Number of components</option>
      </Select>
      <Basis summary="Bins: empty means the defaults (the crop's spectral range; widths from 0 to 12 km/s).">
        <div className={s.bins}>
          <Input label="Centre min" inputMode="decimal" value={vb.min} onChange={(e) => setVb({ ...vb, min: e.target.value })} aria-invalid={velocityBins.error !== null} />
          <Input label="Centre max" inputMode="decimal" value={vb.max} onChange={(e) => setVb({ ...vb, max: e.target.value })} aria-invalid={velocityBins.error !== null} />
          <Input label="Centre bins" inputMode="numeric" value={vb.n} onChange={(e) => setVb({ ...vb, n: e.target.value })} aria-invalid={velocityBins.error !== null} />
          <Input label="Width min" inputMode="decimal" value={sb.min} onChange={(e) => setSb({ ...sb, min: e.target.value })} aria-invalid={sigmaBins.error !== null} />
          <Input label="Width max" inputMode="decimal" value={sb.max} onChange={(e) => setSb({ ...sb, max: e.target.value })} aria-invalid={sigmaBins.error !== null} />
          <Input label="Width bins" inputMode="numeric" value={sb.n} onChange={(e) => setSb({ ...sb, n: e.target.value })} aria-invalid={sigmaBins.error !== null} />
        </div>
        <p>Bins are half-open: a value on the upper edge of a bin is in the next one, and a value on the last edge is outside the diagram. Components outside are counted, not clipped into an edge bin.</p>
      </Basis>
      {binError ? <p role="alert" className={s.err}>{binError}</p> : null}
      <div className={s.row}>
        <Button onClick={() => void drawDiagram()} disabled={datasetId === null || binError !== null || busy !== null}>{shown ? "Draw the diagram again" : "Draw the diagram"}</Button>
        {busy ? <Button variant="ghost" onClick={() => abort.current?.abort()}>Cancel</Button> : null}
      </div>
      {busy ? <p role="status" className={s.note}>{busy === "diagram" ? "Reading the .dat…" : busy === "cell" ? "Finding the pixels of the cell…" : "Making the class maps…"}</p> : null}
      <QueueNote reason={reason} />
      {note ? <p role="status" className={s.note}>{note}</p> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}

      {shown ? (
        <>
          <SigmavPlot ref={svgRef} d={shown.d} run={run.name} cut={cut} cursor={cursor} selected={sel?.cell ?? null} onCursor={setCursor} onSelect={(c) => void selectCell(c)} />
          <p className={s.note}>
            {weightingText(shown.d)}. {shown.d.in_diagram.toLocaleString("en-US")} of {shown.d.components.toLocaleString("en-US")} components are inside the diagram; {shown.d.outside.toLocaleString("en-US")} outside its limits and {shown.d.invalid_sigma.toLocaleString("en-US")} with a width that is not positive are left out.
            {!km ? " The crop has no resolved velocity axis, so widths and centres stay in channels and the width cut and class maps are not available." : ""}
          </p>
          <Basis summary={shown.d.weight_how.split(".")[0] ?? "How the weight is formed"}>
            <p>{shown.d.weight_how}</p>
            <p>{shown.d.policy}</p>
          </Basis>
          <div className={s.row}>
            <Button onClick={() => void exportDiagram("svg")}>Save SVG</Button>
            <Button onClick={() => void exportDiagram("png")}>Save PNG</Button>
          </div>
          {sel ? (
            <p role="status" className={s.note}>
              Cell σ {rangeText(cellRange(shown.d.sigma_edges, sel.cell.s))}, centre {rangeText(cellRange(shown.d.v_edges, sel.cell.v))} {shown.d.axes.centre?.unit}: {sel.components.toLocaleString("en-US")} component{sel.components === 1 ? "" : "s"} in {sel.pixels.toLocaleString("en-US")} pixel{sel.pixels === 1 ? "" : "s"}. The pixels carry stripes on the class maps below.
            </p>
          ) : (
            <p className={s.note}>Click a cell (or move with the arrow keys and press Enter) to mark its pixels on the class maps.</p>
          )}
          {heaviest.length ? (
            <ScrollTable label="Heaviest cells" className={s.scroll}>
              <table>
                <thead>
                  <tr>
                    <th scope="col">σ ({shown.d.axes.centre?.unit ?? "channels"})</th>
                    <th scope="col">Centre</th>
                    <th scope="col">Components</th>
                    <th scope="col">Weight ({shown.d.weight_unit})</th>
                    <th scope="col">Pixels</th>
                  </tr>
                </thead>
                <tbody>
                  {heaviest.map((c) => (
                    <tr key={key(c.s, c.v)} aria-selected={sel?.cell.s === c.s && sel.cell.v === c.v}>
                      <td className="mono">{rangeText(cellRange(shown.d.sigma_edges, c.s))}</td>
                      <td className="mono">{rangeText(cellRange(shown.d.v_edges, c.v))}</td>
                      <td className="mono">{c.count}</td>
                      <td className="mono">{num(c.weight)}</td>
                      <td><Button variant="ghost" onClick={() => void selectCell({ s: c.s, v: c.v })} disabled={busy !== null}>Mark pixels</Button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollTable>
          ) : null}
        </>
      ) : null}

      <h3 className={s.h}>Maps per width class</h3>
      <Input label="Width edges (km/s)" inputMode="decimal" placeholder={cut === null ? `e.g. ${CNM_SIGMA_DEFAULT_KMS}` : String(cut)} value={edgesText} onChange={(e) => setEdgesText(e.target.value)} aria-invalid={edges.error !== null} />
      {edges.error ? <p role="alert" className={s.err}>{edges.error}</p> : (
        <p className={s.note}>Classes are half-open: {edges.edges!.length === 1 ? `narrow is σ < ${edges.edges![0]} km/s, the rest is σ ≥ ${edges.edges![0]} km/s` : `edges ${edges.edges!.join(", ")} km/s give ${edges.edges!.length + 1} classes`}. Empty uses the width cut above.</p>
      )}
      <div className={s.row}>
        <Button onClick={() => void drawMaps()} disabled={datasetId === null || edges.edges === null || busy !== null}>{maps ? "Make the maps again" : "Make the class maps"}</Button>
      </div>
      {maps ? (
        <>
          <p className={s.note}>
            Registered as derived datasets with a record of the run, the weight and the edges: {maps.result.maps.map((m) => m.dataset.name).join(", ")}. {maps.result.policy}
            {maps.result.pixels_without_valid_component ? ` ${maps.result.pixels_without_valid_component.toLocaleString("en-US")} pixels have no valid component and are blank in every map.` : ""}
          </p>
          {sel && !sameGrid(maps) ? <p role="alert" className={s.err}>The cell's pixel mask and the class maps are not on the same grid, so the pixels are not marked.</p> : null}
          <div className={s.maps}>
            {maps.result.maps.map((m, i) => (
              <ClassMapView
                key={m.dataset.id}
                title={`${m.class.label} (${m.class.components.toLocaleString("en-US")} components)`}
                unit={maps.result.weight_unit}
                width={maps.planes[i]!.head.width}
                height={maps.planes[i]!.head.height}
                data={maps.planes[i]!.data}
                mask={sel && sameGrid(maps) ? sel.mask : null}
                pinned={v.pinned}
                onPick={picked}
              />
            ))}
          </div>
        </>
      ) : null}
    </section>
  );
}
