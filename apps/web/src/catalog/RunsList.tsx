import { useState } from "react";
import type { FormEvent } from "react";
import { GitBranch } from "lucide-react";
import type { Run } from "../api/runs";
import { Button, Input, TreeRow } from "../ui";
import { matchRun } from "./filter";
import s from "./Explorer.module.css";

/** Pipeline runs: import a run folder, open one. A dirty commit is flagged in the list itself. */
export function RunsList({ runs, onImport, onOpen }: { runs: Run[]; onImport: (path: string) => Promise<string | null>; onOpen: (run: Run) => void }) {
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const shown = runs.filter((r) => matchRun(r, q));
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!path.trim()) return;
    setBusy(true);
    const r = await onImport(path.trim());
    setBusy(false);
    setErr(r);
    if (!r) setPath("");
  }
  return (
    <div className={s.wrap}>
      <form className={s.form} onSubmit={submit}>
        <Input label="Import a VGT run" placeholder="/path/to/run_folder" value={path} onChange={(e) => setPath(e.target.value)} spellCheck={false} />
        <Button type="submit" disabled={busy || !path.trim()}>{busy ? "Importing…" : "Import run"}</Button>
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
      </form>
      {runs.length === 0 ? <p className={s.note}>No runs yet. Point this at a folder holding run_metadata.toml and its output maps.</p> : null}
      {runs.length > 3 ? <Input label="Filter runs" placeholder="ker=8 noise>0.3 qc:passed" value={q} onChange={(e) => setQ(e.target.value)} spellCheck={false} /> : null}
      {runs.length > 0 && shown.length === 0 ? <p className={s.note}>No run matches. Parameters are written key=value, key&gt;value or key&lt;value.</p> : null}
      <div role="list" aria-label="Runs">
        {shown.map((r) => (
          <div role="listitem" key={r.id}>
            <TreeRow icon={GitBranch} onSelect={() => onOpen(r)} trailing={r.reproducibleFromCommit ? "" : "dirty"}>{r.name}</TreeRow>
          </div>
        ))}
      </div>
    </div>
  );
}
