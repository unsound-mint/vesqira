import { useCallback, useEffect, useRef, useState } from "react";
import { addStep, getStep, listHistory } from "../api/history";
import type { ExplorationStep } from "../api/history";
import type { Scene } from "../scene/scene";
import { StepRecorder } from "./recorder";

/** What stops a step being continued: the datasets its scene draws that the catalog no longer has. */
export function missingDatasets(scene: Scene, have: (id: number) => boolean): number[] {
  const ids = new Set<number>([scene.base.datasetId]);
  for (const o of scene.overlays) if ("datasetId" in o && typeof o.datasetId === "number") ids.add(o.datasetId);
  return [...ids].filter((id) => !have(id));
}

/**
 * The exploration history of one project (0: none open) for the primary viewer pane: the steps (oldest first), recording the
 * pane's scene as steps through a StepRecorder, and "continue from here". `restore` applies a scene to the pane (it may have to
 * open another dataset first); `haveDataset` says what the catalog still has.
 */
export function useExplorationHistory({ projectId, scene, datasetName, haveDataset, restore }: {
  projectId: number;
  scene: Scene | null;
  datasetName: (id: number) => string;
  haveDataset: (id: number) => boolean;
  restore: (scene: Scene, opensOther: boolean) => void;
}) {
  const [steps, setSteps] = useState<ExplorationStep[]>([]);
  const [keep, setKeep] = useState(500);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const recorder = useRef<StepRecorder | null>(null);
  const nameRef = useRef(datasetName);
  nameRef.current = datasetName;

  const reload = useCallback(async (signal?: AbortSignal) => {
    const r = await listHistory(projectId, signal);
    const list = Array.isArray(r?.steps) ? r.steps : [];
    setSteps(list);
    setKeep(r?.keep ?? 500);
    return list;
  }, [projectId]);

  // one recorder per project; the newest step is where the next one branches from
  useEffect(() => {
    const ac = new AbortController();
    setLoaded(false);
    setSteps([]);
    const rec = new StepRecorder(
      async (c) => {
        try {
          const st = await addStep({ projectId, parentId: c.parentId, kind: c.kind, label: c.label, scene: c.scene });
          void reload().catch(() => undefined);
          setError(null);
          return st.id;
        } catch (e) {
          setError(e instanceof Error ? `The history could not be saved: ${e.message}` : "The history could not be saved.");
          return null;
        }
      },
      (id) => nameRef.current(id),
    );
    recorder.current = rec;
    listHistory(projectId, ac.signal)
      .then((r) => {
        const list = Array.isArray(r?.steps) ? r.steps : []; // a server without the route (an older build) has no history, not an error
        setSteps(list);
        setKeep(r?.keep ?? 500);
        const last = list[list.length - 1];
        rec.setHead(last?.id ?? null); // the head's state is unknown after a reload: the next step is never skipped as a duplicate
        setError(null);
        setLoaded(true);
      })
      .catch((e: unknown) => {
        if (e instanceof DOMException && e.name === "AbortError") return;
        setError(e instanceof Error ? `The history could not be read: ${e.message}` : "The history could not be read.");
        setLoaded(true);
      });
    return () => {
      ac.abort();
      rec.flush(); // a step still waiting is written, to the project it belongs to
      if (recorder.current === rec) recorder.current = null;
    };
  }, [projectId, reload]);

  useEffect(() => {
    if (loaded) recorder.current?.observe(scene);
  }, [scene, loaded]);

  const continueFrom = useCallback(async (step: ExplorationStep) => {
    setError(null);
    try {
      const full = await getStep(step.id);
      const target = full.scene as Scene | undefined;
      if (!target) throw new Error("the step has no scene");
      const gone = missingDatasets(target, haveDataset);
      if (gone.length > 0) {
        setError(`Cannot continue from “${step.label}”: ${gone.length === 1 ? "a dataset it draws is" : "datasets it draws are"} no longer in the catalog (${gone.map((id) => `#${id}`).join(", ")}).`);
        return false;
      }
      const opensOther = scene?.base.datasetId !== target.base.datasetId;
      recorder.current?.continueFrom(step, target, opensOther);
      restore(target, opensOther);
      return true;
    } catch (e) {
      setError(e instanceof Error ? `Could not continue from that step: ${e.message}` : "Could not continue from that step.");
      return false;
    }
  }, [haveDataset, restore, scene?.base.datasetId]);

  // the list follows the writes the recorder makes in the background
  return { steps, keep, error, loaded, continueFrom, reload };
}
