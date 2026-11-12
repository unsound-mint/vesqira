import type { Run } from "../api/runs";
import type { RunViewSpec } from "../api/runViews";
import { DEFAULT_COLUMNS } from "../catalog/runTable";
import type { ColumnId } from "../catalog/runTable";
import type { Axis } from "../catalog/runPlot";
import { queriedKeys } from "../catalog/runQuery";

/** What the runs page remembers while the researcher goes to a run and back: the query, the columns, the sort, the plot and the selection. */
export type RunsViewState = {
  query: string;
  columns: ColumnId[];
  sort: { column: ColumnId; dir: 1 | -1 };
  /** The plot: a result (y), what it is drawn against (x) and a parameter to colour by. Empty y means nothing is chosen yet. */
  plot: { y: string; x: string; xKind: Axis["kind"]; by: string };
  selected: number | null;
  /** Runs that did not record something the query asks about are listed after the others (they are not matches). */
  showMissing: boolean;
};

/** Newest import first: the order the runs were made in, which is how the owner remembers them. */
export const initialRunsView: RunsViewState = {
  query: "",
  columns: DEFAULT_COLUMNS,
  sort: { column: "imported", dir: -1 },
  plot: { y: "", x: "", xKind: "param", by: "" },
  selected: null,
  showMissing: false,
};

export const specOf = (s: RunsViewState): RunViewSpec => ({
  query: s.query,
  columns: s.columns,
  sort: { column: s.sort.column, dir: s.sort.dir },
  plot: { y: s.plot.y, x: s.plot.x, xKind: s.plot.xKind, by: s.plot.by },
});

export const stateFrom = (spec: RunViewSpec, keep: Pick<RunsViewState, "selected">): RunsViewState => ({
  query: spec.query,
  columns: spec.columns.length > 0 ? spec.columns : DEFAULT_COLUMNS,
  sort: spec.sort ? { column: spec.sort.column, dir: spec.sort.dir === -1 ? -1 : 1 } : initialRunsView.sort,
  plot: spec.plot ? { y: spec.plot.y, x: spec.plot.x, xKind: spec.plot.xKind === "metric" ? "metric" : "param", by: spec.plot.by } : initialRunsView.plot,
  selected: keep.selected,
  showMissing: false,
});

/** The columns shown: the ones the researcher exposed, then one for each parameter or result the query asks about. */
export function effectiveColumns(columns: ColumnId[], query: string, runs: Run[]): ColumnId[] {
  const out = [...columns];
  for (const key of queriedKeys(query)) {
    const isParam = runs.some((r) => r.params.some((p) => p.key.toLowerCase() === key));
    const spelled = runs.flatMap((r): { key: string }[] => (isParam ? r.params : r.metrics)).find((x) => x.key.toLowerCase() === key)?.key ?? key;
    const id = `${isParam ? "p" : "m"}:${spelled}`;
    if (!out.some((c) => c.toLowerCase() === id.toLowerCase())) out.push(id);
  }
  return out;
}
