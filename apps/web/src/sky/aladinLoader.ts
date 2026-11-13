/**
 * Aladin Lite (CDS, https://github.com/cds-astro/aladin-lite), pinned to 3.8.2 in package.json, is the HiPS renderer of the sky panel
 * (ADR 0072). It is part of this app's own build and is split into its own chunk that this dynamic import fetches from the app's own
 * origin, and only after the person has turned the background on: no script is loaded from a CDN, and the 2.4 MB chunk is not
 * downloaded at all while the background is off. Aladin Lite makes network requests itself once it runs, to the HiPS server it is
 * given (see surveys.ts).
 */
export type AladinShape = unknown;
export type AladinOverlay = { add(s: AladinShape): void; removeAll(): void; show(): void };
export type AladinInstance = {
  gotoRaDec(ra: number, dec: number): void;
  setFoV(deg: number): void;
  getFoV(): [number, number];
  getRaDec(): [number, number];
  setBaseImageLayer(url: string): void;
  setFrame?(frame: string): void;
  addOverlay(o: AladinOverlay): void;
  on(event: string, cb: (e: { ra?: number; dec?: number; isDragging?: boolean }) => void): void;
  getViewDataURL?(opts?: { format?: string; width?: number; height?: number; logo?: boolean }): Promise<string> | string;
  getSize?(): [number, number];
  removeLayers?(): void;
};
export type AladinNs = {
  aladin(el: HTMLElement, opts: Record<string, unknown>): AladinInstance;
  graphicOverlay(opts: Record<string, unknown>): AladinOverlay;
  polygon(vertices: [number, number][], opts?: Record<string, unknown>): AladinShape;
  circle(ra: number, dec: number, radiusDeg: number, opts?: Record<string, unknown>): AladinShape;
  init: Promise<void>;
};

export async function loadAladin(): Promise<AladinNs> {
  const m = await import("aladin-lite");
  const A = m.default as AladinNs;
  await A.init; // rejects when the browser has no WebGL2
  return A;
}
