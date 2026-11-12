import type { Dataset } from "../api/catalog";
import type { Plane } from "../api/planes";
import { glyphStride } from "../viewer/glyphs";
import { limits } from "../viewer/render";
import { reliabilityFor } from "./overlays";
import { needsGeometry } from "./geometry";
import type { GeometryMap } from "./geometry";
import type { Scene, SceneCompass, SceneDisplay, SceneMask, SceneOverlay } from "./scene";

/**
 * The scene of a saved figure: the live scene projected onto what the figure's picture shows, so drawing it again gives
 * the same picture (ADR 0042, section 5). The figure's overlays are drawn from it. It differs from the live scene where
 * the picture does:
 * - the channel and its spectral value are those of the plane that was drawn, as the worker reported them;
 * - an axial map is drawn with the cyclic twilight map and no limits, whatever scalar settings the pane remembers;
 * - orientation lines record the sampling step the figure used (the screen chooses it from the zoom), and whether a
 *   reliability map set their opacity, naming that map;
 * - only the working region is drawn on a figure, so saved regions are left out;
 * - overlays placed by the worker (contours, a beam, the grid, another map's lines) are kept only when their geometry was
 *   drawn: a hidden or refused one is left out, k x sigma levels record the sigma used, and another map's lines record the step the
 *   worker sampled at and whether its declared reliability set their opacity;
 * - the figure shows the whole plane, so the view is the fitted one;
 * - the masks hatched in the picture are the ones it drew (what is shown on screen is a view setting, not part of a pane's scene).
 * - the north and east arrows are recorded when the picture draws them, with the WCS state they rest on (ADR 0047, amendment).
 */
export function figureScene(live: Scene, o: { plane: Plane; axial: boolean; scale: number; bunit: string | null; interp: Dataset["interpretation"]; reliability: Plane | null; geometry?: GeometryMap; datasets?: Dataset[]; /** The masks hatched in the picture (ADR 0055), by what they were when it was drawn. */ masks?: SceneMask[]; /** The north and east arrows the picture draws, from the viewer's compass (ADR 0047): absent draws none. */ compass?: SceneCompass }): Scene {
  const { head } = o.plane;
  const d = live.base.display;
  let display: SceneDisplay;
  if (o.axial) {
    display = { mapping: "axial", colormap: "twilight", stretch: "linear", range: "minmax" };
  } else {
    const lim = d.range === "custom" ? limits(head.stats, "custom", (d.limits as [number, number] | undefined) ?? null) : null;
    display = {
      mapping: "scalar", colormap: d.colormap, stretch: d.stretch, range: d.range,
      ...(lim ? { limits: lim, ...(o.bunit ? { limitsUnit: o.bunit } : {}) } : {}),
      ...(d.scaleGroup ? { scaleGroup: d.scaleGroup } : {}),
    };
  }
  const overlays: SceneOverlay[] = [];
  for (const ov of live.overlays) {
    if (needsGeometry(ov, live)) {
      // contours, a beam, the grid, another map's orientation lines: drawn from the worker's geometry
      // hidden or refused: not in the picture, so not in its scene (a hidden one is not checked or authorized either)
      if (!ov.visible) continue;
      const e = o.geometry?.[ov.id];
      if (!e || e.status !== "ready") continue; // refused: not in the picture, so not in its scene (saveFigure waits for any still computing)
      if (ov.kind === "contours" && ov.levels.mode === "sigma" && e.g.kind === "contours" && e.g.head.sigma?.value) {
        overlays.push({ ...ov, levels: { ...ov.levels, sigma: e.g.head.sigma.value } }); // the sigma the levels were drawn with
      } else if (ov.kind === "orientation" && e.g.kind === "orientation") {
        const relId = o.datasets?.find((d) => d.id === ov.datasetId)?.interpretation?.reliabilityId ?? null;
        const rel = ov.opacity === "reliability" && !!e.g.head.reliability && relId !== null;
        const { reliabilityId: _r, ...rest } = ov;
        overlays.push({ ...rest, sampling: { mode: "step", step: e.g.head.step ?? 1 }, opacity: rel ? "reliability" : "uniform", ...(rel ? { reliabilityId: relId } : {}) });
      } else {
        overlays.push(ov);
      }
    } else if (ov.kind === "orientation" && o.axial) {
      const step = ov.sampling.mode === "step" ? ov.sampling.step : glyphStride(o.scale, ov.sampling.gapPx);
      const relId = o.interp?.reliabilityId ?? null;
      const rel = ov.opacity === "reliability" && relId !== null && reliabilityFor(o.plane, o.reliability) !== null;
      const { reliabilityId: _r, ...rest } = ov;
      overlays.push({ ...rest, sampling: { mode: "step", step }, opacity: rel ? "reliability" : "uniform", ...(rel ? { reliabilityId: relId } : {}) });
    } else if (ov.kind === "regions") {
      overlays.push({ ...ov, saved: [] });
    }
  }
  return {
    schema: live.schema,
    base: {
      datasetId: live.base.datasetId,
      channel: head.channel,
      ...(head.spectral_value !== null ? { spectral: { value: head.spectral_value, unit: head.spectral_unit ?? "" } } : {}),
      display,
    },
    overlays,
    view: { zoom: 1 },
    ...(o.masks?.length ? { masks: o.masks } : {}),
    ...(o.compass ? { compass: o.compass } : {}),
    ...(live.link ? { link: live.link } : {}), // how the pane was linked to the other map(s) when this was drawn (ADR 0042, section 4)
  };
}

/** The visible overlays whose geometry is still being computed: a figure waits for them rather than leave them out. */
export const overlaysPending = (live: Scene, geometry: GeometryMap | undefined): string[] =>
  live.overlays.filter((ov) => ov.visible && needsGeometry(ov, live) && geometry?.[ov.id]?.status !== "ready" && geometry?.[ov.id]?.status !== "refused").map((ov) => ov.id);

/** Whether a figure scene states its display fully; a custom range without valid limits does not, and is not stored. */
export const sceneStatable = (s: Scene): boolean => !(s.base.display.mapping === "scalar" && s.base.display.range === "custom" && !s.base.display.limits);
