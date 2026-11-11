import { postJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];
export type LinkRequest = Schemas["LinkRequest"];
export type LinkReply = Schemas["LinkReply"];

/**
 * What linking dataset `id` (the first map) to `otherId` needs (ADR 0042, section 4): the sky transform over a region of the
 * first map and, with `channels`, its channel correspondence. A pair that cannot be related is a normal answer with `refused`
 * set; only a malformed request or an unreachable worker is an error.
 */
export const postLink = (id: number, body: LinkRequest, signal?: AbortSignal) => postJSON<LinkReply>(`/api/datasets/${id}/link`, body, signal);
