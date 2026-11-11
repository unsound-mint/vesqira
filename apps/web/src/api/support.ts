import { postJSON } from "./http";
import type { components } from "./schema";

export type SupportPreview = components["schemas"]["SupportPreview"];
export type SupportFile = components["schemas"]["SupportFile"];

/** Builds the bundle in memory on the server; nothing is saved until the person follows the download link. */
export const previewSupport = (includeDatasetNames: boolean) => postJSON<SupportPreview>("/api/support/preview", { includeDatasetNames });

/** The exact previewed files as a zip; it expires ten minutes after the preview (404 after that). */
export const supportBundleUrl = (id: string) => `/api/support/bundle/${encodeURIComponent(id)}`;
