import type { ChannelGridPlan, ChannelGridSpec, GridPanel } from "../api/channelmaps";
import type { FigureRequest } from "../api/boards";
import { lut as colorTable } from "./colormaps";
import type { ColormapId } from "./colormaps";
import { normalize } from "./render";
import type { Stretch } from "./render";
import { FIGURE_WIDTH, blobToBase64 } from "./figure";

/** Panels per row for n panels: a near-square grid, at most 8 wide (the cap the stored description holds). */
export function defaultColumns(n: number): number {
  return Math.max(1, Math.min(8, Math.ceil(Math.sqrt(n))));
}

const trim = (v: number) => Number(v.toPrecision(6)).toString();
/** A spectral value in the unit the plan states, as given: no silent rescaling (a velocity in km/s is asked for, not assumed). */
const at = (v: number, unit: string) => `${Number(v.toPrecision(7))} ${unit}`;

/**
 * The text on a panel: which channel or channels, and where on the spectral axis, in the unit the plan says (the file's own
 * or the converted one) with nothing invented. Channel numbers count from 1, as everywhere in the viewer.
 */
export function panelLabel(p: GridPanel, plan: Pick<ChannelGridPlan, "spectral">): string {
  const ch = p.mean ? `Mean of channels ${p.first + 1}–${p.last + 1}` : `Channel ${p.first + 1}`;
  const sp = plan.spectral;
  const a = p.spectral?.first ?? null, b = p.spectral?.last ?? null;
  if (a === null || sp.unit === null || sp.unit === undefined) return ch;
  const unit = sp.unit;
  if (!p.mean || b === null || b === a) return `${ch} · ${at(a, unit)}`;
  return `${ch} · ${Number(a.toPrecision(7))} to ${at(b, unit)}`;
}

/** What a reader must be told about the whole grid: one scale, one stretch, previews, averaging, and what is not drawn. */
export function gridCaption(plan: ChannelGridPlan, o: { stretch: string; bunit: string | null; overlaysOnBase: number }): string[] {
  const out: string[] = [];
  const lo = plan.scale.lo ?? null, hi = plan.scale.hi ?? null;
  out.push(lo !== null && hi !== null
    ? `All ${plan.panel_count} panels share one colour scale and the ${o.stretch} stretch: ${trim(lo)} to ${trim(hi)}${o.bunit ? ` ${o.bunit}` : ""}. ${plan.scale.source}.`
    : `The sampled panels hold no finite pixel, so there is no shared scale to state.`);
  if (plan.averaging) out.push(`Each panel is the mean of ${plan.averaging.channels} channels (${plan.averaging.order}). Non-finite values are excluded from the mean and a pixel with none is no value, never zero.`);
  if (plan.preview) out.push(`${plan.preview.method}.`);
  const sp = plan.spectral;
  if (sp.refused) out.push(`Spectral labels: ${sp.refused}`);
  else if (sp.unit && sp.unit_status === "converted") out.push(`Spectral labels in ${sp.unit}${sp.convention ? ` (${sp.convention} convention, ${sp.convention_source === "user" ? "chosen by you; the file declares none" : "the file's"})` : ""}: ${sp.basis ?? ""}${sp.specsys ? ` Reference frame ${sp.specsys}, unchanged.` : " Reference frame (SPECSYS) not declared by the file."}`);
  else if (sp.unit) out.push(`Spectral labels in the file's own unit (${sp.unit}, ${sp.unit_status ?? "declared"}).${sp.specsys ? ` Reference frame ${sp.specsys}.` : ""}`);
  if (o.overlaysOnBase > 0) out.push(`The map's ${o.overlaysOnBase} overlay${o.overlaysOnBase === 1 ? " is" : "s are"} not drawn on the panels.`);
  out.push("No-value pixels (NaN, BLANK) are hatched, never drawn as zero.");
  return out;
}

/**
 * RGBA pixels of a panel painted with the grid's one scale: the same limits, stretch and colour table for every panel, by
 * construction. Row 0 of the data is FITS y = 1, the bottom of the picture, so rows are flipped. A pixel with no value is
 * hatched, never a colour of the scale.
 */
export function paintPixels(data: Float32Array, w: number, h: number, scale: { lo: number; hi: number }, lut: Uint8ClampedArray, stretch: Stretch, nodata: readonly [readonly [number, number, number], readonly [number, number, number]]): Uint8ClampedArray {
  const px = new Uint8ClampedArray(w * h * 4);
  for (let y = 0; y < h; y++) {
    const srcRow = (h - 1 - y) * w;
    for (let x = 0; x < w; x++) {
      const v = data[srcRow + x]!;
      const o = (y * w + x) * 4;
      if (!Number.isFinite(v)) {
        const c = ((x >> 2) + (y >> 2)) & 1 ? nodata[1] : nodata[0];
        px[o] = c[0]; px[o + 1] = c[1]; px[o + 2] = c[2]; px[o + 3] = 255;
        continue;
      }
      const i = Math.round(normalize(v, scale.lo, scale.hi, stretch) * 255) * 3;
      px[o] = lut[i]!; px[o + 1] = lut[i + 1]!; px[o + 2] = lut[i + 2]!; px[o + 3] = 255;
    }
  }
  return px;
}

/** The stored description of a grid (channel-grid/1) and the reviewer-safe view that goes with it, from what is on screen. */
export function gridFigure(o: { datasetId: number; plan: ChannelGridPlan; first: number; last: number; stride: number; average: number; columns: number; unit: string; convention: string; colormap: string; stretch: string; bunit: string | null }):
  { spec: ChannelGridSpec; view: FigureRequest["view"] } | null {
  const lo = o.plan.scale.lo ?? null, hi = o.plan.scale.hi ?? null;
  if (lo === null || hi === null || !(lo < hi)) return null;
  const sp = o.plan.spectral;
  const converted = sp.unit_status === "converted" && sp.unit ? { unit: sp.unit, ...(sp.convention ? { convention: sp.convention as "radio" | "optical" | "relativistic" } : {}) } : null;
  const source = o.plan.scale.source.slice(0, 190);
  const spec: ChannelGridSpec = {
    schema: "channel-grid/1", datasetId: o.datasetId, first: o.first, last: o.last, stride: o.stride, average: o.average, columns: o.columns, panels: o.plan.panel_count,
    ...(converted ? { spectral: converted } : {}),
    display: { colormap: o.colormap as "viridis" | "inferno" | "gray", stretch: o.stretch as "linear" | "sqrt" | "asinh", limits: [lo, hi], ...(o.bunit ? { limitsUnit: o.bunit } : {}), source },
    preview: { step: o.plan.step, panelWidth: o.plan.panel_width, panelHeight: o.plan.panel_height },
  };
  const view: FigureRequest["view"] = {
    kind: "channelgrid", channel: o.first, colormap: o.colormap as "viridis" | "inferno" | "gray", stretch: o.stretch as "linear" | "sqrt" | "asinh", range: "custom", limits: [lo, hi], ...(o.bunit ? { limitsUnit: o.bunit } : {}),
    grid: { first: o.first, last: o.last, stride: o.stride, average: o.average, panels: o.plan.panel_count, previewStep: o.plan.step, ...(converted ? { unit: converted.unit, ...(converted.convention ? { convention: converted.convention } : {}) } : {}), scaleSource: source },
  };
  return { spec, view };
}

const INK = "#e7ecf2";
const INK2 = "#a7b3c1";
const BG = "#0f151c";
const FONT = "'Geist Variable', 'Geist', system-ui, sans-serif";
const MONO = "'Geist Mono Variable', 'Geist Mono', ui-monospace, monospace";

/** The grid as one PNG: title, panels with their labels, the shared colour bar with its limits, and the caption lines. */
export async function composeGridFigure(o: {
  title: string;
  panels: { label: string; source: HTMLCanvasElement | null }[];
  columns: number;
  aspect: number; // panel width / height
  lut: Uint8ClampedArray;
  scale: { lo: number; hi: number };
  unit: string | null;
  captions: string[];
}): Promise<Blob> {
  const pad = 16, gap = 8, W = FIGURE_WIDTH;
  const cols = Math.min(o.columns, o.panels.length);
  const cw = Math.floor((W - 2 * pad - (cols - 1) * gap) / cols);
  const ch = Math.round(cw / o.aspect);
  const rows = Math.ceil(o.panels.length / cols);
  const probe = document.createElement("canvas").getContext("2d")!;
  probe.font = `12px ${FONT}`;
  const cap = o.captions.flatMap((c) => {
    const words = c.split(" ");
    const lines: string[] = [];
    let cur = "";
    for (const w of words) {
      const t = cur ? `${cur} ${w}` : w;
      if (probe.measureText(t).width > W - 2 * pad && cur) { lines.push(cur); cur = w; } else cur = t;
    }
    return cur ? [...lines, cur] : lines;
  });
  const top = pad + 26;
  const gridH = rows * ch + (rows - 1) * gap;
  const barY = top + gridH + 14;
  const H = barY + 40 + cap.length * 17 + pad;
  const c = document.createElement("canvas");
  c.width = W;
  c.height = H;
  const ctx = c.getContext("2d");
  if (!ctx) throw new Error("This browser cannot draw figures.");
  ctx.fillStyle = BG;
  ctx.fillRect(0, 0, W, H);
  ctx.fillStyle = INK;
  ctx.font = `600 16px ${FONT}`;
  ctx.fillText(o.title.slice(0, 120), pad, pad + 14);
  ctx.imageSmoothingEnabled = false;
  o.panels.forEach((p, i) => {
    const x = pad + (i % cols) * (cw + gap), y = top + Math.floor(i / cols) * (ch + gap);
    if (p.source) ctx.drawImage(p.source, x, y, cw, ch);
    else { ctx.fillStyle = "#1b232d"; ctx.fillRect(x, y, cw, ch); }
    ctx.font = `11px ${MONO}`;
    const tw = Math.min(cw - 4, ctx.measureText(p.label).width + 8);
    ctx.fillStyle = "rgba(6,8,12,0.78)";
    ctx.fillRect(x + 2, y + 2, tw, 16);
    ctx.fillStyle = INK;
    ctx.fillText(p.label.length > 60 ? `${p.label.slice(0, 59)}…` : p.label, x + 6, y + 14, cw - 12);
  });
  const bw = W - 2 * pad;
  for (let x = 0; x < bw; x++) {
    const i = Math.round((x / (bw - 1)) * 255) * 3;
    ctx.fillStyle = `rgb(${o.lut[i]},${o.lut[i + 1]},${o.lut[i + 2]})`;
    ctx.fillRect(pad + x, barY, 1, 12);
  }
  ctx.fillStyle = INK2;
  ctx.font = `12px ${MONO}`;
  ctx.fillText(`${trim(o.scale.lo)}${o.unit ? ` ${o.unit}` : ""}`, pad, barY + 28);
  ctx.textAlign = "right";
  ctx.fillText(`${trim(o.scale.hi)}${o.unit ? ` ${o.unit}` : ""}`, pad + bw, barY + 28);
  ctx.textAlign = "left";
  ctx.font = `12px ${FONT}`;
  cap.forEach((l, i) => ctx.fillText(l, pad, barY + 52 + i * 17));
  return new Promise((res, rej) => c.toBlob((b) => (b ? res(b) : rej(new Error("Could not encode the figure."))), "image/png"));
}

export { blobToBase64 };

/** The panels of a plan that are not loaded, as a sentence for the status line. */
export const loadedText = (loaded: number, total: number, pixels: number) => {
  const bytes = pixels * 4;
  return `${loaded} of ${total} panels loaded · ${bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} KiB` : `${(bytes / 1048576).toFixed(1)} MiB`} transferred`;
};

/** The colour table as a CSS gradient, for the legend bar. */
export function rampCss(id: ColormapId): string {
  const t = colorTable(id);
  return `linear-gradient(to right, ${[0, 64, 128, 192, 255].map((k) => `rgb(${t[k * 3]},${t[k * 3 + 1]},${t[k * 3 + 2]}) ${((k / 255) * 100).toFixed(1)}%`).join(", ")})`;
}
