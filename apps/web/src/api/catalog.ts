import { ApiError, request as call } from "./http";

/** Where callers have always imported it from. */
export { ApiError } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

export type Status = Schemas["Axis"]["ctype_status"];
export type Axis = Schemas["Axis"];
export type Header = Schemas["FitsHeader"];
export type Interpretation = Schemas["Interpretation"];
export type CatalogHeader = Schemas["CatalogHeader"];
export type CatalogColumn = Schemas["CatalogColumn"];
export type CatalogDeclaration = Schemas["CatalogDeclaration"];
export type CatalogDeclarationRequest = Schemas["CatalogDeclarationRequest"];
export type CatalogReport = Schemas["CatalogReport"];
export type CatalogRows = Schemas["CatalogRows"];
export type CatalogPositions = Schemas["CatalogPositions"];
export type DatasetLocation = Schemas["DatasetLocation"];
export type LocationList = Schemas["LocationList"];
export type NewerNotice = Schemas["NewerNotice"];
export type NewerResults = Pick<Schemas["NewerResults"], "datasets" | "figures">;
export type SameContentMatch = Schemas["SameContentMatch"];
/** `locations` is optional here so that a dataset built without it (tests, older answers) is one with only its registered file. */
type DatasetBase = Omit<Schemas["Dataset"], "header" | "kind" | "catalogDeclaration" | "locations"> & { locations?: DatasetLocation[] };
/** An image or cube. `kind` is optional so that a dataset built without it (tests, older answers) is an image, as before. */
export type ImageDataset = DatasetBase & { kind?: "fits"; header: Header; catalogDeclaration?: null };
/** A table of sources (ADR 0044): no planes, no pixel grid. */
export type CatalogDataset = DatasetBase & { kind: "catalog"; header: CatalogHeader; catalogDeclaration: CatalogDeclaration | null };
/** `archived` is set by the server on every dataset it sends (ADR 0041); the generated type predates it. Narrow on `kind`. */
export type Dataset = (ImageDataset | CatalogDataset) & { archived?: boolean };
export const isCatalog = (d: Dataset): d is CatalogDataset & { archived?: boolean } => d.kind === "catalog";

/** Files on this computer that are no longer what was registered. Remote files are not checked here. */
export type Problem = "modified" | "missing" | "not_allowed";
export const getAvailability = () => call<{ problems: Record<string, Problem> }>("/api/datasets/availability").then((r) => r.problems ?? {});
/** Without `archived` the server hides archived datasets; "all" is for the explorer, which filters them itself. */
export const listDatasets = (opts: { archived?: "all" | "only" } = {}) => call<{ datasets: Dataset[] }>(`/api/datasets${opts.archived ? `?archived=${opts.archived}` : ""}`).then((r) => r.datasets);
export const headerCards = (id: number) => call<{ cards: string[]; truncated: boolean }>(`/api/datasets/${id}/header-cards`);
export const listRoots = () => call<{ roots: string[] }>("/api/roots").then((r) => r.roots);
/** `asNew` registers the file as its own dataset although a dataset already holds probably or verifiably the same content. */
export const registerDataset = (path: string, opts: { asNew?: boolean } = {}) =>
  call<Dataset>("/api/datasets", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(opts.asNew ? { path, asNew: true } : { path }) });

/** The offer a 409 `same_content` answer carries: datasets that already hold the content of the file being registered. */
export function sameContentOffer(e: unknown): SameContentMatch[] | null {
  if (!(e instanceof ApiError) || e.status !== 409) return null;
  const b = e.body as { code?: unknown; matches?: unknown } | null;
  return b?.code === "same_content" && Array.isArray(b.matches) ? (b.matches as SameContentMatch[]) : null;
}

/** Where a dataset's bytes are and what is known about them (ADR 0053). */
export const listLocations = (id: number) => call<LocationList>(`/api/datasets/${id}/locations`);
export const addLocation = (id: number, body: { path: string; hostId?: number; declare?: boolean }) =>
  call<DatasetLocation>(`/api/datasets/${id}/locations`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
export const removeLocation = (id: number, locationId: number) =>
  call<{ removed: number }>(`/api/datasets/${id}/locations/${locationId}/delete`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
export const fingerprintLocation = (id: number, locationId: number, mode: "sampled" | "full" | "cancel") =>
  call<unknown>(`/api/datasets/${id}/locations/${locationId}/fingerprint`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
/** Notices that a newer result exists or that data changed; nothing is swapped or redrawn. */
export const getNewer = () => call<NewerResults>("/api/datasets/newer");

/** Register a file as a point catalog whatever its name (a FITS table is called .fits). */
export const registerCatalog = (path: string) =>
  call<CatalogDataset>("/api/catalogs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });
export const catalogReport = (id: number) => call<CatalogReport>(`/api/datasets/${id}/catalog/report`);
export const catalogRows = (id: number, offset: number, limit: number) => call<CatalogRows>(`/api/datasets/${id}/catalog/rows?offset=${offset}&limit=${limit}`);
export const putCatalogDeclaration = (id: number, body: CatalogDeclarationRequest) =>
  call<CatalogDataset>(`/api/datasets/${id}/catalog/declaration`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
export const deleteCatalogDeclaration = (id: number) => call<CatalogDataset>(`/api/datasets/${id}/catalog/declaration`, { method: "DELETE" });

export type Compat = Schemas["Compat"];
export const getCompat = (a: number, b: number) => call<Compat>(`/api/compat?a=${a}&b=${b}`);

export type AngleFrame = NonNullable<Interpretation["frame"]>;
export type AngleQuantity = NonNullable<Interpretation["quantity"]>;
/** frame and quantity are null when the person has not declared them: the file never says. */
export type InterpretationRequest =
  | { kind: "none" }
  | { kind: "axial_orientation"; unit: "deg" | "rad"; reliabilityId: number | null; frame: AngleFrame | null; quantity: AngleQuantity | null };
export const setInterpretation = (id: number, body: InterpretationRequest) =>
  call<Dataset>(`/api/datasets/${id}/interpretation`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

/** Declare (or withdraw) the unit of a file whose header has no BUNIT. The file and its header facts are not changed. */
export const setUnit = (id: number, body: { unit: string } | { clear: true }) =>
  call<Dataset>(`/api/datasets/${id}/unit`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

export type Nature = Dataset["nature"] | "none";
export const setNature = (id: number, nature: Nature) =>
  call<Dataset>(`/api/datasets/${id}/nature`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ nature }) });

export type Preview = { name: string; sizeBytes: number; axes: number; planes: number; planeBytes: number; celestialWcs: boolean; spectralAxis: boolean; access: string };
export const previewFile = (path: string) => call<Preview>(`/api/preview?path=${encodeURIComponent(path)}`);
export const copyDataset = (id: number) =>
  call<{ dataset: Dataset; sha256: string }>(`/api/datasets/${id}/copy`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
