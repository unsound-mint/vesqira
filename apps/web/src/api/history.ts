import { getJSON, postJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** One meaningful state of the viewer (ADR 0055): a scene, not pixels. Mutable working state, pruned oldest first. */
export type ExplorationStep = Schemas["ExplorationStep"];
export type ExplorationStepWithScene = Schemas["ExplorationStepWithScene"];
export type StepKind = ExplorationStep["kind"];

/** A project's steps, oldest first, without scenes; projectId 0 is "no project open". */
export const listHistory = (projectId: number, signal?: AbortSignal) => getJSON<Schemas["ExplorationHistory"]>(`/api/history?project=${projectId}`, signal);
export const getStep = (id: number) => getJSON<ExplorationStepWithScene>(`/api/history/${id}`);
export const addStep = (r: Schemas["AddExplorationStepRequest"]) => postJSON<ExplorationStep>("/api/history", r);
