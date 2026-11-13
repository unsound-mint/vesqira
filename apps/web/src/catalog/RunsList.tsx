import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { GitBranch } from "lucide-react";
import type { Run } from "../api/runs";
import type { Job } from "../api/jobs";
import { Button, FilterBox, PathField, Select, TreeRow, VirtualList } from "../ui";
import { matchRun } from "./filter";
import { runVocabulary } from "./vocabulary";
import { contextIndex } from "./runTable";
import s from "./Explorer.module.css";

/** Pipeline runs: import a run folder, open one. A dirty commit is flagged in the list itself. */
export function RunsList({ runs, hosts = [], jobs = [], query, onQuery, onImport, onOpen, onOpenTable }: {
  runs: Run[];
  hosts?: { id: number; name: string }[];
  jobs?: Job[];
  /** The filter words, when the page keeps them (the runs table reads the same query). Without them the list keeps its own. */
  query?: string;
  onQuery?: (q: string) => void;
  onImport: (path: string, hostId?: number) => Promise<string | null>;
  onOpen: (run: Run) => void;
  /** Opens the runs table; the list only offers it when given. */
  onOpenTable?: () => void;
}) {
  const [path, setPath] = useState("");
  const [hostId, setHostId] = useState(""); // "" is this computer; otherwise a ROHSA fit on that host (ADR 0046)
  const host = hostId ? Number(hostId) : undefined;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [own, setOwn] = useState("");
  const q = query ?? own;
  const setQ = onQuery ?? setOwn;
  const ctxOf = useMemo(() => contextIndex(runs, jobs, (id) => hosts.find((h) => h.id === id)?.name), [runs, jobs, hosts]);
  const shown = useMemo(() => runs.filter((r) => matchRun(r, q, ctxOf(r))), [runs, q, ctxOf]);
  const vocabulary = useMemo(() => runVocabulary(runs, ctxOf), [runs, ctxOf]);
  const runIds = useMemo(() => shown.map((r) => String(r.id)), [shown]);
  const runById = useMemo(() => new Map(runs.map((r) => [String(r.id), r])), [runs]);
  const active = runs.filter((r) => !r.archived).length;
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!path.trim()) return;
    setBusy(true);
    const r = await onImport(path.trim(), host);
    setBusy(false);
    setErr(r);
    if (!r) setPath("");
  }
  return (
    <div className={s.wrap}>
      <form className={s.form} onSubmit={submit}>
        {hosts.length > 0 ? (
          <Select label="Where the run is" value={hostId} onChange={(e) => { setHostId(e.target.value); setPath(""); }}>
            <option value="">This computer</option>
            {hosts.map((h) => <option key={h.id} value={h.id}>{h.name} (a ROHSA fit)</option>)}
          </Select>
        ) : null}
        <PathField label={host ? "Import a ROHSA fit from the host" : "Import a run folder"} kind="dir" hostId={host} hosts={hosts} placeholder={host ? "/shared/home/you/run/region (a folder, or its rohsa_*.dat)" : "/path/to/run_folder"} value={path} onChange={setPath} />
        {host ? <p className={s.note}>The host reads the fit's small records and the headers of its maps; the .dat stays there and is indexed there when you first open its components.</p> : null}
        <Button type="submit" disabled={busy || !path.trim()}>{busy ? "Importing…" : "Import run"}</Button>
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
      </form>
      {runs.length === 0 ? <p className={s.note}>No runs yet. Point this at a folder holding run_metadata.toml and its output maps, or at a ROHSA fit (its rohsa_*.dat, or the folder holding it).</p> : null}
      {runs.length > 3 || runs.length > active ? <FilterBox label="Filter runs" placeholder="Filter runs" value={q} onChange={setQ} vocabulary={vocabulary} shown={shown.length} total={active} noun="runs" /> : null}
      {onOpenTable && runs.length > 0 ? <Button variant="ghost" onClick={onOpenTable}>Open as a table with plot…</Button> : null}
      {runs.length > 0 && shown.length === 0 ? <p className={s.note}>No run matches. Remove a filter above to widen the list.</p> : null}
      <VirtualList label="Runs" ids={runIds} renderRow={(id, rowProps) => {
        const r = runById.get(id)!;
        return <TreeRow icon={GitBranch} rowProps={rowProps} onSelect={() => onOpen(r)} trailing={[r.archived ? "archived" : "", r.reproducibleFromCommit ? "" : "dirty"].filter(Boolean).join(" · ")}>{r.name}</TreeRow>;
      }} />
    </div>
  );
}
