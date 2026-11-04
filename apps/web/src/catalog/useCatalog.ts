import { useCallback, useEffect, useState } from "react";
import { ApiError, listDatasets, registerDataset, setInterpretation, setNature } from "../api/catalog";
import type { Dataset, InterpretationRequest, Nature } from "../api/catalog";
import { listHosts } from "../api/hosts";
import { listJobs } from "../api/jobs";
import type { Job } from "../api/jobs";
import type { Host } from "../api/hosts";
import { importRun, listRuns } from "../api/runs";
import type { Run } from "../api/runs";

export function useCatalog() {
  const [datasets, setDatasets] = useState<Dataset[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    listRuns().then((r) => live && setRuns(r)).catch(() => undefined);
    listHosts().then((h) => live && setHosts(h)).catch(() => undefined);
    listJobs().then((j) => live && setJobs(j)).catch(() => undefined);
    listDatasets()
      .then((d) => live && (setDatasets(d), setError(null)))
      .catch((e: unknown) => live && setError(e instanceof ApiError ? e.message : "Could not load datasets."))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, []);

  /** Returns an error message for the form, or null on success. */
  const register = useCallback(async (path: string): Promise<string | null> => {
    try {
      const d = await registerDataset(path);
      setDatasets((cur) => [...cur.filter((x) => x.id !== d.id), d].sort((a, b) => a.name.localeCompare(b.name)));
      setSelectedId(d.id);
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not register the file.";
    }
  }, []);

  /** Declare (or clear) what the user says a dataset is. Returns an error text with reasons, or null on success. */
  const declare = useCallback(async (id: number, body: InterpretationRequest): Promise<string | null> => {
    try {
      const d = await setInterpretation(id, body);
      setDatasets((cur) => cur.map((x) => (x.id === d.id ? d : x)));
      return null;
    } catch (e) {
      return e instanceof ApiError ? [e.message, ...e.reasons].join(" · ") : "Could not save the interpretation.";
    }
  }, []);

  /** Import a run folder; returns an error text, or null. Refreshes datasets, because the import registers outputs and may declare one. */
  const importRunFolder = useCallback(async (path: string): Promise<string | null> => {
    try {
      const run = await importRun(path);
      setRuns(await listRuns());
      setDatasets(await listDatasets());
      const first = run.outputs.find((o) => o.role === "psi") ?? run.outputs[0];
      if (first) setSelectedId(first.datasetId);
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not import the run.";
    }
  }, []);

  const refreshHosts = useCallback(async () => setHosts(await listHosts()), []);
  const refreshDatasets = useCallback(async () => {
    const d = await listDatasets();
    setDatasets(d);
  }, []);

  const refreshRuns = useCallback(async () => {
    setRuns(await listRuns());
    setJobs(await listJobs());
  }, []);

  const declareNature = useCallback(async (id: number, nature: Nature): Promise<string | null> => {
    try {
      const d = await setNature(id, nature);
      setDatasets((cur) => cur.map((x) => (x.id === d.id ? d : x)));
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not save.";
    }
  }, []);

  return { declareNature, jobs, refreshRuns, hosts, refreshHosts, refreshDatasets, runs, importRunFolder, declare, datasets, selectedId, select: setSelectedId, loading, error, register, selected: datasets.find((d) => d.id === selectedId) ?? null };
}
