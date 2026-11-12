import { createContext, useContext } from "react";

/**
 * What the actor may do in the current workspace (ADR 0050 slice 3), from the capabilities answer. Local mode holds everything,
 * so the default lets every action show. The server enforces every permission whatever the page shows; this only hides what
 * would be refused.
 */
export type Can = (permission: string) => boolean;

export const PermissionsContext = createContext<Can>(() => true);

/** Whether the actor holds a permission (for example "board.write"); true in local mode. */
export const useCan = (): Can => useContext(PermissionsContext);
