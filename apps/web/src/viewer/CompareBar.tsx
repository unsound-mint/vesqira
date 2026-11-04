import { Pill, Select } from "../ui";
import type { Compat, Dataset } from "../api/catalog";
import s from "./CompareBar.module.css";

/** Links only maps that are known to share a grid. Otherwise they sit side by side, unlinked, with the reasons. */
export function linkState(compat: Compat | null, loading: boolean): { linked: boolean; text: string; tone: "ok" | "warn" | "neutral" } {
  if (loading || !compat) return { linked: false, text: "Checking the grids…", tone: "neutral" };
  return compat.compatible
    ? { linked: true, text: "Same grid: cursor and channel are linked", tone: "ok" }
    : { linked: false, text: "Different or unconfirmed grids: not linked, nothing is resampled", tone: "warn" };
}

export function CompareBar({ datasets, selectedId, compareId, onChange, compat, loading, error }: {
  datasets: Dataset[];
  selectedId: number;
  compareId: number | null;
  onChange: (id: number | null) => void;
  compat: Compat | null;
  loading: boolean;
  error: string | null;
}) {
  const others = datasets.filter((d) => d.id !== selectedId);
  const st = linkState(compat, loading);
  return (
    <div className={s.bar}>
      <Select label="Compare with" value={compareId ?? ""} onChange={(e) => onChange(e.target.value ? Number(e.target.value) : null)}>
        <option value="">Nothing</option>
        {others.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
      </Select>
      {compareId !== null ? (
        <div className={s.state}>
          {error ? <span role="alert" className={s.err}>{error}</span> : <Pill tone={st.tone}>{st.text}</Pill>}
          {compat && !compat.compatible ? (
            <details className={s.why}>
              <summary>Why ({compat.reasons.length})</summary>
              <ul>{compat.reasons.map((r) => <li key={r}>{r}</li>)}</ul>
            </details>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
