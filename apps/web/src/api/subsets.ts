import { getJSON, postJSON } from "./http";
import type { components } from "./schema";
import type { Coord, View } from "./planes";

type Schemas = components["schemas"];

/** A virtual subset (ADR 0063): a saved window onto a dataset, never a file. Stale: its source file changed since the version was pinned. */
export type SubsetView = Schemas["SubsetView"];
export type SubsetDefinition = Schemas["SubsetDefinition"];
export type SubsetDetail = Schemas["SubsetDetail"];
export type SubsetInput = Schemas["SubsetInput"];
export type SubsetSaved = Schemas["SubsetSaved"];
export type SubsetMaterialised = Schemas["SubsetMaterialised"];
export type SubsetWindow = Schemas["SubsetWindow"];

export const listSubsets = () => getJSON<Schemas["SubsetList"]>("/api/subsets").then((r) => r.subsets);
export const getSubset = (id: number) => getJSON<SubsetDetail>(`/api/subsets/${id}`);
export const createSubset = (datasetId: number, input: SubsetInput) => postJSON<SubsetSaved>(`/api/datasets/${datasetId}/subsets`, input);
export const editSubset = (id: number, input: SubsetInput) => postJSON<SubsetSaved>(`/api/subsets/${id}`, input);
export const repinSubset = (id: number, reason: string, expectVersion: number) => postJSON<SubsetSaved>(`/api/subsets/${id}/repin`, { reason, expectVersion });
export const materialiseSubset = (id: number, expectVersion: number) => postJSON<SubsetMaterialised>(`/api/subsets/${id}/materialise`, { expectVersion });
export const deleteSubset = (id: number) => postJSON<Schemas["SubsetDeleted"]>(`/api/subsets/${id}/delete`, {});
/** What the window resolved to and the sky and spectral facts of the subset, read through the window from the source file. */
export const getSubsetView = (id: number, signal?: AbortSignal) => getJSON<View>(`/api/subsets/${id}/view`, signal);
export const getSubsetCoord = (id: number, x: number, y: number, channel: number, signal?: AbortSignal) =>
  getJSON<Coord>(`/api/subsets/${id}/coord?x=${x}&y=${y}&channel=${channel}`, signal);
