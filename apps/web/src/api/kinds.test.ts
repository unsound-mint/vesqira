import { describe, expect, it } from "vitest";
import { isCatalog, isHealpix, isImage, type Dataset } from "./catalog";
import { imageDatasets } from "../scene/overlayForm";

const base = { id: 1, path: "/d/x.fits", name: "x.fits", sizeBytes: 1, modifiedAt: "", addedAt: "", hostId: 0, nature: "", interpretation: null, unitDeclaration: null } as const;

// A HEALPix map (ADR 0067) has no pixel grid: it must never be offered where an image is expected (viewer, overlays, masks, comparisons).
describe("dataset kinds", () => {
  const healpix = { ...base, id: 3, kind: "healpix", header: { kind: "healpix", nside: 16, npix: 3072, ordering: "RING", coordsys: "GALACTIC", frame: "galactic", polcconv: "COSMO", columns: [], stokes_unit: "K_CMB", covariance_unit: null, has_qu_cov: true, has_i: true, bad_data: null, pixel_resolution_arcmin: 220, provenance_note: "" } } as unknown as Dataset;
  const image = { ...base, id: 1, header: { naxis: 2, bitpix: -32, axes: [] } } as unknown as Dataset;
  const table = { ...base, id: 2, kind: "catalog", header: { kind: "catalog", format: "csv", rows: 3, columns: [] }, catalogDeclaration: null } as unknown as Dataset;

  it("tells an image from a catalog and a HEALPix map", () => {
    expect([image, table, healpix].map(isImage)).toEqual([true, false, false]);
    expect([image, table, healpix].map(isCatalog)).toEqual([false, true, false]);
    expect([image, table, healpix].map(isHealpix)).toEqual([false, false, true]);
  });

  it("keeps HEALPix maps out of the image lists", () => {
    expect(imageDatasets([image, table, healpix]).map((d) => d.id)).toEqual([1]);
  });
});
