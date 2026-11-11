import type { Dataset } from "../api/catalog";
import { createFigure } from "../api/boards";
import type { Figure, FigureRequest } from "../api/boards";
import { blobToBase64, captionLines, composeSpectrumFigure, figureScale, FIGURE_WIDTH } from "./figure";
import { figureScene, overlaysPending, sceneStatable } from "../scene/figureScene";
import type { Scene } from "../scene/scene";
import { nodataColours } from "./nodata";
import { composePlaneFigure } from "./planeFigure";
import { limits } from "./render";
import type { Viewer } from "./useViewer";

/** The title a figure of what the viewer shows gets when the author gives none. */
export function defaultFigureTitle(v: Viewer, dataset: Dataset): string {
  const { view, plane } = v;
  return `${dataset.name}${view && plane && view.channels > 1 ? `, channel ${plane.head.channel + 1}` : ""}`;
}

/**
 * Saves what the viewer shows as a figure that keeps its source, parameters and provenance (pinned evidence). The
 * picture is composed in the browser from the drawn canvas, so the viewer must have drawn the full plane. Throws an
 * Error with a sentence for the user when it cannot.
 */
export async function saveViewFigure(v: Viewer, dataset: Dataset, opts: { title?: string; caption?: string; kind?: "plane" | "spectrum"; datasets?: Dataset[] } = {}): Promise<Figure> {
  const { view, plane } = v;
  const kind = opts.kind ?? "plane";
  if (!view || !plane) throw new Error("The image is not drawn yet.");
  if (kind !== "spectrum" && plane.head.preview) throw new Error("This is a reduced preview. The figure can be saved when the full plane has loaded.");
  const axial = v.interp !== null;
  const lim = limits(plane.head.stats, v.range, v.customLimits);
  const facts = { dataset: dataset.name, kind: axial ? ("orientation" as const) : kind, channelLine: "", displayLine: "", limits: lim, limitsUnit: view.bunit, unitDeclared: view.bunit_status === "user" };
  const captions = captionLines(facts, view, plane, { colormap: v.colormap, stretch: v.stretch, range: v.range, axial, region: v.region, nature: dataset.nature });
  const heading = opts.title?.trim() || defaultFigureTitle(v, dataset);
  let blob: Blob;
  let scene: Scene | null = null; // a spectrum figure is a plot, not a pane: it has no scene
  let viewParams: FigureRequest["view"];
  if (kind === "spectrum" && v.spectrum) {
    blob = await composeSpectrumFigure({ spectrum: v.spectrum, title: heading, captions, flagged: view.flags.declared ? view.flags.channels : [], channel: plane.head.channel });
    const r = v.region;
    viewParams = {
      kind: axial ? "orientation" : kind,
      channel: plane.head.channel,
      ...(plane.head.spectral_value !== null ? { spectralValue: plane.head.spectral_value, spectralUnit: plane.head.spectral_unit ?? undefined } : {}),
      ...(axial ? {} : { colormap: v.colormap, stretch: v.stretch, range: v.range, ...(lim ? { limits: lim, limitsUnit: view.bunit ?? undefined } : {}) }),
      ...(axial ? { showLines: v.showLines } : {}),
      ...(r ? { region: r.shape === "rect" ? { shape: "rect", x0: r.x0, y0: r.y0, x1: r.x1, y1: r.y1 } : { shape: "circle", cx: r.cx, cy: r.cy, r: r.r } } : {}),
    };
  } else {
    if (kind === "spectrum") throw new Error("The region spectrum is not ready yet.");
    const src = v.canvas.current;
    if (!src) throw new Error("The image is not drawn yet.");
    const scale = figureScale(FIGURE_WIDTH, plane.head.width);
    // The figure's overlays are drawn from its own scene, through the same overlay registry as the screen. The scene is
    // stored only when it states the display fully; the picture is drawn from it either way.
    if (!v.scene || v.scene.base.datasetId !== dataset.id) throw new Error("The image is not drawn yet.");
    if (overlaysPending(v.scene, v.geometry).length) throw new Error("Some overlays are still being computed. The figure can be saved once they are drawn.");
    const drawn = figureScene(v.scene, { plane, axial, scale, bunit: view.bunit, interp: v.interp, reliability: v.reliability, geometry: v.geometry, datasets: opts.datasets });
    // how the base raster was drawn, so "draw again" can reproduce it: the canvas it was drawn on, the theme's no-value colours
    const render = { canvas: { width: src.width, height: src.height }, nodata: nodataColours().map((c) => [...c]), figureWidth: FIGURE_WIDTH };
    scene = sceneStatable(drawn) && render.canvas.width > 0 && render.canvas.height > 0 ? { ...drawn, render } : null;
    const made = await composePlaneFigure({ dataset, datasets: opts.datasets, view, plane, drawn, interp: v.interp, reliability: v.reliability, geometry: v.geometry, title: heading, figureWidth: FIGURE_WIDTH, showLines: v.showLines, source: src });
    blob = made.blob;
    viewParams = made.viewParams;
  }
  return createFigure({ datasetId: dataset.id, title: heading, caption: opts.caption ?? "", view: viewParams, previewPng: await blobToBase64(blob), ...(scene ? { scene } : {}) });
}
