/** Largest rectangle with the image's aspect ratio that fits the container. Pixels stay square. */
export function fit(cw: number, ch: number, w: number, h: number): { width: number; height: number; scale: number } {
  if (cw <= 0 || ch <= 0 || w <= 0 || h <= 0) return { width: 0, height: 0, scale: 0 };
  const scale = Math.min(cw / w, ch / h);
  return { width: w * scale, height: h * scale, scale };
}

/**
 * Image pixel under a point inside the displayed image rectangle. x counts from the left and y from the
 * bottom (FITS y = 1 is the bottom row), both 0-based. null outside the image.
 */
export function pixelAt(px: number, py: number, dispW: number, dispH: number, w: number, h: number): { x: number; y: number } | null {
  if (dispW <= 0 || dispH <= 0 || px < 0 || py < 0 || px >= dispW || py >= dispH) return null;
  const x = Math.min(w - 1, Math.floor((px / dispW) * w));
  const fromTop = Math.min(h - 1, Math.floor((py / dispH) * h));
  return { x, y: h - 1 - fromTop };
}

/** Channel after a key press, or null when the key does nothing. Arrows step 1, Page keys step 10. */
export function channelForKey(key: string, channel: number, count: number): number | null {
  const clamp = (v: number) => Math.min(count - 1, Math.max(0, v));
  switch (key) {
    case "ArrowRight":
    case "ArrowUp":
      return clamp(channel + 1);
    case "ArrowLeft":
    case "ArrowDown":
      return clamp(channel - 1);
    case "PageUp":
      return clamp(channel + 10);
    case "PageDown":
      return clamp(channel - 10);
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return null;
  }
}
