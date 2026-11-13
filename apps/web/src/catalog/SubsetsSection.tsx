import { useState } from "react";
import type { ImageSummary } from "../api/catalog";
import { createSubset } from "../api/subsets";
import type { SubsetView } from "../api/subsets";
import { Button, Pill } from "../ui";
import { SubsetForm } from "./SubsetForm";
import { describe } from "./subsets";
import s from "./AnalysisPanel.module.css";
import p from "./SubsetPage.module.css";

/**
 * The virtual subsets of this dataset and the form that makes one (ADR 0063). A subset is a saved window: it is viewed without
 * writing anything and written as an ordinary file only when something needs one. Unlike a cutout, it never goes out of date
 * silently: a changed source makes it stale until it is re-pinned.
 */
export function SubsetsSection({ dataset, subsets, onOpen, onCreated }: { dataset: ImageSummary; subsets: SubsetView[]; onOpen: (id: number) => void; onCreated: (s: SubsetView) => void }) {
  const [adding, setAdding] = useState(false);
  const mine = subsets.filter((x) => x.sourceId === dataset.id);
  return (
    <div className={s.wrap}>
      <p className={s.note}>A saved window onto this file: a box, every N-th pixel and a spectral range. Nothing is copied until you write it as a file.</p>
      {mine.length > 0 ? (
        <ul className={p.history} aria-label="Subsets of this dataset">
          {mine.map((x) => (
            <li key={x.id}>
              <button type="button" className={p.link} onClick={() => onOpen(x.id)}>{x.name}</button>{" "}
              <Pill tone={x.stale ? "warn" : "accent"}>{x.stale ? "virtual, stale" : "virtual"}</Pill> <span className={s.note}>{describe(x.definition)}</span>
            </li>
          ))}
        </ul>
      ) : null}
      {adding ? (
        <SubsetForm datasetId={dataset.id} submitLabel="Save the subset" save={(input) => createSubset(dataset.id, input)} onCancel={() => setAdding(false)}
          onSaved={(r) => { setAdding(false); onCreated(r.subset); }} />
      ) : (
        <div><Button type="button" onClick={() => setAdding(true)}>New virtual subset</Button></div>
      )}
    </div>
  );
}
