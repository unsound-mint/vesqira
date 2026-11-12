import { ApiError, getJSON, postJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** Per-dataset access labels (ADR 0069): what a researcher declares about showing a dataset to others. A declaration, not a check of the file. */
export type AccessLabel = Schemas["AccessLabel"];
export type AccessEffective = Schemas["AccessEffective"];
export type AccessSource = Schemas["AccessSource"];
export type DatasetAccess = Schemas["DatasetAccess"];
export type AccessOverview = Schemas["AccessOverview"];
export type AccessRefusal = Schemas["AccessRefusal"];
export type RestrictedPanel = Schemas["RestrictedPanel"];

/** The words for a label. Always shown as text next to an icon, never as colour alone. */
export const ACCESS_NAME: Record<AccessLabel, string> = { none: "Released", collaboration: "Collaboration only", unreleased: "Unreleased" };

export const restricted = (l: AccessLabel | string | undefined): l is "collaboration" | "unreleased" => l === "collaboration" || l === "unreleased";

export const getAccessOverview = () => getJSON<AccessOverview>("/api/access");
export const getDatasetAccess = (id: number) => getJSON<DatasetAccess>(`/api/datasets/${id}/access`);
export const setDatasetAccess = (id: number, label: AccessLabel, reason: string) => postJSON<DatasetAccess>(`/api/datasets/${id}/access`, { label, reason });
export const setBlockedLabels = (workspaceId: number, labels: ("collaboration" | "unreleased")[]) =>
  postJSON<{ blockedLabels: AccessLabel[] }>(`/api/workspaces/${workspaceId}/blocked-labels`, { labels });

/** The refusal a publication gets for its labels: 409 asks for confirmation, 403 is a block the author cannot override. Null for any other error. */
export function accessRefusal(e: unknown): (AccessRefusal & { status: number }) | null {
  if (!(e instanceof ApiError) || (e.status !== 409 && e.status !== 403)) return null;
  const b = e.body as Partial<AccessRefusal> | null;
  return b && (b.code === "access_confirmation_required" || b.code === "publishing_blocked") ? { ...(b as AccessRefusal), status: e.status } : null;
}
