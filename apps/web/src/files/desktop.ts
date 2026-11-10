type Internals = { invoke: (cmd: string, args?: unknown) => Promise<unknown> };

/** True inside the native window; the same web app runs in a browser without it (ADR 0037). */
export const hasNativeDialogs = () => typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;

/** Opens the system folder chooser. Resolves to the chosen absolute path, or null when cancelled. */
export async function pickFolder(): Promise<string | null> {
  const t = (window as unknown as { __TAURI_INTERNALS__?: Internals }).__TAURI_INTERNALS__;
  if (!t) return null;
  const r = await t.invoke("pick_folder");
  return typeof r === "string" ? r : null;
}
