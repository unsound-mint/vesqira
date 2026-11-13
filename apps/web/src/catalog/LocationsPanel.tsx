import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { addLocation, ApiError, fingerprintLocation, listLocations, removeLocation } from "../api/catalog";
import type { DatasetSummary, DatasetLocation, LocationList } from "../api/catalog";
import { Button, Checkbox, PathField } from "../ui";
import { bytes } from "./format";
import { KIND_TEXT, LINK_TEXT, short } from "./identity";
import s from "./LocationsPanel.module.css";

type Host = { id: number; name: string };
const NO_HOSTS: Host[] = [];
const POLL_MS = 1500;

const day = (iso: string) => iso.slice(0, 10);

/**
 * Where a dataset's bytes are, and what is known about them (ADR 0053): the registered file and any other locations, each
 * with its sampled fingerprint and full SHA-256 (or "not computed"), why it belongs to the dataset, and which one the viewer
 * reads from. Another file is linked only on evidence, or on your declaration, which is labelled as such.
 */
export function LocationsPanel({ dataset, hosts = NO_HOSTS, onChanged }: { dataset: DatasetSummary; hosts?: Host[]; onChanged?: () => void }) {
  const [list, setList] = useState<LocationList | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [path, setPath] = useState("");
  const [declare, setDeclare] = useState(false);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const id = dataset.id;

  const load = useCallback(() => {
    listLocations(id).then((l) => { setList(l); setError(null); }).catch((e: unknown) => setError(e instanceof Error ? e.message : "Could not read the locations."));
  }, [id]);
  useEffect(() => {
    setList(null);
    load();
  }, [load]);

  const busyHashing = list !== null && Object.values(list.hashing).some((h) => h.state !== "failed");
  useEffect(() => {
    if (!busyHashing) return;
    const t = setTimeout(load, POLL_MS);
    return () => clearTimeout(t);
  }, [busyHashing, list, load]);

  const act = useCallback(async (f: () => Promise<unknown>) => {
    try {
      await f();
      setError(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "That did not work.");
    }
    load();
  }, [load]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!path.trim()) return;
    setBusy(true);
    try {
      await addLocation(id, { path: path.trim(), declare });
      setPath("");
      setDeclare(false);
      setFormError(null);
      load();
      onChanged?.();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "Could not add the location.");
    }
    setBusy(false);
  }

  const where = (l: DatasetLocation) => (l.hostId === 0 ? "this computer" : hosts.find((h) => h.id === l.hostId)?.name ?? `host ${l.hostId}`);
  const reads = list?.readsFrom ? list.locations.find((l) => l.id === list.readsFrom!.locationId) : undefined;

  return (
    <div className={s.wrap}>
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {list === null && !error ? <p className={s.note}>Reading the locations…</p> : null}
      {list ? (
        <>
          <p className={s.reads} data-testid="reads-from">
            {reads && list.readsFrom ? (
              <>Reads from: <span className="mono" title={reads.path}>{reads.path.split("/").pop()}</span> on {where(reads)}. {list.readsFrom.reason}.{list.readsFrom.checked ? "" : " (A file on a host is not contacted to say this.)"}</>
            ) : (
              <>Nothing can be read now{list.unavailable ? `: ${list.unavailable}` : ""}.</>
            )}
          </p>
          <ul className={s.list} aria-label="Locations">
            {list.locations.map((l) => {
              const task = list.hashing[String(l.id)];
              const running = task && task.state !== "failed";
              const pct = task && task.totalBytes > 0 ? Math.min(100, Math.floor((task.doneBytes / task.totalBytes) * 100)) : 0;
              return (
                <li key={l.id} className={s.loc}>
                  <div className={s.row}>
                    <strong>{KIND_TEXT[l.kind]}</strong>
                    <span>on {where(l)}</span>
                    <span>· {LINK_TEXT[l.link]}</span>
                  </div>
                  <div className={`mono ${s.path}`} title={l.path}>{l.path}</div>
                  <p className={s.fp}>{bytes(l.sizeBytes)}</p>
                  <p className={s.fp}>
                    Sampled fingerprint:{" "}
                    {l.sampledFingerprint ? (
                      <><span className="mono" title={l.sampledFingerprint.value}>{short(l.sampledFingerprint.value)}</span> (computed {day(l.sampledFingerprint.at)} by {l.sampledFingerprint.by})</>
                    ) : "not computed"}
                  </p>
                  <p className={s.fp}>
                    Full SHA-256:{" "}
                    {l.fullSha256 ? (
                      <><span className="mono" title={l.fullSha256.value}>{short(l.fullSha256.value)}</span> (computed {day(l.fullSha256.at)} by {l.fullSha256.by})</>
                    ) : "not computed"}
                  </p>
                  {running ? <p className={s.fp} role="status">{task.state === "queued" ? "Waiting for another full hash to finish." : `Computing the full SHA-256: ${pct}%`}</p> : null}
                  {task?.state === "failed" ? <p role="alert" className={s.err}>The full SHA-256 was not computed: {task.error}</p> : null}
                  <div className={s.row}>
                    {!l.sampledFingerprint ? <Button type="button" onClick={() => void act(() => fingerprintLocation(id, l.id, "sampled"))}>Compute sampled fingerprint</Button> : null}
                    {!l.fullSha256 && !running ? <Button type="button" onClick={() => void act(() => fingerprintLocation(id, l.id, "full"))}>Compute full SHA-256</Button> : null}
                    {running ? <Button type="button" onClick={() => void act(() => fingerprintLocation(id, l.id, "cancel"))}>Cancel</Button> : null}
                    {!l.primary ? <Button type="button" title="Forgets this location; the file is not deleted" onClick={() => void act(() => removeLocation(id, l.id).then(() => onChanged?.()))}>Remove location</Button> : null}
                  </div>
                </li>
              );
            })}
          </ul>
          <p className={s.note}>
            A sampled fingerprint covers: {list.sampledCovers}. It is not a hash of the whole file. A different value proves two files differ; an equal one does not prove they are the same.
            A full SHA-256 reads every byte{list.fullHashCapBytes ? ` (up to ${bytes(list.fullHashCapBytes)}, in the background, one at a time)` : ""} and is the only check called “verified”.
          </p>
        </>
      ) : null}
      <form className={s.add} onSubmit={submit}>
        <PathField label="Add another file that holds this dataset" kind="file" placeholder="/absolute/path/to/a/copy.fits" value={path} onChange={setPath} />
        <Checkbox label="I say it is the same dataset (used only when nothing computed can tell; never overrides a proven difference)" checked={declare} onChange={(e) => setDeclare(e.target.checked)} />
        <div className={s.row}>
          <Button type="submit" disabled={busy || !path.trim()}>{busy ? "Checking…" : "Add location"}</Button>
        </div>
        {formError ? <p role="alert" className={s.err}>{formError}</p> : null}
      </form>
    </div>
  );
}
