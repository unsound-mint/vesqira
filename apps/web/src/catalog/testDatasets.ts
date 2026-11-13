import type { DatasetFacts } from "../api/catalog";

/** What the server reads into `facts` from a stored header (services/api/internal/catalog/summary.go), for the datasets tests build. */
type LooseHeader = {
  kind?: string; naxis?: number; axes?: { length: number; ctype?: string | null }[]; bunit?: string | null; bunit_status?: string;
  rows?: number; nside?: number; columns?: { name: string; dtype: string; unit?: string | null; scalar: boolean }[];
} | undefined;

export function factsOfHeader(h: LooseHeader, kind: string = h?.kind ?? "fits"): DatasetFacts {
  const f: DatasetFacts = { naxis: null, channels: null, width: null, height: null, ctypes: [], bunit: null, bunitStated: false, rows: null, nside: null, numericColumns: [] };
  if (kind === "catalog") {
    f.rows = h?.rows ?? null;
    f.numericColumns = (h?.columns ?? []).filter((c) => c.scalar && (c.dtype === "float" || c.dtype === "int")).map((c) => ({ name: c.name, unit: c.unit ?? null }));
  } else if (kind === "healpix") {
    f.nside = h?.nside ?? null;
  } else {
    const axes = h?.axes ?? [];
    f.naxis = h?.naxis ?? null;
    f.channels = axes.length > 2 && axes[2]!.length > 1 ? axes[2]!.length : 1;
    f.ctypes = axes.slice(0, 2).map((a) => a.ctype ?? "");
    if (axes.length >= 2) [f.width, f.height] = [axes[0]!.length, axes[1]!.length];
    f.bunit = h?.bunit ?? null;
    f.bunitStated = h?.bunit_status === "declared" || (h?.bunit ?? null) !== null;
  }
  return f;
}

/** A test dataset with the `facts` and `locationCount` the server would have sent beside its header. */
export function withFacts<T extends { header?: unknown; kind?: string }>(d: T): T & { facts: DatasetFacts; locationCount: number } {
  return { locationCount: 1, ...d, facts: factsOfHeader(d.header as LooseHeader, d.kind) };
}
