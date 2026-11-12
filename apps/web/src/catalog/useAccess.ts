import { useCallback, useEffect, useState } from "react";
import { getAccessOverview } from "../api/access";
import type { AccessOverview } from "../api/access";

const NONE: AccessOverview["datasets"] = {};

/** The effective label of every dataset that has one, keyed by dataset id, for the badges of the lists (ADR 0069). */
export function useAccess(): { labels: Record<string, string>; refresh: () => void } {
  const [ov, setOv] = useState<AccessOverview["datasets"]>(NONE);
  const refresh = useCallback(() => {
    getAccessOverview().then((o) => setOv(o.datasets ?? NONE)).catch(() => undefined); // a failed read shows no badge; publishing still asks the server
  }, []);
  useEffect(refresh, [refresh]);
  const labels: Record<string, string> = {};
  for (const [id, v] of Object.entries(ov)) if (v.effective?.label && v.effective.label !== "none") labels[id] = v.effective.label;
  return { labels, refresh };
}
