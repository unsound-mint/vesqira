import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError, deleteCatalogDeclaration, getAvailability, isCatalog, listDatasets, putCatalogDeclaration, registerDataset, setInterpretation, setNature, setUnit } from "../api/catalog";
import type { CatalogDataset, CatalogDeclarationRequest, Dataset, ImageDataset, InterpretationRequest, Nature, Problem } from "../api/catalog";
import { listHosts } from "../api/hosts";
import { listJobs } from "../api/jobs";
import type { Job } from "../api/jobs";
import type { Host } from "../api/hosts";
import { importRun, listRuns } from "../api/runs";
import type { Run } from "../api/runs";

export function useCatalog() {
  // Every dataset, archived ones included: an archived one still opens and still resolves for what refers to it. `datasets`
  // below is what lists and pickers offer.
  const [allDatasets, setDatasets] = useState<Dataset[]>([]);
  // The pickers (compare, reliability, analysis) offer images only: a point catalog has no pixel grid.
  const datasets = useMemo(() => allDatasets.filter((d): d is ImageDataset & { archived?: boolean } => !d.archived && !isCatalog(d)), [allDatasets]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [problems, setProblems] = useState<Record<string, Problem>>({});

  /** Which registered files changed or vanished on disk. Checked at start, when the window comes back and after a change here. */
  const checkFiles = useCallback(() => {
    getAvailability().then(setProblems).catch(() => undefined);
  }, []);
  useEffect(() => {
    checkFiles();
    const onVisible = () => document.visibilityState === "visible" && checkFiles();
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [checkFiles]);

  useEffect(() => {
    let live = true;
    listRuns({ archived: "all" }).then((r) => live && setRuns(r)).catch(() => undefined);
    listHosts().then((h) => live && setHosts(h)).catch(() => undefined);
    listJobs().then((j) => live && setJobs(j)).catch(() => undefined);
    listDatasets({ archived: "all" })
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
      checkFiles();
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not register the file.";
    }
  }, [checkFiles]);

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
      setRuns(await listRuns({ archived: "all" }));
      setDatasets(await listDatasets({ archived: "all" }));
      const first = run.outputs.find((o) => o.role === "psi") ?? run.outputs[0];
      if (first) setSelectedId(first.datasetId);
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not import the run.";
    }
  }, []);

  const refreshHosts = useCallback(async () => setHosts(await listHosts()), []);
  const refreshDatasets = useCallback(async () => {
    const d = await listDatasets({ archived: "all" });
    setDatasets(d);
  }, []);

  const refreshRuns = useCallback(async () => {
    setRuns(await listRuns({ archived: "all" }));
    setJobs(await listJobs());
  }, []);

  /** Declare (null withdraws) which columns of a catalog are positions, their frame and an optional angle. Returns an error text with reasons, or null. */
  const declareCatalog = useCallback(async (id: number, body: CatalogDeclarationRequest | null): Promise<string | null> => {
    try {
      const d: CatalogDataset = body === null ? await deleteCatalogDeclaration(id) : await putCatalogDeclaration(id, body);
      setDatasets((cur) => cur.map((x) => (x.id === d.id ? { ...d, archived: x.archived } : x)));
      return null;
    } catch (e) {
      return e instanceof ApiError ? [e.message, ...e.reasons].join(" · ") : "Could not save the declaration.";
    }
  }, []);

  /** Declare or withdraw the unit of a file without BUNIT. Returns an error text, or null on success. */
  const declareUnit = useCallback(async (id: number, unit: string | null): Promise<string | null> => {
    try {
      const d = await setUnit(id, unit === null ? { clear: true } : { unit });
      setDatasets((cur) => cur.map((x) => (x.id === d.id ? d : x)));
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not save the unit.";
    }
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

  const selected = allDatasets.find((d) => d.id === selectedId) ?? null;
  const selectedImage = selected && !isCatalog(selected) ? selected : null;
  const selectedCatalog = selected && isCatalog(selected) ? selected : null;

  return { problems, declareNature, declareCatalog, selectedImage, selectedCatalog, declareUnit, jobs, refreshRuns, hosts, refreshHosts, refreshDatasets, runs, importRunFolder, declare, datasets, allDatasets, selectedId, select: setSelectedId, loading, error, register, selected };
}
