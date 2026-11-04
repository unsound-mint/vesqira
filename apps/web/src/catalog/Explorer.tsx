import { useState } from "react";
import type { FormEvent } from "react";
import { Database } from "lucide-react";
import type { Dataset } from "../api/catalog";
import { Button, Input, TreeRow } from "../ui";
import { bytes } from "./format";
import { matchDataset } from "./filter";
import { previewFile } from "../api/catalog";
import type { Preview } from "../api/catalog";
import s from "./Explorer.module.css";

type Props = {
  datasets: Dataset[];
  hosts?: { id: number; name: string }[];
  selectedId: number | null;
  loading: boolean;
  error: string | null;
  onSelect: (id: number) => void;
  onRegister: (path: string) => Promise<string | null>;
};

export function Explorer({ hosts = [], datasets, selectedId, loading, error, onSelect, onRegister }: Props) {
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [q, setQ] = useState("");
  const shown = datasets.filter((d) => matchDataset(d, q, hosts.find((h) => h.id === d.hostId)?.name));

  async function doPreview() {
    try {
      setPreview(await previewFile(path.trim()));
      setFormError(null);
    } catch (e) {
      setPreview(null);
      setFormError(e instanceof Error ? e.message : "Could not preview the file.");
    }
  }

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
        <Button type="button" disabled={busy || !path.trim()} onClick={doPreview}>Preview before registering</Button>
        {preview ? (
          <div className={s.note} role="status">
            <p style={{ margin: 0 }}><span className="mono">{preview.name}</span>: {bytes(preview.sizeBytes)}, {preview.planes.toLocaleString("en-US")} plane{preview.planes === 1 ? "" : "s"} of about {bytes(preview.planeBytes)} each.</p>
            <p style={{ margin: 0 }}>Sky coordinates: {preview.celestialWcs ? "declared" : "not declared (positions will be image pixels only)"}. Spectral axis: {preview.spectralAxis ? "declared" : preview.axes > 2 ? "not fully declared (no velocities will be shown)" : "none"}.</p>
            <p style={{ margin: 0 }}>{preview.access}</p>
          </div>
        ) : null}
        {formError ? <p role="alert" className={s.err}>{formError}</p> : null}
      </form>
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {loading ? <p className={s.note}>Loading datasets…</p> : null}
      {!loading && !error && datasets.length === 0 ? <p className={s.note}>No datasets yet. Files are referenced where they are; nothing is copied.</p> : null}
      {datasets.length > 5 ? <Input label="Filter datasets" placeholder="name, unit, host or nature" value={q} onChange={(e) => setQ(e.target.value)} spellCheck={false} /> : null}
      {datasets.length > 0 && shown.length === 0 ? <p className={s.note}>No dataset matches.</p> : null}
      <div role="list" aria-label="Datasets">
        {shown.map((d) => (
          <div role="listitem" key={d.id}>
            <TreeRow icon={Database} selected={d.id === selectedId} onSelect={() => onSelect(d.id)} trailing={`${d.hostId ? `${hosts.find((h) => h.id === d.hostId)?.name ?? "host"} · ` : ""}${bytes(d.sizeBytes)}`}>{d.name}</TreeRow>
          </div>
        ))}
      </div>
    </div>
  );
}
