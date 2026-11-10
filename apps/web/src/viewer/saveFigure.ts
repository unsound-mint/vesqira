import type { Dataset } from "../api/catalog";
import { createFigure } from "../api/boards";
import type { Figure } from "../api/boards";
import { lut } from "./colormaps";
import { blobToBase64, captionLines, composeImageFigure, composeSpectrumFigure, FIGURE_WIDTH } from "./figure";
import { orientationLinesFor } from "./glyphs";
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
export async function saveViewFigure(v: Viewer, dataset: Dataset, opts: { title?: string; caption?: string; kind?: "plane" | "spectrum" } = {}): Promise<Figure> {
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
  if (kind === "spectrum" && v.spectrum) {
    blob = await composeSpectrumFigure({ spectrum: v.spectrum, title: heading, captions, flagged: view.flags.declared ? view.flags.channels : [], channel: plane.head.channel });
  } else {
    const src = v.canvas.current;
    if (!src) throw new Error("The image is not drawn yet.");
    const iw = FIGURE_WIDTH - 40;
    blob = await composeImageFigure({
      source: src,
      planeW: plane.head.width,
      planeH: plane.head.height,
      lines: axial && v.showLines ? orientationLinesFor(v, iw / plane.head.width) : [],
      region: v.region,
      legend: axial ? { kind: "cyclic", lut: lut("twilight") } : { kind: "scalar", lut: lut(v.colormap), limits: lim, unit: view.bunit },
      title: heading,
      captions,
    });
  }
  const r = v.region;
  const viewParams: Record<string, unknown> = {
    kind: axial ? "orientation" : kind,
    channel: plane.head.channel,
    ...(plane.head.spectral_value !== null ? { spectralValue: plane.head.spectral_value, spectralUnit: plane.head.spectral_unit ?? undefined } : {}),
    ...(axial ? {} : { colormap: v.colormap, stretch: v.stretch, range: v.range, ...(lim ? { limits: lim, limitsUnit: view.bunit ?? undefined } : {}) }),
    ...(axial ? { showLines: v.showLines } : {}),
    ...(r ? { region: r.shape === "rect" ? { shape: "rect", x0: r.x0, y0: r.y0, x1: r.x1, y1: r.y1 } : { shape: "circle", cx: r.cx, cy: r.cy, r: r.r } } : {}),
  };
  return createFigure({ datasetId: dataset.id, title: heading, caption: opts.caption ?? "", view: viewParams, previewPng: await blobToBase64(blob) });
}
