import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Database, Layers, Table } from "lucide-react";
import { isCatalog, isHealpix, type DatasetSummary } from "../api/catalog";
import type { Problem } from "../api/catalog";
import { Button, FilterBox, PathField, TreeRow, VirtualList } from "../ui";
import { bytes } from "./format";
import { matchDataset } from "./filter";
import { datasetVocabulary } from "./vocabulary";
import { previewFile } from "../api/catalog";
import type { Preview } from "../api/catalog";
import type { SubsetView } from "../api/subsets";
import { SameContentOffer } from "./SameContentOffer";
import type { Offer } from "./SameContentOffer";
import s from "./Explorer.module.css";

const FITS_EXT = [".fits", ".fit", ".fits.gz", ".csv", ".tsv", ".ecsv", ".dat", ".txt", ".tbl", ".cat", ".xcat", ".vot", ".votable"];
const isFitsPath = (p: string) => /\.(fits?|fits\.gz)$/i.test(p.trim());
const PROBLEM_SHORT: Record<string, string> = { modified: " · changed on disk", missing: " · missing", not_allowed: " · outside the source folders" };

type Props = {
  datasets: DatasetSummary[];
  hosts?: Host[];
  selectedId: number | null;
  problems?: Record<string, Problem>;
  /** The access label each dataset carries, by id (ADR 0069); shown as words in the row, unlabelled datasets are absent. */
  access?: Record<string, string>;
  loading: boolean;
  error: string | null;
  onSelect: (id: number) => void;
  /** Virtual subsets (ADR 0063) are listed under their source, marked as virtual: a window, never a file. */
  subsets?: SubsetView[];
  selectedSubsetId?: number | null;
  onSelectSubset?: (id: number) => void;
  onRegister: (path: string, opts?: { asNew?: boolean }) => Promise<string | null>;
  /** A registered dataset already holds the content of the file just submitted (ADR 0053): the choice, not an error. */
  offer?: Offer | null;
  onAddLocation?: (datasetId: number, path: string) => Promise<string | null>;
  onDismissOffer?: () => void;
};

type Host = { id: number; name: string };
const NO_HOSTS: Host[] = [];
const NO_SUBSETS: SubsetView[] = [];

export function Explorer({ hosts = NO_HOSTS, datasets, selectedId, problems = {}, access = {}, loading, error, onSelect, subsets = NO_SUBSETS, selectedSubsetId = null, onSelectSubset, onRegister, offer = null, onAddLocation, onDismissOffer }: Props) {
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [q, setQ] = useState("");
  const [showSources, setShowSources] = useState(false);
  // A file that is only the mask file of another dataset (the server says so from the masks' rows) is listed under that dataset's
  // Masks section instead; it stays registered and one click brings it back here. The open dataset is never hidden.
  const isSource = (d: DatasetSummary) => !d.archived && (d.maskSourceFor?.length ?? 0) > 0 && d.id !== selectedId;
  const matching = datasets.filter((d) => matchDataset(d, q, hosts.find((h) => h.id === d.hostId)?.name));
  const sourceCount = datasets.filter(isSource).length;
  const hiddenSources = showSources ? 0 : matching.filter(isSource).length;
  const shown = showSources ? matching : matching.filter((d) => !isSource(d));
  const vocabulary = useMemo(() => datasetVocabulary(datasets, hosts), [datasets, hosts]);
  const active = datasets.filter((d) => !d.archived).length;
  // One flat list of rows (a dataset, then the virtual subsets of it): the windowed list needs the rows by index, and the
  // subsets are grouped once here instead of filtering all of them for each of thousands of datasets.
  const subsetById = useMemo(() => new Map(subsets.map((x) => [`s${x.id}`, x])), [subsets]);
  const datasetById = useMemo(() => new Map(datasets.map((d) => [String(d.id), d])), [datasets]);
  const rowIds = useMemo(() => {
    const bySource = new Map<number, string[]>();
    for (const x of subsets) bySource.set(x.sourceId, [...(bySource.get(x.sourceId) ?? []), `s${x.id}`]);
    return shown.flatMap((d) => [String(d.id), ...(bySource.get(d.id) ?? [])]);
  }, [shown, subsets]);
  const selectedRow = selectedSubsetId !== null ? `s${selectedSubsetId}` : selectedId === null ? null : String(selectedId);

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
    const submitted = path;
    setBusy(true);
    const err = await onRegister(submitted.trim());
    setBusy(false);
    setFormError(err);
    // Clear the field only if it still holds what was registered: the reply can arrive after the person has typed the next path
    if (!err) setPath((cur) => (cur === submitted ? "" : cur));
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
        {offer && onAddLocation ? (
          <SameContentOffer
            offer={offer}
            onAdd={(id) => { void onAddLocation(id, offer.path).then(setFormError); }}
            onSeparate={() => { void onRegister(offer.path, { asNew: true }).then(setFormError); }}
            onDismiss={() => onDismissOffer?.()}
          />
        ) : null}
      </form>
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {loading ? <p className={s.note}>Loading datasets…</p> : null}
      {!loading && !error && datasets.length === 0 ? <p className={s.note}>No datasets yet. Files are referenced where they are; nothing is copied.</p> : null}
      {datasets.length > 5 || datasets.length > active ? <FilterBox label="Filter datasets" placeholder="Filter datasets" value={q} onChange={setQ} vocabulary={vocabulary} shown={shown.length} total={showSources ? active : active - sourceCount} noun="datasets" /> : null}
      {sourceCount > 0 ? (
        <p className={s.note} data-testid="mask-sources-note">
          {showSources
            ? `Showing ${sourceCount} mask ${sourceCount === 1 ? "file" : "files"} too. `
            : `${hiddenSources} mask ${hiddenSources === 1 ? "file is" : "files are"} hidden${hiddenSources === sourceCount ? "" : ` (${sourceCount} in all)`}: ${sourceCount === 1 ? "its" : "their"} only role is being the mask of another dataset. `}
          <Button type="button" variant="ghost" size="sm" aria-pressed={showSources} onClick={() => setShowSources(!showSources)}>{showSources ? "Hide them" : "Show"}</Button>
        </p>
      ) : null}
      {datasets.length > 0 && shown.length === 0 ? <p className={s.note}>{active === 0 && !q.includes("archived:") ? "All datasets are archived. Filter with archived:yes to see them." : "No dataset matches."}</p> : null}
      <VirtualList label="Datasets" ids={rowIds} selectedId={selectedRow} renderRow={(id, rowProps) => {
        const x = subsetById.get(id);
        if (x) {
          return (
            <TreeRow icon={Layers} level={1} rowProps={rowProps} selected={x.id === selectedSubsetId} onSelect={() => onSelectSubset?.(x.id)}
              title="A virtual subset: a saved window onto the file above, not a file"
              trailing={`virtual subset${x.stale ? " · stale: its file changed" : ""}`}>{x.name}</TreeRow>
          );
        }
        const d = datasetById.get(id)!;
        return <TreeRow icon={isCatalog(d) ? Table : Database} rowProps={rowProps} selected={d.id === selectedId && selectedSubsetId === null} onSelect={() => onSelect(d.id)} trailing={`${access[String(d.id)] === "unreleased" ? "unreleased · " : access[String(d.id)] === "collaboration" ? "collaboration only · " : ""}${d.maskSourceFor?.length ? `mask file for ${d.maskSourceFor.map((m) => m.name).join(", ")} · ` : ""}${d.hostId ?`${hosts.find((h) => h.id === d.hostId)?.name ?? "host"} · ` : ""}${isCatalog(d) && d.facts.rows !== null ? `${d.facts.rows.toLocaleString("en-US")} rows · ` : ""}${isHealpix(d) && d.facts.nside !== null ? `HEALPix ${d.facts.nside} · ` : ""}${bytes(d.sizeBytes)}${d.archived ? " · archived" : ""}${PROBLEM_SHORT[problems[String(d.id)] ?? ""] ?? ""}`}>{d.name}</TreeRow>;
      }} />
    </div>
  );
}
