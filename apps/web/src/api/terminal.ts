import type { components } from "./schema";
import { getJSON, postJSON } from "./http";

export type TerminalSession = components["schemas"]["TerminalSession"];

/** Whether the server was started with --enable-terminal (ADR 0029). Any failure to find out means off. */
export async function terminalEnabled(): Promise<boolean> {
  try {
    const body = await getJSON<{ features?: Record<string, boolean> } | null>("/api/capabilities");
    return body?.features?.terminal === true;
  } catch {
    return false;
  }
}

/** One id per browser tab (per page load): the server ends a tab's earlier session when it opens a new one. */
const TAB = (() => {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
})();

/** Asks for a session on this computer (hostId 0) or on a registered host. The token comes back once and is good for one connection. */
export const openTerminalSession = (hostId: number) => postJSON<TerminalSession>("/api/terminal/sessions", { tab: TAB, hostId });

/**
 * Opens the socket. The token travels only as a WebSocket subprotocol (never in the URL, which would be kept in
 * server logs and browser history); the server selects the first protocol and never echoes the token.
 */
export function connectTerminal(session: TerminalSession): WebSocket {
  const url = `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/api/terminal`;
  const ws = new WebSocket(url, [session.protocol, `vesqira-token.${session.token}`]);
  ws.binaryType = "arraybuffer";
  return ws;
}

/** What the server says when it ends a session. */
export type TerminalExit = { reason: string; code: number | null };

export function parseExit(text: string): TerminalExit | null {
  try {
    const m = JSON.parse(text) as { type?: string; reason?: unknown; code?: unknown };
    if (m.type !== "exit") return null;
    return { reason: typeof m.reason === "string" ? m.reason : "ended", code: typeof m.code === "number" ? m.code : null };
  } catch {
    return null;
  }
}

/** The page-side bounds, equal to the server's; a size outside them would close the session. */
export const clampSize = (cols: number, rows: number) => ({ cols: Math.min(500, Math.max(1, Math.floor(cols))), rows: Math.min(200, Math.max(1, Math.floor(rows))) });
