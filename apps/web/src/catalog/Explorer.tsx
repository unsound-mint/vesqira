import { useState } from "react";
import type { FormEvent } from "react";
import { Database } from "lucide-react";
import type { Dataset } from "../api/catalog";
import { Button, Input, TreeRow } from "../ui";
import { bytes } from "./format";
import s from "./Explorer.module.css";

type Props = {
  datasets: Dataset[];
  selectedId: number | null;
  loading: boolean;
  error: string | null;
  onSelect: (id: number) => void;
  onRegister: (path: string) => Promise<string | null>;
};

export function Explorer({ datasets, selectedId, loading, error, onSelect, onRegister }: Props) {
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!path.trim()) return;
    setBusy(true);
    const err = await onRegister(path.trim());
    setBusy(false);
    setFormError(err);
    if (!err) setPath("");
  }

  return (
    <div className={s.wrap}>
      <form className={s.form} onSubmit={submit}>
        <Input label="Register a FITS file" placeholder="/absolute/path/to/cube.fits" value={path} onChange={(e) => setPath(e.target.value)} spellCheck={false} />
        <Button type="submit" variant="primary" disabled={busy || !path.trim()}>{busy ? "Reading header…" : "Register"}</Button>
        {formError ? <p role="alert" className={s.err}>{formError}</p> : null}
      </form>
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {loading ? <p className={s.note}>Loading datasets…</p> : null}
      {!loading && !error && datasets.length === 0 ? <p className={s.note}>No datasets yet. Files are referenced where they are; nothing is copied.</p> : null}
      <div role="list" aria-label="Datasets">
        {datasets.map((d) => (
          <div role="listitem" key={d.id}>
            <TreeRow icon={Database} selected={d.id === selectedId} onSelect={() => onSelect(d.id)} trailing={bytes(d.sizeBytes)}>{d.name}</TreeRow>
          </div>
        ))}
      </div>
    </div>
  );
}
