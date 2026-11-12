import { useCallback, useEffect, useState } from "react";
import type { ChangeEvent, FormEvent } from "react";
import type { Dataset } from "../api/catalog";
import { ApiError } from "../api/http";
import {
  ACTION_TEXT, correctableKeywords, importSidecar, listCorrections, previewCorrection, previewSidecar, saveCorrection, shiftArcsec, sidecarURL, textKeyword, valueText,
} from "../api/corrections";
import type { CorrectionChange, CorrectionCoords, CorrectionImpact, CorrectionInput, CorrectionList, CorrectionPreview, CorrectionRecord, SidecarPreview } from "../api/corrections";
import { Button, Input, Select } from "../ui";
import { TierBadge } from "../ui/TierBadge";
import s from "./CorrectionsPanel.module.css";

type Row = { key: string; value: string };
type Pending =
  | { kind: "input"; input: Omit<CorrectionInput, "reason" | "source">; title: string }
  | { kind: "sidecar"; doc: unknown; name: string };

const deg = (v: number | null | undefined) => (v === null || v === undefined ? "—" : v.toFixed(6));
const day = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
const message = (e: unknown, fallback: string) => (e instanceof ApiError || e instanceof Error ? e.message : fallback);

/** What one record changed, keyword by keyword: "CRVAL2 2 → 2.05 (file 2)". A null after means back to the file's own value. */
function ChangeList({ changes }: { changes: CorrectionChange[] }) {
  if (changes.length === 0) return <p className={s.note}>No value changed.</p>;
  return (
    <ul className={s.changes}>
      {changes.map((c) => (
        <li key={c.what}>
          <span className="mono">{c.what}</span>{" "}
          {c.after === null ? <>correction withdrawn: back to the file&apos;s <span className="mono">{valueText(c.file)}</span></> : <><span className="mono">{valueText(c.before ?? c.file)}</span> → <span className="mono">{valueText(c.after)}</span>{" "}<span className={s.file}>(the file says {valueText(c.file)})</span></>}
        </li>
      ))}
    </ul>
  );
}

/** The coordinates at the image centre and corners before and after, and how far each moves, by astropy in the worker. */
function Coordinates({ before, after }: { before: CorrectionCoords; after: CorrectionCoords | null }) {
  const sky = before.points.some((p) => p.lon !== null) || (after?.points.some((p) => p.lon !== null) ?? false);
  const spec = before.spectral?.channels ?? [];
  const specAfter = after?.spectral?.channels ?? [];
  const unit = (after?.spectral?.facts as { unit?: string } | undefined)?.unit ?? (before.spectral?.facts as { unit?: string } | undefined)?.unit ?? "";
  return (
    <>
      {sky ? (
        <div className={s.scroller} role="region" aria-label="Sky coordinates before and after" tabIndex={0}>
          <table className={s.table}>
            <caption>Sky position (degrees), before → after</caption>
            <thead><tr><th scope="col">Point</th><th scope="col">Before</th><th scope="col">After</th><th scope="col">Moves</th></tr></thead>
            <tbody>
              {before.points.map((p, i) => {
                const q = after?.points[i];
                const moved = p.lon !== null && p.lat !== null && q?.lon !== null && q?.lat !== null && q ? shiftArcsec(p.lon, p.lat, q.lon!, q.lat!) : null;
                return (
                  <tr key={p.name}>
                    <th scope="row">{p.name}</th>
                    <td className="mono">{deg(p.lon)}, {deg(p.lat)}</td>
                    <td className="mono">{q ? `${deg(q.lon)}, ${deg(q.lat)}` : "—"}</td>
                    <td className="mono">{moved === null ? "—" : `${moved.toFixed(2)}″`}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : <p className={s.note}>No celestial WCS before or after: positions are image pixels only.</p>}
      {spec.length > 0 || specAfter.length > 0 ? (
        <div className={s.scroller} role="region" aria-label="Spectral values before and after" tabIndex={0}>
          <table className={s.table}>
            <caption>Spectral axis{unit ? ` (${unit})` : ""}, before → after</caption>
            <thead><tr><th scope="col">Channel</th><th scope="col">Before</th><th scope="col">After</th></tr></thead>
            <tbody>
              {(spec.length > 0 ? spec : specAfter).map((c, i) => (
                <tr key={c.channel}>
                  <th scope="row">{c.channel + 1}</th>
                  <td className="mono">{valueText(spec[i]?.value ?? null)}</td>
                  <td className="mono">{valueText(specAfter[i]?.value ?? null)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </>
  );
}

/** What depends on the dataset's coordinates or unit: re-read with the new values (live) or kept as made (pinned). */
export function ImpactList({ impact }: { impact: CorrectionImpact }) {
  const pinned = impact.groups.filter((g) => g.effect === "pinned");
  const live = impact.groups.filter((g) => g.effect === "live");
  return (
    <div className={s.impact} aria-label="What depends on this dataset">
      <h4 className={s.h}>Read again with the new values</h4>
      <ul className={s.list}>
        {impact.live.map((t) => <li key={t}>{t}</li>)}
        {live.map((g) => <li key={g.key}>{g.text}{g.items.length > 0 ? <span className={s.items}> {g.items.map((i) => i.label).join(", ")}</span> : null}</li>)}
      </ul>
      <h4 className={s.h}>Keep the values they were made with</h4>
      {pinned.length === 0 ? <p className={s.note}>Nothing saved depends on this dataset yet: no figures, derived results or masks.</p> : (
        <ul className={s.list}>
          {pinned.map((g) => <li key={g.key}>{g.text}{g.items.length > 0 ? <span className={s.items}> {g.items.map((i) => i.label).join(", ")}{g.count > g.items.length ? ` and ${g.count - g.items.length} more` : ""}</span> : null}</li>)}
        </ul>
      )}
      {impact.notes.map((t) => <p key={t} className={s.note}>{t}</p>)}
    </div>
  );
}

/**
 * Corrections of what the file says (ADR 0061): a unit the file states, or WCS keywords, each with a required reason, kept as an
 * append-only history beside the dataset. The file is never changed: the worker applies the corrections in memory. Every change
 * is shown before it is saved (the coordinates at the image centre and corners before and after, and what depends on the
 * dataset), and a revert is a new record.
 */
export function CorrectionsPanel({ dataset, onChanged }: { dataset: Dataset; onChanged?: () => void }) {
  const id = dataset.id;
  const naxis = "naxis" in dataset.header ? Number(dataset.header.naxis) : 2;
  const [list, setList] = useState<CorrectionList | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [rows, setRows] = useState<Row[]>([{ key: "", value: "" }]);
  const [reason, setReason] = useState("");
  const [source, setSource] = useState("");
  const [sentence, setSentence] = useState("");
  const [pending, setPending] = useState<Pending | null>(null);
  const [preview, setPreview] = useState<CorrectionPreview | SidecarPreview | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    listCorrections(id).then((l) => { setList(l); setError(null); }).catch((e: unknown) => setError(message(e, "Could not read the corrections.")));
  }, [id]);
  useEffect(() => { setList(null); load(); }, [load]);

  const keywords = correctableKeywords(naxis, list?.unitCorrectable ?? false);
  const current = list?.current ?? null;
  const stale = current?.stale === true;

  function reset() {
    setPending(null);
    setPreview(null);
    setReason("");
    setSource("");
    setSentence("");
  }

  function setInput(): Omit<CorrectionInput, "reason" | "source"> | string {
    const wcs: Record<string, number | string> = {};
    let unit: string | undefined;
    for (const r of rows) {
      if (!r.key) continue;
      const v = r.value.trim();
      if (!v) return `Give a value for ${r.key}.`;
      if (r.key === "BUNIT") unit = v;
      else if (textKeyword(r.key)) wcs[r.key] = v;
      else {
        const n = Number(v);
        if (!Number.isFinite(n)) return `${r.key} is a number.`;
        wcs[r.key] = n;
      }
    }
    if (unit === undefined && Object.keys(wcs).length === 0) return "Choose a keyword to correct.";
    return { action: "set", wcs, ...(unit !== undefined ? { unit } : {}) };
  }

  async function show(p: Pending) {
    setBusy(true);
    setError(null);
    setPreview(null);
    setPending(p);
    try {
      setPreview(p.kind === "sidecar" ? await previewSidecar(id, p.doc) : await previewCorrection(id, { ...p.input, reason: reason.trim() || "preview only", source: source.trim() }));
    } catch (e) {
      setError(message(e, "Could not show the effect."));
    }
    setBusy(false);
  }

  async function save() {
    if (!pending || !list) return;
    setBusy(true);
    setError(null);
    try {
      if (pending.kind === "sidecar") await importSidecar(id, pending.doc, list.last, sentence.trim() || undefined);
      else await saveCorrection(id, { ...pending.input, reason: reason.trim(), source: source.trim(), expect: list.last });
      reset();
      setRows([{ key: "", value: "" }]);
      load();
      onChanged?.();
    } catch (e) {
      setError(message(e, "The correction was not saved."));
    }
    setBusy(false);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    const inp = setInput();
    if (typeof inp === "string") { setError(inp); return; }
    void show({ kind: "input", input: inp, title: "Correct what the file says" });
  }

  async function readFile(e: ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f) return;
    if (f.size > 64 * 1024) { setError("A corrections file is small JSON (under 64 KB); this one is larger."); return; }
    try {
      const doc: unknown = JSON.parse(await f.text());
      void show({ kind: "sidecar", doc, name: f.name });
    } catch {
      setError("That file is not JSON.");
    }
  }

  const sidecar = preview && "match" in preview ? preview : null;
  const corr = preview && !("match" in preview) ? preview : null;
  const refused = preview?.preview.refused ?? null;
  const needsReason = pending?.kind === "input";
  const needsSentence = sidecar !== null && !sidecar.applies;
  const canSave = !busy && preview !== null && !refused && (!needsReason || reason.trim().length >= 3) && (!needsSentence || sentence.trim().length >= 10);

  return (
    <div className={s.wrap}>
      <h3 className={s.title}>Corrections</h3>
      <p className={s.note}>Correct a unit the file states, or WCS keywords, with a reason. The file is never changed: the corrected values are applied in memory, and the file&apos;s own values stay visible beside them.</p>

      {current ? (
        <div className={s.box}>
          <div className={s.row}><strong>In force</strong> <TierBadge tier={current.tier} /></div>
          {stale ? <p role="status" className={s.warn}>The file has changed since these corrections were made, so they are not applied. Confirm that they still apply to the file as it is, or withdraw them.</p> : null}
          <div className={s.scroller} role="region" aria-label="Corrections in force" tabIndex={0}>
            <table className={s.table}>
              <thead><tr><th scope="col">Keyword</th><th scope="col">The file says</th><th scope="col">Corrected to</th><th scope="col">Reason</th></tr></thead>
              <tbody>
                {current.state.unit ? <tr><th scope="row" className="mono">BUNIT</th><td className="mono">{valueText(current.state.unit.file)}</td><td className="mono">{valueText(current.state.unit.value)}</td><td>{current.state.unit.reason}{current.state.unit.source ? <span className={s.file}> · {current.state.unit.source}</span> : null}</td></tr> : null}
                {Object.entries(current.state.wcs ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([k, e]) => (
                  <tr key={k}><th scope="row" className="mono">{k}</th><td className="mono">{valueText(e.file)}</td><td className="mono">{valueText(e.value)}</td><td>{e.reason}{e.source ? <span className={s.file}> · {e.source}</span> : null}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className={s.actions}>
            {stale ? <Button type="button" variant="primary" disabled={busy} onClick={() => void show({ kind: "input", input: { action: "confirm" }, title: "Confirm for the file as it is" })}>They still apply…</Button> : null}
            <Button type="button" disabled={busy} onClick={() => void show({ kind: "input", input: { action: "withdraw" }, title: "Withdraw every correction" })}>Withdraw all…</Button>
          </div>
        </div>
      ) : list ? <p className={s.note}>Nothing is corrected: every value is the file&apos;s own.</p> : null}

      {!stale ? (
        <form className={s.form} onSubmit={submit} aria-label="Correct a value">
          {rows.map((r, i) => (
            <div key={i} className={s.pair}>
              <Select label={i === 0 ? "Keyword" : `Keyword ${i + 1}`} value={r.key} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}>
                <option value="">Choose…</option>
                {keywords.map((k) => <option key={k} value={k}>{k}{k === "BUNIT" ? " (brightness unit)" : ""}</option>)}
              </Select>
              <Input label={r.key ? `New ${r.key}` : "New value"} value={r.value} spellCheck={false} autoComplete="off" maxLength={40} inputMode={r.key && !textKeyword(r.key) ? "decimal" : undefined} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
            </div>
          ))}
          <div className={s.actions}>
            {rows.length < 8 ? <Button type="button" onClick={() => setRows([...rows, { key: "", value: "" }])}>Add a keyword</Button> : null}
            <Button type="submit" variant="primary" disabled={busy}>Show the effect</Button>
          </div>
          {!list?.unitCorrectable ? <p className={s.note}>{list?.fileBunit ? "This map's values are angles, so its unit cannot be corrected." : "The file has no BUNIT: declare its unit in “Unit of the values” instead. A correction replaces a unit the file states."}</p> : null}
        </form>
      ) : null}

      {pending ? (
        <section className={s.box} aria-label="Before saving">
          <div className={s.row}><strong>{pending.kind === "sidecar" ? `Import ${pending.name}` : pending.title}</strong></div>
          {busy && !preview ? <p role="status" className={s.note}>Asking the worker…</p> : null}
          {sidecar ? <p className={sidecar.applies ? s.note : s.warn}>This corrections file was {sidecar.matchText}.</p> : null}
          {preview ? <ChangeList changes={preview.changes} /> : null}
          {refused ? <p role="alert" className={s.err}>Refused: {refused}</p> : null}
          {preview && !refused ? <Coordinates before={preview.preview.before} after={preview.preview.after ?? null} /> : null}
          {preview?.preview.bunit_after && preview.preview.file_bunit && preview.preview.bunit_after !== preview.preview.file_bunit ? <p className={s.note}>Brightness unit after: <span className="mono">{preview.preview.bunit_after}</span> (the file says <span className="mono">{preview.preview.file_bunit}</span>).</p> : null}
          {preview ? <ImpactList impact={preview.impact} /> : null}
          {needsReason ? (
            <>
              <Input label="Reason (required)" value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
              {corr?.action === "set" ? <Input label="Source (optional): a log, a paper or a DOI" value={source} maxLength={300} onChange={(e) => setSource(e.target.value)} /> : null}
            </>
          ) : null}
          {needsSentence ? <Input label="Why it applies to this different file (required)" value={sentence} maxLength={500} onChange={(e) => setSentence(e.target.value)} /> : null}
          <div className={s.actions}>
            <Button type="button" variant="primary" disabled={!canSave} onClick={() => void save()}>{pending.kind === "sidecar" ? (needsSentence ? "Apply to this different file" : "Apply") : "Save as a new record"}</Button>
            <Button type="button" disabled={busy} onClick={reset}>Cancel</Button>
          </div>
        </section>
      ) : null}

      {error ? <p role="alert" className={s.err}>{error}</p> : null}

      <History records={list?.history ?? []} busy={busy} onRevert={(r) => void show({ kind: "input", input: { action: "revert", reverts: r.id }, title: `Revert “${ACTION_TEXT[r.action].toLowerCase()}” of ${day(r.at)}` })} />

      <div className={s.actions}>
        {list && list.history.length > 0 ? <a className={s.link} href={sidecarURL(id)} download>Export corrections (JSON)</a> : null}
        <label className={s.file}>
          <span className={s.link}>Import a corrections file…</span>
          <input type="file" accept="application/json,.json" className={s.hidden} onChange={(e) => void readFile(e)} />
        </label>
      </div>
    </div>
  );
}

/** The append-only history, newest first. Nothing in it is ever edited or deleted; a revert is a new record. */
function History({ records, busy, onRevert }: { records: CorrectionRecord[]; busy: boolean; onRevert: (r: CorrectionRecord) => void }) {
  if (records.length === 0) return null;
  const newest = [...records].reverse();
  return (
    <section aria-label="History of corrections">
      <h4 className={s.h}>History</h4>
      <ol className={s.history}>
        {newest.map((r, i) => (
          <li key={r.id} className={s.record}>
            <div className={s.row}>
              <strong>{ACTION_TEXT[r.action]}</strong>
              <span className={s.file}>{day(r.at)} · {r.author === "user" ? "by you" : r.author}</span>
              {r.stale ? <span className={s.warn}>made for an earlier version of the file</span> : null}
            </div>
            <ChangeList changes={r.changes} />
            <p className={s.reason}>{r.reason}{r.source ? <span className={s.file}> · {r.source}</span> : null}</p>
            {(r.action === "set" || r.action === "import" || r.action === "revert") && r.changes.length > 0 && i < newest.length ? (
              <div><Button type="button" disabled={busy} onClick={() => onRevert(r)}>Revert this…</Button></div>
            ) : null}
          </li>
        ))}
      </ol>
    </section>
  );
}
