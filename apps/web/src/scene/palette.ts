/**
 * Overlay stroke colours (scene/1 `color`). The image canvas is dark in both themes (--canvas), so these are light inks that
 * read on it whatever the theme. The same values are the --overlay-* tokens in design/tokens.css (a test keeps them equal):
 * the viewer strokes with the tokens, a figure (a canvas, no CSS) with these values.
 */
export const OVERLAY_COLORS = {
  white: "#e7ecf2",
  cyan: "#3cc4e8",
  amber: "#f0b04c",
  magenta: "#f07ad8",
  lime: "#9be36a",
} as const;
export type OverlayColor = keyof typeof OVERLAY_COLORS;
export const OVERLAY_COLOR_NAMES = Object.keys(OVERLAY_COLORS) as OverlayColor[];

/** Default stroke widths in output pixels, by kind, when the overlay sets none. */
export const DEFAULT_WIDTH: Record<"contours" | "orientation" | "beam" | "grid", number> = { contours: 1.25, orientation: 2, beam: 1.5, grid: 0.75 };
