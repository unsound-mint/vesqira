import type { HealthItem, ProjectListItem, ProjectSummary } from "../api/projects";

export const num = (n: number) => n.toLocaleString("en-US");

/** "~/Projects/VGT" for a folder under a home directory (display only: the full path is always the title). */
export const tilde = (path: string) => path.replace(/^\/home\/[^/]+(?=\/|$)/, "~").replace(/^\/Users\/[^/]+(?=\/|$)/, "~");

/** Where a project is: a folder on this computer, or "<host> · folder". */
export function location(p: Pick<ProjectListItem | ProjectSummary, "hostId" | "path">, hostName: (id: number) => string): string {
  return p.hostId ? `${hostName(p.hostId)} · ${p.path}` : tilde(p.path);
}

/** Health as "k of n": items judged so far; "na" (nothing to judge yet) is not counted. */
export function healthScore(items: HealthItem[]): { ok: number; of: number } {
  const judged = items.filter((i) => i.state !== "na");
  return { ok: judged.filter((i) => i.state === "ok").length, of: judged.length };
}
