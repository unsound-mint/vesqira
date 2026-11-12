import { createElement } from "react";
import type { RohsaSigmav } from "../api/rohsa";
import { resolveVars, svgWithProvenance } from "../catalog/runExport";
import { viewBoxOf } from "../runs/runPicture";
import { SigmavPlot, footerLines } from "./SigmavPlot";
import { provenanceOf } from "./sigmav";
import type { CellRef } from "./SigmavPlot";

/**
 * The diagram as a self-contained SVG document with its provenance inside: drawn by the component the page shows, plus the lines that
 * state the bins, the counts, the fit and the cut. The page's colour tokens are replaced by their values; `now` is passed in so the
 * same inputs give the same file.
 */
export async function sigmavSvg(d: RohsaSigmav, run: string, cut: number | null, selected: CellRef | null, now: string): Promise<string> {
  const { renderToStaticMarkup } = await import("react-dom/server");
  let raw = renderToStaticMarkup(createElement(SigmavPlot, { d, run, cut, cursor: null, selected, footer: footerLines(d, cut) }));
  const box = viewBoxOf(raw);
  raw = raw.replace(/ tabindex="0"/, "").replace(/ data-[a-z-]+="[^"]*"/g, "").replace('width="100%"', `width="${box.width}" height="${box.height}"`);
  if (!/^<svg[^>]*\sxmlns="/.test(raw)) raw = raw.replace("<svg", '<svg xmlns="http://www.w3.org/2000/svg"');
  const css = getComputedStyle(document.documentElement);
  return svgWithProvenance(resolveVars(raw, (n) => css.getPropertyValue(n).trim()), provenanceOf(d, run, cut, now));
}
