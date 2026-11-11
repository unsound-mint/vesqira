import type { Dataset } from "../api/catalog";
import type { Plane } from "../api/planes";
import { glyphStride } from "../viewer/glyphs";
import { limits } from "../viewer/render";
import { reliabilityFor } from "./overlays";
import type { Scene, SceneDisplay, SceneOverlay } from "./scene";

/**
 * The scene of a saved figure: the live scene projected onto what the figure's picture shows, so drawing it again gives
 * the same picture (ADR 0042, section 5). The figure's overlays are drawn from it. It differs from the live scene where
 * the picture does:
 * - the channel and its spectral value are those of the plane that was drawn, as the worker reported them;
 * - an axial map is drawn with the cyclic twilight map and no limits, whatever scalar settings the pane remembers;
 * - orientation lines record the sampling step the figure used (the screen chooses it from the zoom), and whether a
 *   reliability map set their opacity, naming that map;
 * - only the working region is drawn on a figure, so saved regions are left out;
 * - the figure shows the whole plane, so the view is the fitted one.
 */
export function figureScene(live: Scene, o: { plane: Plane; axial: boolean; scale: number; bunit: string | null; interp: Dataset["interpretation"]; reliability: Plane | null }): Scene {
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
    if (ov.kind === "orientation" && o.axial) {
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
  };
}

/** Whether a figure scene states its display fully; a custom range without valid limits does not, and is not stored. */
export const sceneStatable = (s: Scene): boolean => !(s.base.display.mapping === "scalar" && s.base.display.range === "custom" && !s.base.display.limits);
