import { useCallback, useEffect, useMemo, useState } from "react";
import { addLocation, ApiError, applyDeclaration, previewApply, undoApply, deleteCatalogDeclaration, getAvailability, getNewer, isCatalog, isHealpix, isImage, listDatasets, putCatalogDeclaration, registerDataset, sameContentOffer, setInterpretation, setNature, setUnit } from "../api/catalog";
import type { ApplyScopeName, CatalogDataset, CatalogDeclarationRequest, Dataset, ImageDataset, InterpretationRequest, Nature, NewerResults, Problem } from "../api/catalog";
import type { Offer } from "./SameContentOffer";
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
  const datasets = useMemo(() => allDatasets.filter((d): d is ImageDataset & { archived?: boolean } => !d.archived && isImage(d)), [allDatasets]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [problems, setProblems] = useState<Record<string, Problem>>({});
  /** Notices that a newer result exists or that the data behind a dataset or figure changed (ADR 0053), checked with the files. */
  const [newer, setNewer] = useState<NewerResults>({ datasets: {}, figures: {} });
  /** A file whose content a registered dataset already holds: the choice offered instead of registering it. */
  const [offer, setOffer] = useState<Offer | null>(null);

  /** Which registered files changed or vanished on disk. Checked at start, when the window comes back and after a change here. */
  const checkFiles = useCallback(() => {
    getAvailability().then(setProblems).catch(() => undefined);
    getNewer().then(setNewer).catch(() => undefined);
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
      // The list starts empty, so anything already in it was registered while this first read was in flight: keep it.
      .then((d) => live && (setDatasets((cur) => [...d, ...cur.filter((x) => !d.some((y) => y.id === x.id))].sort((a, b) => a.name.localeCompare(b.name))), setError(null)))
      .catch((e: unknown) => live && setError(e instanceof ApiError ? e.message : "Could not load datasets."))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, []);

  /** Returns an error message for the form, or null on success. */
  const register = useCallback(async (path: string, opts: { asNew?: boolean } = {}): Promise<string | null> => {
    try {
      const d = await registerDataset(path, opts);
      setDatasets((cur) => [...cur.filter((x) => x.id !== d.id), d].sort((a, b) => a.name.localeCompare(b.name)));
      setSelectedId(d.id);
      setOffer(null);
      checkFiles();
      return null;
    } catch (e) {
      const matches = sameContentOffer(e);
      if (matches) { // not an error: a dataset already holds this content, and the choice is the person's
        setOffer({ path, matches });
        return null;
      }
      return e instanceof ApiError ? e.message : "Could not register the file.";
    }
  }, [checkFiles]);

  /** Add the offered file as another location of a dataset instead of registering a duplicate. Returns an error text, or null. */
  const addAsLocation = useCallback(async (datasetId: number, path: string): Promise<string | null> => {
    try {
      await addLocation(datasetId, { path });
      setOffer(null);
      setDatasets(await listDatasets({ archived: "all" }));
      setSelectedId(datasetId);
      return null;
    } catch (e) {
      return e instanceof ApiError ? e.message : "Could not add the location.";
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

  /** Apply one declaration to the other runs, and undo it. Both reload the datasets: the declarations they write or remove show in the inspector. */
  const applyActions = useMemo(() => ({
    preview: previewApply,
    apply: async (sourceId: number, scope: ApplyScopeName, targetIds: number[]) => {
      const r = await applyDeclaration(sourceId, scope, targetIds);
      setDatasets(await listDatasets({ archived: "all" }));
      return r;
    },
    undo: async (applyId: string) => {
      const r = await undoApply(applyId);
      setDatasets(await listDatasets({ archived: "all" }));
      return r;
    },
  }), []);

  /** Import a run folder; returns an error text, or null. Refreshes datasets, because the import registers outputs and may declare one. */
  const importRunResult = useCallback(async (path: string, hostId?: number): Promise<{ id: number } | { error: string }> => {
    try {
      const run = await importRun(path, hostId);
      setRuns(await listRuns({ archived: "all" }));
      setDatasets(await listDatasets({ archived: "all" }));
      const first = run.outputs.find((o) => o.role === "orientation") ?? run.outputs[0];
      if (first) setSelectedId(first.datasetId);
      return { id: run.id };
    } catch (e) {
      return { error: e instanceof ApiError ? [e.message, ...e.reasons].join(" · ") : "Could not import the run." };
    }
  }, []);
  const importRunFolder = useCallback(async (path: string, hostId?: number): Promise<string | null> => {
    const r = await importRunResult(path, hostId);
    return "error" in r ? r.error : null;
  }, [importRunResult]);

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

  const catalogCount = useMemo(() => allDatasets.filter((d) => !d.archived && isCatalog(d)).length, [allDatasets]);
  const selected = allDatasets.find((d) => d.id === selectedId) ?? null;
  const selectedImage = selected && isImage(selected) ? selected : null;
  const selectedCatalog = selected && isCatalog(selected) ? selected : null;
  const selectedHealpix = selected && isHealpix(selected) ? selected : null;

  return { problems, newer, offer, dismissOffer: () => setOffer(null), addAsLocation, declareNature, declareCatalog, catalogCount, selectedImage, selectedCatalog, selectedHealpix, declareUnit, jobs, refreshRuns, hosts, refreshHosts, refreshDatasets, runs, importRunFolder, importRunResult, declare, applyActions, datasets, allDatasets, selectedId, select: setSelectedId, loading, error, register, selected };
}
