import { useState } from "react";
import type { Dataset, Nature } from "../api/catalog";
import { Select } from "../ui";
import s from "./AnalysisPanel.module.css";

const TEXT: Record<string, string> = {
  observed: "Observed data.",
  simulated: "Simulated data: a result on a simulation is not validation on observations.",
  constructed_control: "A constructed control (synthetic data built for a test). It must not be read as an observation.",
};

/** What the data is, as you declare it. The file cannot say; it travels with figures and the review page. */
export function NatureControl({ dataset, onChange }: { dataset: Dataset; onChange: (id: number, n: Nature) => Promise<string | null> }) {
  const [err, setErr] = useState<string | null>(null);
  return (
    <div className={s.wrap}>
      <Select label="What this data is" value={dataset.nature || "none"} onChange={async (e) => setErr(await onChange(dataset.id, e.target.value as Nature))}>
        <option value="none">Not declared</option>
        <option value="observed">Observed</option>
        <option value="simulated">Simulated</option>
        <option value="constructed_control">Constructed control</option>
      </Select>
      <p className={s.note}>{dataset.nature ? TEXT[dataset.nature] : "Not declared. Figures made from it will not say whether it is observed, simulated or a control."}</p>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
