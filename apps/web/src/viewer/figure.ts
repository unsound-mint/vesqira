import type { Plane, Spectrum, View } from "../api/planes";
import { spectralText } from "./format";
import { axisDisplay, segments } from "./SpectrumPlot";
import type { RegionShape } from "../api/planes";
import { strokeMarks } from "../scene/overlays";
import type { Mark } from "../scene/overlays";

export type FigureFacts = {
  dataset: string;
  kind: "plane" | "spectrum" | "orientation";
  channelLine: string;
  displayLine: string;
  limits: [number, number] | null;
  limitsUnit: string | null;
  /** The unit is one the author declared because the file has no BUNIT: the picture must say so wherever it states a unit. */
  unitDeclared?: boolean;
};

/** The caption lines printed on a figure. Pure, so what a figure claims can be tested. */
export function captionLines(f: FigureFacts, view: View | null, plane: Plane | null, opts: { colormap: string; stretch: string; range: string; axial: boolean; region: RegionShape | null; nature?: string }): string[] {
  const out = [f.dataset];
  if (plane && view) {
    const s = view.spectral;
    const ch = view.channels > 1 ? `Channel ${plane.head.channel + 1} of ${view.channels}` : "Single plane";
    const v = s && s.resolved && plane.head.spectral_value !== null ? `, ${s.ctype ?? s.name} ${spectralText(plane.head.spectral_value, s.unit)}` : s && !s.resolved ? ", spectral axis unresolved" : "";
    out.push(`${ch}${v}`);
  }
  if (opts.nature === "constructed_control") out.push("Constructed control (synthetic data built for a test), not an observation.");
  else if (opts.nature === "simulated") out.push("Simulated data, not an observation.");
  if (opts.axial) out.push("Axial orientation (0° = 180°); unit declared by the author. Lines from the +x pixel axis, counter-clockwise (the VGT convention, assumed; the file does not declare it).");
  else if (f.kind === "plane") out.push(`${opts.colormap}, ${opts.stretch} stretch, ${opts.range === "percentile" ? "1st to 99th percentile" : opts.range === "custom" ? "limits set by the author" : "minimum to maximum"} of finite pixels${f.limitsUnit ? ` in ${f.limitsUnit}` : ""}${opts.range === "custom" && f.limits ? ` (${f.limits[0]} to ${f.limits[1]})` : ""}`);
  if (opts.region) out.push(`Region: ${opts.region.shape} (mean of finite pixels; NaN, BLANK and infinite values excluded)`);
  if (f.unitDeclared && !opts.axial && f.limitsUnit) out.push(`Brightness unit (${f.limitsUnit}) declared by the author; the file has no BUNIT.`);
  out.push("No-value pixels (NaN, BLANK) are hatched, never drawn as zero. Header values are declared by the file, not verified.");
  return out;
}

const INK = "#e7ecf2";
const INK2 = "#a7b3c1";
const BG = "#0f151c";
const FONT = "'Geist Variable', 'Geist', system-ui, sans-serif";
const MONO = "'Geist Mono Variable', 'Geist Mono', ui-monospace, monospace";

function wrap(ctx: CanvasRenderingContext2D, text: string, maxW: number): string[] {
  const words = text.split(" ");
  const lines: string[] = [];
  let cur = "";
  for (const w of words) {
    const t = cur ? `${cur} ${w}` : w;
    if (ctx.measureText(t).width > maxW && cur) {
      lines.push(cur);
      cur = w;
    } else cur = t;
  }
  if (cur) lines.push(cur);
  return lines;
}

function toBlob(c: HTMLCanvasElement): Promise<Blob> {
  return new Promise((res, rej) => c.toBlob((b) => (b ? res(b) : rej(new Error("Could not encode the figure."))), "image/png"));
}

export const FIGURE_WIDTH = 720;

/** Plane figure: the whole plane as drawn, its overlays (marks from the scene's overlay registry), a legend and the caption. Returns a PNG. */
export async function composeImageFigure(o: {
  source: HTMLCanvasElement;
  planeW: number;
  planeH: number;
  marks: Mark[];
  legend: { kind: "scalar"; lut: Uint8ClampedArray; limits: [number, number] | null; unit: string | null } | { kind: "cyclic"; lut: Uint8ClampedArray };
  title: string;
  captions: string[];
}): Promise<Blob> {
  const pad = 20;
  const W = FIGURE_WIDTH;
  const iw = W - 2 * pad;
  const ih = Math.round((iw * o.planeH) / o.planeW);
  const probe = document.createElement("canvas").getContext("2d")!;
  probe.font = `12px ${FONT}`;
  const capLines = o.captions.flatMap((c) => wrap(probe, c, iw));
  const H = pad + 26 + ih + 14 + 40 + capLines.length * 17 + pad;
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
  const top = pad + 26;
  ctx.imageSmoothingEnabled = false;
  ctx.drawImage(o.source, pad, top, iw, ih);
  ctx.save();
  ctx.translate(pad, top);
  strokeMarks(ctx, o.marks, iw / o.planeW, ih / o.planeH, INK);
  ctx.restore();
  // legend bar
  const by = top + ih + 14;
  for (let x = 0; x < iw; x++) {
    const i = Math.round((x / (iw - 1)) * 255) * 3;
    ctx.fillStyle = `rgb(${o.legend.lut[i]},${o.legend.lut[i + 1]},${o.legend.lut[i + 2]})`;
    ctx.fillRect(pad + x, by, 1, 12);
  }
  ctx.fillStyle = INK2;
  ctx.font = `12px ${MONO}`;
  const ends = o.legend.kind === "cyclic" ? ["0°", "180° (same as 0°)"] : o.legend.limits ? o.legend.limits.map((v) => `${Number(v.toPrecision(5))}${o.legend.kind === "scalar" && o.legend.unit ? ` ${o.legend.unit}` : ""}`) : ["unknown", "unknown"];
  ctx.fillText(ends[0]!, pad, by + 28);
  ctx.textAlign = "right";
  ctx.fillText(ends[1]!, pad + iw, by + 28);
  ctx.textAlign = "left";
  ctx.font = `12px ${FONT}`;
  capLines.forEach((l, i) => ctx.fillText(l, pad, by + 52 + i * 17));
  return toBlob(c);
}

/** Spectrum figure drawn from the data: gaps where the mean is unknown, axis in the unit stated. */
export async function composeSpectrumFigure(o: { spectrum: Spectrum; title: string; captions: string[]; flagged: number[]; channel: number }): Promise<Blob> {
  const W = FIGURE_WIDTH;
  const pad = 20;
  const pw = W - 2 * pad - 40;
  const ph = 300;
  const probe = document.createElement("canvas").getContext("2d")!;
  probe.font = `12px ${FONT}`;
  const capLines = o.captions.flatMap((c) => wrap(probe, c, W - 2 * pad));
  const H = pad + 26 + ph + 50 + capLines.length * 17 + pad;
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
  const { factor, label } = axisDisplay(o.spectrum.head.axis.unit);
  const xs = Array.from(o.spectrum.axis, (a) => a * factor);
  const ys = Array.from(o.spectrum.mean);
  const fin = ys.filter(Number.isFinite);
  const left = pad + 40;
  const top = pad + 30;
  const xlo = Math.min(...xs);
  const xhi = Math.max(...xs);
  const ylo = fin.length ? Math.min(...fin) : 0;
  const yhi = fin.length ? Math.max(...fin) : 1;
  const px = (x: number) => left + (xhi === xlo ? 0.5 : (x - xlo) / (xhi - xlo)) * pw;
  const py = (y: number) => top + ph - (yhi === ylo ? 0.5 : (y - ylo) / (yhi - ylo)) * ph;
  ctx.strokeStyle = "#2f3d4c";
  ctx.strokeRect(left, top, pw, ph);
  ctx.strokeStyle = "#3cc4e8";
  ctx.lineWidth = 2;
  for (const seg of segments(xs.map(px), ys.map(py))) {
    ctx.beginPath();
    seg.forEach((p, i) => {
      const [x, y] = p.split(",").map(Number) as [number, number];
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.stroke();
  }
  ctx.strokeStyle = "#f0b04c";
  for (const f of o.flagged) {
    ctx.beginPath();
    ctx.moveTo(px(xs[f]!), top + ph);
    ctx.lineTo(px(xs[f]!), top + ph + 8);
    ctx.stroke();
  }
  ctx.strokeStyle = INK2;
  ctx.setLineDash([4, 4]);
  ctx.beginPath();
  ctx.moveTo(px(xs[o.channel] ?? xs[0]!), top);
  ctx.lineTo(px(xs[o.channel] ?? xs[0]!), top + ph);
  ctx.stroke();
  ctx.setLineDash([]);
  ctx.fillStyle = INK2;
  ctx.font = `12px ${MONO}`;
  const unit = o.spectrum.head.bunit ?? "brightness unit unresolved";
  ctx.fillText(`${Number(xlo.toPrecision(5))}`, left, top + ph + 22);
  ctx.textAlign = "right";
  ctx.fillText(`${Number(xhi.toPrecision(5))}`, left + pw, top + ph + 22);
  ctx.textAlign = "center";
  ctx.fillText(o.spectrum.head.axis.kind === "spectral" ? `${o.spectrum.head.axis.name ?? "spectral"} (${label})` : "channel number (spectral axis unresolved)", left + pw / 2, top + ph + 40);
  ctx.textAlign = "left";
  ctx.fillText(fin.length ? `${Number(yhi.toPrecision(5))}` : "", pad, top + 12);
  ctx.fillText(fin.length ? `${Number(ylo.toPrecision(5))}` : "", pad, top + ph);
  ctx.font = `12px ${FONT}`;
  ctx.fillStyle = INK2;
  [`Mean over finite pixels in ${unit}; gaps mean no pixel had a value.`, ...capLines].forEach((l, i) => ctx.fillText(l, pad, top + ph + 62 + i * 17));
  return toBlob(c);
}

export const blobToBase64 = (b: Blob) =>
  new Promise<string>((res, rej) => {
    const r = new FileReader();
    r.onload = () => res(String(r.result).split(",")[1] ?? "");
    r.onerror = () => rej(new Error("Could not read the figure."));
    r.readAsDataURL(b);
  });
