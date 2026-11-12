import { request as call, postJSON as post } from "./http";
import type { components } from "./schema";

/** A saved view of the runs table: a name for a query, the exposed columns, the sort and the plot's axes. Words and keys only. */
export type RunView = components["schemas"]["RunView"];
export type RunViewSpec = components["schemas"]["RunViewSpec"];

export const listRunViews = () => call<components["schemas"]["RunViewList"]>("/api/run-views");
export const saveRunView = (name: string, spec: RunViewSpec) => post<RunView>("/api/run-views", { name, spec });
export const deleteRunView = (id: number) => post<components["schemas"]["RunViewDeleted"]>(`/api/run-views/${id}/delete`, {});
