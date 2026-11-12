import { BG, canvasMeasure, FONT, INK, INK2, layoutImageFigure, MASK_ROW, MONO } from "./figure";
import type { ImageFigure, Measure } from "./figure";
import { dashOf, FIGURE_STROKE, HALO, LABEL_PX } from "../scene/overlays";
import { HATCH } from "./maskHatch";
import type { MaskType } from "../api/masks";
import type { Mark } from "../scene/overlays";
import { OVERLAY_COLORS } from "../scene/palette";

/**
 * SVG export of a plane figure (ADR 0042, section 5). It is a second backend of the same drawing as the PNG: the layout is
 * `layoutImageFigure`, the vector layers are the marks the overlay registry produced (the ones `strokeMarks` strokes on the
 * canvas), and the base raster is the plane as drawn, embedded as a PNG. Nothing here knows an overlay kind, so a new kind
 * that has a renderer is exported without touching this file.
 *
 * Each element says what it is, so a count means something: an ink element carries `data-role` (contour, grid, beam,
 * orientation, region, saved, draft, catalog), its dark halo carries only `data-halo`, a filled polygon's fill only
 * `data-fill`. No overlay id, row number or dataset id is written: the file may be passed on.
 */
export type SvgMetadata = {
  title: string;
  /** The author's caption; the caption lines the picture prints follow it in the description. */
  caption: string;
  /** The reviewer-safe provenance document, the one a PNG download carries. */
  provenance: unknown;
  software: string;
};

const NS = "urn:vesqira:figure:1";

/** Removes anything that names a dataset or a path, wherever it sits, so a document that was not built to be safe cannot leak into an export. */
export function withoutOwnerKeys(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(withoutOwnerKeys);
  if (v && typeof v === "object") {
    return Object.fromEntries(Object.entries(v as Record<string, unknown>).filter(([k]) => !/^(datasetId|dataset_id|path|paths)$/i.test(k)).map(([k, x]) => [k, withoutOwnerKeys(x)]));
  }
  return v;
}

// eslint-disable-next-line no-control-regex
const XML_BAD = /[\u0000-\u0008\u000b\u000c\u000e-\u001f￾￿]/g;
export const xmlEscape = (s: string) => s.replace(XML_BAD, "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
const n = (v: number) => String(Math.round(v * 1000) / 1000);

type Line = Extract<Mark, { type: "line" }>;
type Path = Extract<Mark, { type: "path" }>;

function layerSvg(marks: Mark[], sx: number, sy: number): string {
  const out: string[] = [];
  const lines = marks.filter((m): m is Line => m.type === "line");
  const paths = marks.filter((m): m is Path => m.type === "path");
  const d = (p: Path) => {
    let s = "";
    for (let i = 0; i + 1 < p.points.length; i += 2) s += `${i === 0 ? "M" : "L"}${n(p.points[i]! * sx)} ${n(p.points[i + 1]! * sy)}`;
    return p.closed ? `${s}Z` : s;
  };
  for (const p of paths) {
    if (p.fill === undefined) continue;
    out.push(`<path data-fill="true" d="${d(p)}" fill="${OVERLAY_COLORS[p.stroke.color]}" fill-opacity="${n(p.fill)}" stroke="none"/>`);
  }
  for (const halo of [true, false]) {
    for (const g of lines) {
      const w = g.stroke?.width ?? 2;
      const colour = halo ? HALO : g.stroke ? OVERLAY_COLORS[g.stroke.color] : INK;
      out.push(`<line ${halo ? 'data-halo="true"' : 'data-role="orientation"'} x1="${n(g.x1 * sx)}" y1="${n(g.y1 * sy)}" x2="${n(g.x2 * sx)}" y2="${n(g.y2 * sy)}" stroke="${colour}" stroke-width="${n(halo ? w + 2 : w)}" stroke-opacity="${n(g.opacity)}" stroke-linecap="round"/>`);
    }
    for (const p of paths) {
      const w = halo ? p.stroke.width + 2 : p.stroke.width;
      const dash = p.stroke.dashed ? ` stroke-dasharray="${dashOf(p.stroke.width).map(n).join(" ")}"` : "";
      out.push(`<path ${halo ? 'data-halo="true"' : `data-role="${p.role}"`} d="${d(p)}" fill="none" stroke="${halo ? HALO : OVERLAY_COLORS[p.stroke.color]}" stroke-width="${n(w)}" stroke-linecap="round" stroke-linejoin="round"${dash}/>`);
    }
  }
  for (const m of marks) {
    if (m.type !== "rect" && m.type !== "circle") continue;
    const st = FIGURE_STROKE[m.role];
    const style = `fill="none" stroke="${st.color}" stroke-width="${st.width}"${st.dash.length ? ` stroke-dasharray="${st.dash.join(" ")}"` : ""}`;
    out.push(m.type === "rect"
      ? `<rect data-role="${m.role}" x="${n(m.x * sx)}" y="${n(m.y * sy)}" width="${n(m.w * sx)}" height="${n(m.h * sy)}" ${style}/>`
      : `<ellipse data-role="${m.role}" cx="${n(m.cx * sx)}" cy="${n(m.cy * sy)}" rx="${n(m.r * sx)}" ry="${n(m.r * sy)}" ${style}/>`);
  }
  for (const m of marks) {
    if (m.type !== "marker") continue;
    const x = n(m.x * sx), y = n(m.y * sy);
    out.push(`<circle data-halo="true" cx="${x}" cy="${y}" r="${n(m.r + 1)}" fill="none" stroke="${HALO}" stroke-width="${n(m.ringWidth + 2)}"/>`);
    const fill = m.fill === "none" ? "none" : m.fill === "ink" ? OVERLAY_COLORS[m.ring] : m.fill;
    out.push(`<circle data-role="catalog" cx="${x}" cy="${y}" r="${n(m.r)}" fill="${fill}" stroke="${OVERLAY_COLORS[m.ring]}" stroke-width="${n(m.ringWidth)}"/>`);
  }
  for (const m of marks) {
    if (m.type !== "text") continue;
    const pad = 3;
    const x = m.x * sx + (m.anchor === "start" ? pad : m.anchor === "end" ? -pad : 0);
    const y = m.y * sy + (m.baseline === "bottom" ? -pad : m.baseline === "top" ? pad : 0);
    const anchor = m.anchor;
    const baseline = m.baseline === "top" ? "hanging" : m.baseline === "bottom" ? "text-after-edge" : "central";
    out.push(`<text data-role="${m.role}" x="${n(x)}" y="${n(y)}" text-anchor="${anchor}" dominant-baseline="${baseline}" font-family="${xmlEscape(MONO)}" font-size="${LABEL_PX}" fill="${OVERLAY_COLORS[m.color]}" stroke="${HALO}" stroke-width="3" stroke-linejoin="round" paint-order="stroke">${xmlEscape(m.text)}</text>`);
  }
  return out.join("");
}

/** The SVG document of a plane figure. `raster` is the base plane as a PNG data URL (image/png, base64). */
export function figureSvg(o: ImageFigure & { raster: string; meta: SvgMetadata }, measure: Measure = canvasMeasure()): string {
  if (!/^data:image\/png;base64,[A-Za-z0-9+/=]+$/.test(o.raster)) throw new Error("The base raster must be a PNG data URL.");
  const { W, H, pad, iw, ih, top, by, capLines, ends, maskTop } = layoutImageFigure(o, measure);
  const sx = iw / o.planeW, sy = ih / o.planeH;
  const masks = o.masks ?? [];
  const types = [...new Set(masks.map((m) => m.type))];
  // the same 6 x 6 tiles as the viewer and the PNG; only the types in use are defined
  const hatch = (t: MaskType) => {
    const h = HATCH[t];
    return `<pattern id="hatch-${t}" width="6" height="6" patternUnits="userSpaceOnUse">${h.dots ? `<circle cx="3" cy="3" r="1.1" fill="${INK}"/>` : `<path d="${h.tile}" stroke="${INK}" stroke-width="1" fill="none"/>`}</pattern>`;
  };
  const lut = o.legend.lut;
  const stops = Array.from({ length: 65 }, (_, k) => {
    const i = Math.round((k / 64) * 255) * 3;
    return `<stop offset="${n(k / 64)}" stop-color="rgb(${lut[i]},${lut[i + 1]},${lut[i + 2]})"/>`;
  }).join("");
  const desc = [o.meta.caption.trim(), ...capLines].filter((l) => l !== "").join("\n");
  const meta = JSON.stringify({ title: o.meta.title, description: o.meta.caption, provenance: withoutOwnerKeys(o.meta.provenance), software: o.meta.software });
  const text = (x: number, y: number, s: string, extra: string) => `<text x="${n(x)}" y="${n(y)}" ${extra}>${xmlEscape(s)}</text>`;
  return [
    `<?xml version="1.0" encoding="UTF-8"?>`,
    `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-labelledby="figure-title figure-desc">`,
    `<title id="figure-title">${xmlEscape(o.meta.title)}</title>`,
    `<desc id="figure-desc">${xmlEscape(desc)}</desc>`,
    `<metadata><v:figure xmlns:v="${NS}" format="application/json">${xmlEscape(meta).replace(/&quot;/g, '"')}</v:figure></metadata>`,
    `<defs>${types.map(hatch).join("")}<clipPath id="picture"><rect x="0" y="0" width="${n(iw)}" height="${n(ih)}"/></clipPath><linearGradient id="scale" x1="0" y1="0" x2="1" y2="0">${stops}</linearGradient></defs>`,
    `<rect width="${W}" height="${H}" fill="${BG}"/>`,
    text(pad, pad + 14, o.title.slice(0, 120), `font-family="${xmlEscape(FONT)}" font-size="16" font-weight="600" fill="${INK}"`),
    `<image x="${pad}" y="${top}" width="${n(iw)}" height="${n(ih)}" preserveAspectRatio="none" style="image-rendering:pixelated" image-rendering="optimizeSpeed" xlink:href="${o.raster}"/>`,
    `<g transform="translate(${pad} ${top})" clip-path="url(#picture)">${o.layers.map((l, i) => `<g data-layer="${i + 1}">${layerSvg(l, sx, sy)}</g>`).join("")}</g>`,
    ...(masks.length ? [`<g transform="translate(${pad} ${top})" clip-path="url(#picture)" data-layer="masks">${masks.map((m) => (m.d ? `<path data-mask-type="${m.type}" d="${m.d}" fill="url(#hatch-${m.type})"/>` : "")).join("")}</g>`] : []),
    ...masks.map((m, i) => `<g data-mask-legend="${m.type}"><rect x="${pad}" y="${n(maskTop + i * MASK_ROW)}" width="18" height="12" fill="url(#hatch-${m.type})" stroke="${INK2}" stroke-width="1"/>${text(pad + 26, maskTop + i * MASK_ROW + 10, m.label, `font-family="${xmlEscape(FONT)}" font-size="12" fill="${INK2}"`)}</g>`),
    `<rect x="${pad}" y="${by}" width="${n(iw)}" height="12" fill="url(#scale)"/>`,
    text(pad, by + 28, ends[0], `font-family="${xmlEscape(MONO)}" font-size="12" fill="${INK2}"`),
    text(pad + iw, by + 28, ends[1], `font-family="${xmlEscape(MONO)}" font-size="12" fill="${INK2}" text-anchor="end"`),
    ...capLines.map((l, i) => text(pad, by + 52 + i * 17, l, `font-family="${xmlEscape(FONT)}" font-size="12" fill="${INK2}"`)),
    `</svg>`,
  ].join("\n");
}
