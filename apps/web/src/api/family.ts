import { request as call, postJSON as post } from "./http";
import type { components } from "./schema";
import type { Dataset } from "./catalog";

type Schemas = components["schemas"];

/** Maps that differ in one parameter, compared with a chosen reference (ADR 0070). */
export type FamilyRequest = Schemas["FamilyRequest"];
export type FamilySummary = Schemas["FamilySummary"];
export type FamilyMemberStats = Schemas["FamilyMemberStats"];
export type FamilyCandidate = Schemas["FamilyCandidate"];
export type FamilyResult = Omit<Schemas["FamilyResult"], "maps"> & { maps: { role: "spread" | "difference"; member: number; dataset: Dataset }[] };

/** The computation runs on this computer and stops with its request. A refusal (different grids, mixed kinds) comes back as a sentence. */
export const runFamily = (r: FamilyRequest, signal?: AbortSignal, requestId?: string) => post<FamilyResult>("/api/analysis/family", r, signal, requestId);

/** Families recognised from the derived records: results of one source whose options are equal but one. */
export const listFamilyCandidates = (signal?: AbortSignal) => call<{ families: FamilyCandidate[] }>("/api/analysis/families", { signal }).then((r) => r.families);

/** A sweep's finished jobs as a family: one map per job (the run's output of one role), labelled by the parameters that differ, and the jobs left out with the reason. */
export type SweepFamily = Schemas["SweepFamily"];
export const sweepFamily = (sweepId: number, role?: string, signal?: AbortSignal) =>
  call<SweepFamily>(`/api/sweeps/${sweepId}/family${role ? `?role=${encodeURIComponent(role)}` : ""}`, { signal });

/** The comparison and spread maps family runs registered, with what holds each one. */
export type FamilyMapView = Schemas["FamilyMapView"];
export type FamilyMapRemoval = Schemas["FamilyMapRemoval"];
export const listFamilyMaps = (signal?: AbortSignal) => call<{ maps: FamilyMapView[] }>("/api/analysis/family-maps", { signal }).then((r) => (Array.isArray(r?.maps) ? r.maps : []));
/** Removes the maps nothing uses. When every chosen map is kept the call throws an ApiError whose body holds the results with the reasons. */
export const removeFamilyMaps = (datasetIds: number[]) => post<{ results: FamilyMapRemoval[] }>("/api/analysis/family-maps/remove", { datasetIds });
