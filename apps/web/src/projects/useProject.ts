import { useCallback, useEffect, useRef, useState } from "react";
import { cancelScan, getProject, listProjects, openProject, rescanProject } from "../api/projects";
import type { ProjectListItem, ProjectSummary } from "../api/projects";

const LAST = "vesqira.project";
/** The window returning to the front rescans, but not more often than this. */
export const RESCAN_AFTER_MS = 30_000;
const POLL_MS = 1000;

const readLast = (): number | null => {
  try {
    const n = Number(localStorage.getItem(LAST));
    return Number.isInteger(n) && n > 0 ? n : null;
  } catch {
    return null;
  }
};
const writeLast = (id: number | null) => {
  try {
    if (id === null) localStorage.removeItem(LAST);
    else localStorage.setItem(LAST, String(id));
  } catch {
    /* private window: the project just does not reopen next time */
  }
};

export const isScanning = (p: ProjectSummary | null) => p?.scan.state === "scanning" || p?.scan.state === "registering";

/**
 * The open project: its summary, kept fresh while a scan runs (polled every second, and only then), rescanned when the
 * window comes back, and remembered so it reopens on start. `version` changes whenever a scan finishes: lists keyed on it reload.
 */
export function useProject(onScanned: () => void) {
  const [project, setProject] = useState<ProjectSummary | null>(null);
  const [recent, setRecent] = useState<ProjectListItem[]>([]);
  const [opening, setOpening] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const lastScan = useRef(0);
  const scanned = useRef(onScanned);
  scanned.current = onScanned;

  const refreshRecent = useCallback(() => {
    listProjects().then((r) => setRecent(Array.isArray(r) ? r : [])).catch(() => undefined);
  }, []);

  const refresh = useCallback(async (id: number) => {
    try {
      const p = await getProject(id);
      setProject((cur) => (cur && cur.id !== id ? cur : p)); // a late answer for a project that was closed meanwhile is dropped
    } catch {
      /* the next poll or focus tries again */
    }
  }, []);

  const rescan = useCallback(async (id: number) => {
    lastScan.current = Date.now();
    try {
      const r = await rescanProject(id);
      if (r.started) setProject((cur) => (cur && cur.id === id ? { ...cur, scan: r.scan } : cur));
    } catch {
      /* offline or the project was removed: nothing to show */
    }
  }, []);

  // start: the recent list, and the project that was open last time (rescanned, because the folder may have changed)
  useEffect(() => {
    refreshRecent();
    const id = readLast();
    if (id === null) return;
    let live = true;
    getProject(id)
      .then((p) => {
        if (!live) return;
        setProject(p);
        void rescan(id);
      })
      .catch(() => writeLast(null));
    return () => {
      live = false;
    };
  }, [refreshRecent, rescan]);

  const pid = project?.id ?? null;
  const active = isScanning(project);
  useEffect(() => {
    if (!active || pid === null) return;
    const t = setInterval(() => void refresh(pid), POLL_MS);
    return () => clearInterval(t);
  }, [active, pid, refresh]);

  const was = useRef(false);
  useEffect(() => {
    if (was.current && !active) {
      setVersion((v) => v + 1);
      scanned.current();
    }
    was.current = active;
  }, [active]);

  useEffect(() => {
    if (pid === null) return;
    const back = () => {
      if (document.visibilityState === "hidden" || Date.now() - lastScan.current < RESCAN_AFTER_MS) return;
      void rescan(pid);
    };
    window.addEventListener("focus", back);
    document.addEventListener("visibilitychange", back);
    return () => {
      window.removeEventListener("focus", back);
      document.removeEventListener("visibilitychange", back);
    };
  }, [pid, rescan]);

  /** Opens a folder (hostId 0: this computer). Returns the summary, or null with `error` set. */
  const open = useCallback(
    async (path: string, hostId: number): Promise<ProjectSummary | null> => {
      setOpening(true);
      setError(null);
      try {
        const p = await openProject(path, hostId);
        lastScan.current = Date.now();
        setProject(p);
        writeLast(p.id);
        refreshRecent();
        return p;
      } catch (e) {
        setError(e instanceof Error ? e.message : "Could not open the folder.");
        return null;
      } finally {
        setOpening(false);
      }
    },
    [refreshRecent],
  );

  const cancel = useCallback(async () => {
    if (pid === null) return;
    try {
      await cancelScan(pid);
    } catch {
      /* it may have finished meanwhile */
    }
    await refresh(pid);
  }, [pid, refresh]);

  return { project, recent, opening, error, version, open, refresh, rescan, cancel };
}
