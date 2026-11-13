import { useCallback, useEffect, useState } from "react";
import type { SkyFrame } from "../api/sky";
import { DEFAULT_SURVEY, SURVEYS } from "./surveys";

/**
 * What the person chose for the sky panel, kept in this browser's own storage (ADR 0072): never sent to the server, never part of
 * a workspace, so a group workspace cannot turn the network background on for anyone. The background defaults to off.
 */
export type SkySettings = { frame: SkyFrame; background: boolean; survey: string };
const KEY = "vesqira.sky.v1";
export const DEFAULTS: SkySettings = { frame: "icrs", background: false, survey: DEFAULT_SURVEY };

/** Reads stored settings defensively: storage may be blocked, empty or hand-edited. Anything unreadable is the default, which is off. */
export function parseSettings(raw: string | null): SkySettings {
  if (!raw) return DEFAULTS;
  try {
    const o = JSON.parse(raw) as Partial<Record<keyof SkySettings, unknown>>;
    return {
      frame: o.frame === "galactic" ? "galactic" : "icrs",
      background: o.background === true,
      survey: typeof o.survey === "string" && SURVEYS.some((s) => s.id === o.survey) ? o.survey : DEFAULT_SURVEY,
    };
  } catch {
    return DEFAULTS;
  }
}

function read(): SkySettings {
  try {
    return parseSettings(localStorage.getItem(KEY));
  } catch {
    return DEFAULTS;
  }
}

export function useSkySettings() {
  const [settings, setSettings] = useState<SkySettings>(read);
  useEffect(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify(settings));
    } catch {
      /* per-browser convenience only */
    }
  }, [settings]);
  const set = useCallback((patch: Partial<SkySettings>) => setSettings((s) => ({ ...s, ...patch })), []);
  return { settings, set };
}
