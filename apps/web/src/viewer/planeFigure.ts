import type { Dataset } from "../api/catalog";
import type { FigureRequest } from "../api/boards";
import type { Plane, RegionShape, View } from "../api/planes";
import { overlayBasis } from "../scene/basis";
import { needsGeometry } from "../scene/geometry";
import type { GeometryMap } from "../scene/geometry";
import { sceneLayers } from "../scene/overlays";
import { regionsOf } from "../scene/scene";
import type { Scene } from "../scene/scene";
import { lut } from "./colormaps";
import type { ColormapId } from "./colormaps";
import { captionLines, composeImageFigure, figureScale } from "./figure";
import type { ImageFigure } from "./figure";
import { limits } from "./render";
import type { RangeMode, Stretch } from "./render";

/** The name a figure's caption gives a dataset; never its id, which belongs to the owner's side (ADR 0011). */
export const datasetNameOf = (dataset: Dataset, datasets: Dataset[] | undefined) => (id: number) =>
  id === dataset.id ? dataset.name : datasets?.find((d) => d.id === id)?.name ?? "another dataset";

export type PlaneFigureInput = {
  dataset: Dataset;
  datasets?: Dataset[];
  view: View;
  plane: Plane;
  /** The figure scene (`figureScene`): what the picture shows. */
  drawn: Scene;
  interp: Dataset["interpretation"];
  reliability: Plane | null;
  geometry: GeometryMap;
  title: string;
  figureWidth: number;
  showLines: boolean;
};

/**
 * Everything a plane figure says and draws, from the figure scene and what was read for it: the parts of the picture
 * (`figure`, which paints the PNG and writes the SVG) and the view description the server checks the scene against. Saving
 * a figure and drawing one again both come through here, which is why a redrawing of unchanged data is the same picture.
 */
export function planeFigureParts(i: PlaneFigureInput): { figure: ImageFigure; viewParams: FigureRequest["view"] } {
  const { dataset, view, plane, drawn } = i;
  const d = drawn.base.display;
  const axial = i.interp !== null && d.mapping === "axial";
  const range = d.range as RangeMode;
  const lim = limits(plane.head.stats, range, (d.limits as [number, number] | undefined) ?? null);
  const region = ((regionsOf(drawn)?.active && regionsOf(drawn)!.active!.shape !== "line" ? regionsOf(drawn)!.active : null) ?? null) as RegionShape | null;
  const facts = { dataset: dataset.name, kind: axial ? ("orientation" as const) : ("plane" as const), channelLine: "", displayLine: "", limits: lim, limitsUnit: view.bunit, unitDeclared: view.bunit_status === "user" };
  const captions = captionLines(facts, view, plane, { colormap: d.colormap, stretch: d.stretch, range: d.range, axial, region, nature: dataset.nature });
  const nameOf = datasetNameOf(dataset, i.datasets);
  // every overlay drawn from another file (or the grid) states its basis in the caption, as its row does on screen
  const overlayLines = drawn.overlays
    .filter((o) => o.visible && needsGeometry(o, drawn))
    .map((o) => overlayBasis(o, drawn, i.geometry[o.id], nameOf))
    .filter((t): t is string => t !== null);
  const scale = figureScale(i.figureWidth, plane.head.width);
  const figure: ImageFigure = {
    planeW: plane.head.width,
    planeH: plane.head.height,
    layers: sceneLayers(drawn, { plane, scale, interp: i.interp, reliability: i.reliability, geometry: i.geometry }).map((l) => l.marks),
    legend: axial ? { kind: "cyclic", lut: lut("twilight") } : { kind: "scalar", lut: lut(d.colormap as ColormapId), limits: lim, unit: view.bunit },
    title: i.title,
    captions: [...captions, ...overlayLines],
    width: i.figureWidth,
  };
  const kind = axial ? ("orientation" as const) : ("plane" as const);
  const viewParams: FigureRequest["view"] = {
    kind,
    channel: plane.head.channel,
    ...(plane.head.spectral_value !== null ? { spectralValue: plane.head.spectral_value, spectralUnit: plane.head.spectral_unit ?? undefined } : {}),
    ...(axial ? {} : { colormap: d.colormap as ColormapId, stretch: d.stretch as Stretch, range, ...(lim ? { limits: lim, limitsUnit: view.bunit ?? undefined } : {}) }),
    ...(axial ? { showLines: i.showLines } : {}),
    ...(region ? { region: region.shape === "rect" ? { shape: "rect", x0: region.x0, y0: region.y0, x1: region.x1, y1: region.y1 } : { shape: "circle", cx: region.cx, cy: region.cy, r: region.r } } : {}),
  };
  return { figure, viewParams };
}

/** Composes the PNG of a plane figure from its parts and the base raster canvas. */
export async function composePlaneFigure(i: PlaneFigureInput & { source: HTMLCanvasElement }): Promise<{ blob: Blob; figure: ImageFigure; viewParams: FigureRequest["view"] }> {
  const { figure, viewParams } = planeFigureParts(i);
  return { blob: await composeImageFigure({ ...figure, source: i.source }), figure, viewParams };
}
