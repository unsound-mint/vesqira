import type { Rgb } from "./render";

/** A #rrggbb design token as numbers, or the fallback when the token is absent or not a plain hex colour. */
export function cssRgb(name: string, fallback: Rgb): Rgb {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const m = /^#([0-9a-f]{6})$/i.exec(raw);
  return m ? [parseInt(m[1]!.slice(0, 2), 16), parseInt(m[1]!.slice(2, 4), 16), parseInt(m[1]!.slice(4, 6), 16)] : fallback;
}

/**
 * The two colours of the hatching that marks a pixel with no value, as the theme gives them. The viewer draws with them and a
 * saved figure records them (scene `render.nodata`), because "draw again" has to reproduce the same hatching in any theme.
 */
export const nodataColours = (): [Rgb, Rgb] => [cssRgb("--nodata-a", [27, 34, 44]), cssRgb("--nodata-b", [85, 98, 117])];
