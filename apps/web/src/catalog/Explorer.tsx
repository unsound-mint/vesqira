import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Database, Table } from "lucide-react";
import { isCatalog } from "../api/catalog";
import type { Dataset, Problem } from "../api/catalog";
import { Button, FilterBox, PathField, TreeRow } from "../ui";
import { bytes } from "./format";
import { matchDataset } from "./filter";
import { datasetVocabulary } from "./vocabulary";
import { previewFile } from "../api/catalog";
import type { Preview } from "../api/catalog";
import s from "./Explorer.module.css";

const FITS_EXT = [".fits", ".fit", ".fits.gz", ".csv", ".tsv", ".ecsv", ".dat", ".txt", ".tbl", ".cat", ".xcat", ".vot", ".votable"];
const isFitsPath = (p: string) => /\.(fits?|fits\.gz)$/i.test(p.trim());
const PROBLEM_SHORT: Record<string, string> = { modified: " · changed on disk", missing: " · missing", not_allowed: " · outside the source folders" };

type Props = {
  datasets: Dataset[];
  hosts?: Host[];
  selectedId: number | null;
  problems?: Record<string, Problem>;
  loading: boolean;
  error: string | null;
  onSelect: (id: number) => void;
  onRegister: (path: string) => Promise<string | null>;
};

type Host = { id: number; name: string };
const NO_HOSTS: Host[] = [];

export function Explorer({ hosts = NO_HOSTS, datasets, selectedId, problems = {}, loading, error, onSelect, onRegister }: Props) {
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [q, setQ] = useState("");
  const shown = datasets.filter((d) => matchDataset(d, q, hosts.find((h) => h.id === d.hostId)?.name));
  const vocabulary = useMemo(() => datasetVocabulary(datasets, hosts), [datasets, hosts]);
  const active = datasets.filter((d) => !d.archived).length;

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
        <PathField label="Register a FITS file or a catalog" kind="file" ext={FITS_EXT} placeholder="/absolute/path/to/cube.fits or stars.csv" value={path} onChange={setPath} />
        <Button type="submit" variant="primary" disabled={busy || !path.trim()}>{busy ? "Reading header…" : "Register"}</Button>
        <Button type="button" disabled={busy || !isFitsPath(path)} onClick={doPreview} title={path.trim() && !isFitsPath(path) ? "The preview is for FITS files; a catalog shows its rows once registered." : undefined}>Preview before registering</Button>
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
      {datasets.length > 5 || datasets.length > active ? <FilterBox label="Filter datasets" placeholder="Filter datasets" value={q} onChange={setQ} vocabulary={vocabulary} shown={shown.length} total={active} noun="datasets" /> : null}
      {datasets.length > 0 && shown.length === 0 ? <p className={s.note}>{active === 0 && !q.includes("archived:") ? "All datasets are archived. Filter with archived:yes to see them." : "No dataset matches."}</p> : null}
      <div role="list" aria-label="Datasets">
        {shown.map((d) => (
          <div role="listitem" key={d.id}>
            <TreeRow icon={isCatalog(d) ? Table : Database} selected={d.id === selectedId} onSelect={() => onSelect(d.id)} trailing={`${d.hostId ? `${hosts.find((h) => h.id === d.hostId)?.name ?? "host"} · ` : ""}${isCatalog(d) ? `${d.header.rows.toLocaleString("en-US")} rows · ` : ""}${bytes(d.sizeBytes)}${d.archived ? " · archived" : ""}${PROBLEM_SHORT[problems[String(d.id)] ?? ""] ?? ""}`}>{d.name}</TreeRow>
          </div>
        ))}
      </div>
    </div>
  );
}
