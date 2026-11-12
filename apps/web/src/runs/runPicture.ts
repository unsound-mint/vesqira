import { createElement } from "react";
import { resolveVars } from "../catalog/runExport";
import type { PlotModel } from "../catalog/runPlot";
import { RunsPlot } from "./RunsPlot";

/** The drawing's own size in user units, from its viewBox attribute. */
export function viewBoxOf(svg: string): { width: number; height: number } {
  const m = /viewBox="([^"]*)"/.exec(svg);
  const [, , width = 720, height = 400] = (m?.[1] ?? "").split(/\s+/).map(Number);
  return { width, height };
}

/**
 * The plot as a self-contained SVG document, drawn by the same component the page shows, with no selection and nothing that depends
 * on the time or the page: the same runs give the same picture, so "Draw again" can say whether anything changed. The page's colour
 * tokens are replaced by their values.
 */
export async function plotSvg(model: PlotModel, footer: string): Promise<string> {
  const { renderToStaticMarkup } = await import("react-dom/server");
  let raw = renderToStaticMarkup(createElement(RunsPlot, { model, selected: null, onSelect: () => undefined, onOpen: () => undefined, footer }));
  const box = viewBoxOf(raw);
  raw = raw.replace(/ tabindex="0"/, "").replace(/ data-[a-z-]+="[^"]*"/g, "").replace('width="100%"', `width="${box.width}" height="${box.height}"`);
  if (!/^<svg[^>]*\sxmlns="/.test(raw)) raw = raw.replace("<svg", '<svg xmlns="http://www.w3.org/2000/svg"');
  const css = getComputedStyle(document.documentElement);
  return resolveVars(raw, (n) => css.getPropertyValue(n).trim());
}

/** The SVG rasterised at twice its size. Throws an Error with a sentence when the browser cannot draw it. */
export async function svgToPng(svg: string): Promise<Blob> {
  const box = viewBoxOf(svg);
  const img = new Image(box.width, box.height);
  img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
  await img.decode();
  const scale = 2;
  const canvas = document.createElement("canvas");
  canvas.width = box.width * scale;
  canvas.height = box.height * scale;
  const g = canvas.getContext("2d");
  if (!g) throw new Error("The browser has no canvas to draw the picture on.");
  g.drawImage(img, 0, 0, canvas.width, canvas.height);
  const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, "image/png"));
  if (!blob) throw new Error("The browser could not make a PNG of the plot.");
  return blob;
}

/** The picture a saved figure holds: the plot as a PNG. */
export async function plotPng(model: PlotModel, footer: string): Promise<Blob> {
  return svgToPng(await plotSvg(model, footer));
}
