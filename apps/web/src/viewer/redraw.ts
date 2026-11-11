import { createFigure, figurePreviewUrl } from "../api/boards";
import type { Figure } from "../api/boards";
import { listDatasets } from "../api/catalog";
import type { Dataset } from "../api/catalog";
import { getChannelGrid, getPanel } from "../api/channelmaps";
import type { ChannelGridSpec } from "../api/channelmaps";
import { getOverlayGeometry } from "../api/overlays";
import { getPlane, getView } from "../api/planes";
import type { Plane } from "../api/planes";
import { overlayTitle } from "../scene/basis";
import { figureScene } from "../scene/figureScene";
import { needsGeometry, spacingFor } from "../scene/geometry";
import type { GeometryEntry } from "../scene/geometry";
import { orientationOf } from "../scene/scene";
import type { Scene, SceneOverlay } from "../scene/scene";
import { lut } from "./colormaps";
import type { ColormapId } from "./colormaps";
import { composeGridFigure, GRID_NODATA, gridCaption, gridFigure, paintPixels, panelLabel, STRETCH_LABEL } from "./channelGrid";
import { blobToBase64, figureScale } from "./figure";
import { figureSvg } from "./figureSvg";
import { composePlaneFigure } from "./planeFigure";
import type { PlaneFigureInput } from "./planeFigure";
import { limits, renderPlane } from "./render";
import type { RangeMode, Rgb, Stretch } from "./render";

/**
 * "Draw again" (ADR 0042, section 5): the picture of a stored figure, made again from its stored description with the viewer's
 * own code. The base raster is the CPU renderer at the canvas size the figure recorded (the GPU renderer agrees with it to one
 * colour step, ADR 0034), the overlays are the registry's marks from geometry the worker computes again, and the picture is
 * composed by the function a save uses. Nothing is swapped silently: the caller has already asked the owner about any file that
 * changed (`getRedrawCheck`), and the redrawing becomes a new figure only when the owner saves it.
 */
export type Redrawn = {
  kind: "scene" | "channel-grid";
  blob: Blob;
  /** Notes the owner must read: what is different from the stored description now (a contour sigma that was computed again, a changed preview step). */
  notes: string[];
  /** The SVG document, for a scene figure (a grid is panels of raster only); null otherwise. */
  svg: (() => string) | null;
  /** Saves the redrawing as a new figure linked to the original. The original is not touched. */
  save: () => Promise<Figure>;
};

const describe = (e: unknown, what: string) => (e instanceof Error && e.message ? `${what}: ${e.message}` : `${what} could not be read.`);

/** The base raster as the viewer's CPU path draws it, at the canvas size the figure recorded. */
export function baseRaster(plane: Plane, scene: Scene, interp: Dataset["interpretation"], render: NonNullable<Scene["render"]>): HTMLCanvasElement {
  const d = scene.base.display;
  const axial = d.mapping === "axial" && interp !== null;
  const img = renderPlane(plane.data, plane.head.width, plane.head.height, {
    lut: lut(axial ? "twilight" : (d.colormap as ColormapId)),
    range: axial ? null : limits(plane.head.stats, d.range as RangeMode, (d.limits as [number, number] | undefined) ?? null),
    axial: axial ? { toDegrees: interp!.unit === "deg" ? 1 : 180 / Math.PI } : undefined,
    stretch: d.stretch as Stretch,
    outW: render.canvas.width,
    outH: render.canvas.height,
    nodata: render.nodata.map((c) => [c[0], c[1], c[2]]) as unknown as [Rgb, Rgb],
  });
  const canvas = document.createElement("canvas");
  canvas.width = img.width;
  canvas.height = img.height;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("This browser cannot draw figures.");
  ctx.putImageData(new ImageData(img.pixels, img.width, img.height), 0, 0);
  return canvas;
}

/** A contour overlay that recorded its sigma, without it, so the worker measures the noise of the data as it is now. */
function withoutSigma(o: SceneOverlay): SceneOverlay {
  if (o.kind !== "contours" || o.levels.mode !== "sigma") return o;
  const { sigma: _s, ...levels } = o.levels;
  return { ...o, levels };
}

const sigmas = (s: Scene) => Object.fromEntries(s.overlays.flatMap((o) => (o.kind === "contours" && o.levels.mode === "sigma" && o.levels.sigma !== undefined ? [[o.id, o.levels.sigma] as const] : [])));

/**
 * Draws a scene figure again. `refresh` is true once the owner agreed to draw from data that is not proven unchanged: contour
 * sigmas are then measured again and recorded; otherwise the recorded sigma must match what the worker measures, so a file that
 * changed without the identity showing it is refused rather than drawn as if it were the original.
 */
export async function redrawSceneFigure(f: Figure, o: { refresh: boolean }): Promise<Redrawn> {
  const stored = f.scene as Scene | undefined;
  const render = stored?.render;
  if (!stored || stored.schema !== "scene/1" || !render) throw new Error("This figure does not record how it was drawn, so it cannot be drawn again.");
  const datasets = await listDatasets({ archived: "all" });
  const base = datasets.find((d) => d.id === stored.base.datasetId);
  if (!base) throw new Error("The figure's dataset is no longer registered.");
  const axial = stored.base.display.mapping === "axial" && base.interpretation !== null;
  const [view, plane] = await Promise.all([getView(base.id), getPlane(base.id, stored.base.channel)]);
  if (plane.head.preview) throw new Error("The plane came back as a reduced preview; it cannot be drawn again at full resolution.");
  const relId = base.interpretation?.reliabilityId ?? null;
  const reliability = axial && relId !== null ? await getPlane(relId, 0).catch(() => null) : null;
  const s = figureScale(render.figureWidth, plane.head.width);
  const nameOf = (id: number) => datasets.find((d) => d.id === id)?.name ?? "another dataset";
  const geometry: Record<string, GeometryEntry> = {};
  await Promise.all(
    stored.overlays
      .filter((ov) => ov.visible && needsGeometry(ov, stored))
      .map(async (ov) => {
        const asked = o.refresh ? withoutSigma(ov) : ov;
        try {
          const g = await getOverlayGeometry(base.id, asked, spacingFor(ov, s));
          geometry[ov.id] = { key: ov.id, status: "ready", g };
        } catch (e) {
          // a layer the figure drew that cannot be drawn now is a reason to stop, never a layer quietly left out
          throw new Error(describe(e, `${overlayTitle(ov, stored, nameOf)} cannot be drawn again`));
        }
      }),
  );
  const drawn = figureScene(stored, { plane, axial, scale: s, bunit: view.bunit, interp: base.interpretation, reliability, geometry, datasets });
  const input: PlaneFigureInput = { dataset: base, datasets, view, plane, drawn, interp: base.interpretation, reliability, geometry, title: f.title, figureWidth: render.figureWidth, showLines: orientationOf(drawn)?.visible ?? true };
  const source = baseRaster(plane, drawn, base.interpretation, render);
  const made = await composePlaneFigure({ ...input, source });
  const scene: Scene = { ...drawn, render };
  const notes: string[] = [];
  const was = sigmas(stored), now = sigmas(drawn);
  for (const id of Object.keys(now)) {
    if (was[id] !== undefined && was[id] !== now[id]) {
      const ov = drawn.overlays.find((x) => x.id === id)!;
      notes.push(`${overlayTitle(ov, drawn, nameOf)}: the noise level (σ) is now ${Number(now[id]!.toPrecision(4))}, the figure recorded ${Number(was[id]!.toPrecision(4))}; the levels are drawn at the new value.`);
    }
  }
  if (!notes.length && JSON.stringify({ ...stored, render: undefined }) !== JSON.stringify({ ...drawn })) notes.push("The scene as drawn now differs from the one the figure stored.");
  const meta = { title: f.title, caption: f.caption, provenance: f.provenance, software: "Vesqira; vector layers drawn from the figure's stored scene over the base raster as drawn, units and provenance as the PNG download carries them" };
  return {
    kind: "scene",
    blob: made.blob,
    notes,
    svg: () => figureSvg({ ...made.figure, raster: source.toDataURL("image/png"), meta }),
    save: async () => createFigure({ datasetId: base.id, title: f.title, caption: f.caption, view: made.viewParams, previewPng: await blobToBase64(made.blob), scene, redrawnFrom: f.id }),
  };
}

const POOL = 4;
async function inPool<T, R>(items: T[], f: (x: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(POOL, items.length) }, async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await f(items[i]!);
    }
  }));
  return out;
}

/** Draws a channel-map grid again from its channel-grid/1 description: the recorded shared scale, stretch and colour map, the panels read again at the recorded preview step. */
export async function redrawGridFigure(f: Figure): Promise<Redrawn> {
  const spec = f.scene as ChannelGridSpec | undefined;
  if (!spec || spec.schema !== "channel-grid/1" || !spec.render) throw new Error("This figure does not record how it was drawn, so it cannot be drawn again.");
  const datasets = await listDatasets({ archived: "all" });
  const base = datasets.find((d) => d.id === spec.datasetId);
  if (!base) throw new Error("The figure's dataset is no longer registered.");
  const [view, planned] = await Promise.all([
    getView(base.id),
    getChannelGrid(base.id, { first: spec.first, last: spec.last, stride: spec.stride, average: spec.average, unit: spec.spectral?.unit ?? "", convention: spec.spectral?.convention ?? "", limits: [spec.display.limits[0]!, spec.display.limits[1]!] }),
  ]);
  const [lo, hi] = [spec.display.limits[0]!, spec.display.limits[1]!];
  // the shared scale is the recorded one, with the words the figure recorded for it: a scale measured again is not the figure's scale
  const plan = { ...planned, scale: { ...planned.scale, lo, hi, source: spec.display.source } };
  const notes: string[] = [];
  if (plan.step !== spec.preview.step) notes.push(`The panels are now read at preview step ${plan.step}; the figure recorded ${spec.preview.step}.`);
  const colormap = spec.display.colormap as ColormapId, stretch = spec.display.stretch as Stretch;
  const panels = await inPool(plan.panels, async (p) => {
    const l = await getPanel(base.id, p, plan.step).catch((e: unknown) => { throw new Error(describe(e, `Panel ${p.index + 1}`)); });
    const c = document.createElement("canvas");
    c.width = l.head.width;
    c.height = l.head.height;
    c.getContext("2d")?.putImageData(new ImageData(paintPixels(l.data, l.head.width, l.head.height, { lo, hi }, lut(colormap), stretch, GRID_NODATA) as Uint8ClampedArray<ArrayBuffer>, l.head.width, l.head.height), 0, 0);
    return { label: panelLabel(p, plan), source: c };
  });
  const bunit = view.bunit ?? null;
  const blob = await composeGridFigure({
    title: f.title,
    panels,
    columns: spec.columns,
    aspect: plan.panel_width / plan.panel_height,
    lut: lut(colormap),
    scale: { lo, hi },
    unit: bunit,
    captions: [base.name, ...gridCaption(plan, { stretch: STRETCH_LABEL[stretch], bunit, overlaysOnBase: spec.render.overlaysNotDrawn }), "Header values are declared by the file, not verified."],
    width: spec.render.figureWidth,
  });
  const built = gridFigure({ datasetId: base.id, plan, first: spec.first, last: spec.last, stride: spec.stride, average: spec.average, columns: spec.columns, unit: spec.spectral?.unit ?? "", convention: spec.spectral?.convention ?? "", colormap, stretch, bunit, overlaysNotDrawn: spec.render.overlaysNotDrawn, figureWidth: spec.render.figureWidth });
  if (!built) throw new Error("There is no shared scale to draw: the figure's limits are not usable.");
  return {
    kind: "channel-grid",
    blob,
    notes,
    svg: null,
    save: async () => createFigure({ datasetId: base.id, title: f.title, caption: f.caption, view: built.view, previewPng: await blobToBase64(blob), channelGrid: built.spec, redrawnFrom: f.id }),
  };
}

export const redrawFigure = (f: Figure, o: { refresh: boolean }): Promise<Redrawn> =>
  (f.scene as { schema?: string } | undefined)?.schema === "channel-grid/1" ? redrawGridFigure(f) : redrawSceneFigure(f, o);

/** Whether two pictures hold the same pixels (decoded RGBA, so a different PNG encoding of the same picture counts as the same). */
export async function samePixels(a: Blob, b: Blob): Promise<boolean> {
  const read = async (blob: Blob) => {
    const bmp = await createImageBitmap(blob);
    const c = document.createElement("canvas");
    c.width = bmp.width;
    c.height = bmp.height;
    const ctx = c.getContext("2d")!;
    ctx.drawImage(bmp, 0, 0);
    return ctx.getImageData(0, 0, c.width, c.height);
  };
  const [x, y] = await Promise.all([read(a), read(b)]);
  if (x.width !== y.width || x.height !== y.height) return false;
  for (let i = 0; i < x.data.length; i++) if (x.data[i] !== y.data[i]) return false;
  return true;
}

/** The figure's saved picture, as a blob. */
export const savedPicture = (f: Figure): Promise<Blob> => fetch(figurePreviewUrl(f.id)).then((r) => r.blob());

/** A file name for a download: ASCII letters, digits, dash and underscore only (the server does the same for the PNG). */
export const downloadName = (title: string, ext: string) => `${title.replace(/[^A-Za-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "") || "figure"}.${ext}`;
