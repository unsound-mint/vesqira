import { useCallback, useEffect, useState } from "react";
import { ApiError, listDatasets, registerDataset, setInterpretation } from "../api/catalog";
import type { Dataset, InterpretationRequest } from "../api/catalog";

export function useCatalog() {
  const [datasets, setDatasets] = useState<Dataset[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
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

  return { declare, datasets, selectedId, select: setSelectedId, loading, error, register, selected: datasets.find((d) => d.id === selectedId) ?? null };
}
