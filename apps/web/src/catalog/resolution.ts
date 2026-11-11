import type { Dataset } from "../api/catalog";
import { EM } from "./format";

const ARCSEC = 3600;

/** Pixel scale of the first two axes in arcsec, only when both are in degrees (declared by CUNIT). */
export function pixelScale(d: Dataset): string {
  const ax = ("axes" in d.header ? d.header.axes ?? [] : []).slice(0, 2); // a catalog has no pixel grid
  if (ax.length < 2 || ax.some((a) => a.cdelt === null || a.cunit !== "deg")) return EM;
  return `${(Math.abs(ax[0]!.cdelt!) * ARCSEC).toFixed(2)} × ${(Math.abs(ax[1]!.cdelt!) * ARCSEC).toFixed(2)} arcsec`;
}

/** The beam (resolution) when the header declares it. Unresolved means the header does not say; it is never assumed from the pixel scale. */
export function beamText(d: Dataset): string {
  const b = "beam" in d.header ? d.header.beam : undefined;
  if (!b || b.status !== "declared" || b.bmaj === undefined || b.bmin === undefined) return "not declared";
  return `${(b.bmaj * ARCSEC).toFixed(2)} × ${(b.bmin * ARCSEC).toFixed(2)} arcsec${b.bpa !== undefined ? `, position angle ${b.bpa}°` : ""}`;
}
