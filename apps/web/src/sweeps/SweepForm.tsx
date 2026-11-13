import { useEffect, useState } from "react";
import { Button, Input, Select } from "../ui";
import type { Job, JobTemplate } from "../api/jobs";
import { createSweep, gib, parseValues, previewSweep } from "../api/sweeps";
import { formatDuration } from "../ui/duration";
import type { Sweep, SweepPreview, SweepRequest } from "../api/sweeps";
import { PIN_RERUN_NOTE, sweepKeys } from "../jobs/templateForm";
import s from "./Sweeps.module.css";

type Row = { key: string; label: string; values: string };
const MAX_PARAMS = 3;

/** "Sweep over…": one to three settings, each an explicit list of values; shows how many jobs and what they request before drafting them. Nothing is submitted. */
export function SweepForm({ jobs, templates, baseJobId, onCreated }: { jobs: Job[]; templates: JobTemplate[] | null; baseJobId?: number | null; onCreated: (s: Sweep) => void }) {
  const bases = jobs.filter((j) => j.template === "vgt" || j.template === "rohsa");
  const [base, setBase] = useState<string>(baseJobId ? String(baseJobId) : bases[0] ? String(bases[0].id) : "");
  const template = bases.find((j) => String(j.id) === base)?.template ?? bases[0]?.template ?? "";
  // what a sweep may vary is what the base job's tool profile declares (ADR 0068), not a list kept here
  const t = templates?.find((x) => x.id === template) ?? null;
  const KEYS = t ? sweepKeys(t) : [];
  // a row starts on the first plain setting; a tie (one setting for several keys) is a deliberate choice, not a default
  const firstKey = KEYS.find((k) => !(t?.ties ?? []).some((x) => x.name === k.key))?.key ?? KEYS[0]?.key ?? "";
  const [name, setName] = useState(`${template || "tool"}-sweep`);
  const [note, setNote] = useState("");
  const [rows, setRows] = useState<Row[]>([{ key: "", label: "", values: "" }]);
  const [pin, setPin] = useState<"base" | "on" | "off">("base");
  const [preview, setPreview] = useState<SweepPreview | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // a row starts on the template's first setting, and returns to it when its setting is not one the base's tool has
  useEffect(() => {
    if (KEYS.length === 0) return;
    setRows((cur) => (cur.every((r) => KEYS.some((k) => k.key === r.key)) ? cur : cur.map((r) => (KEYS.some((k) => k.key === r.key) ? r : { ...r, key: firstKey }))));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [template, templates]);

  const request = (): SweepRequest => ({
    name: name.trim(),
    baseJobId: Number(base),
    note: note.trim(),
    ...(pin === "base" ? {} : { pinProfileCommit: pin === "on" }),
    params: rows.map((r) => ({ key: r.key, label: r.label.trim(), values: parseValues(r.values) })),
  });
  const edit = (i: number, patch: Partial<Row>) => { setRows((cur) => cur.map((r, j) => (j === i ? { ...r, ...patch } : r))); setPreview(null); };
  const ready = base !== "" && t !== null && rows.every((r) => r.key !== "") && name.trim() !== "" && rows.every((r) => parseValues(r.values).length > 0);

  async function act<T>(f: () => Promise<T>, then: (r: T) => void) {
    setBusy(true);
    try { then(await f()); setErr(null); } catch (e) { setErr(e instanceof Error ? e.message : "That did not work."); }
    setBusy(false);
  }

  // changing the base to another kind of job starts the grid again: its settings are not the other's
  function changeBase(id: string) {
    const next = bases.find((j) => String(j.id) === id)?.template ?? "";
    setBase(id);
    setPreview(null);
    if (next !== template) {
      setRows([{ key: "", label: "", values: "" }]);
      setName(`${next}-sweep`);
      setPin("base");
    }
  }

  if (templates !== null && !t && bases.length > 0) return <p className={s.note}>No tool profile declares the job template of this base job, so there are no settings to sweep.</p>;
  if (bases.length === 0) return <p className={s.note}>A sweep starts from a VGT or ROHSA job that kept its settings. Make a job first (New job), then sweep from it.</p>;

  return (
    <form className={s.root} aria-label="New sweep" onSubmit={(e) => e.preventDefault()}>
      <h2 className={s.h}>New sweep</h2>
      <p className={s.note}>Repeats a job with a grid of settings: every combination becomes an ordinary draft job whose script you can read. Nothing is submitted until you approve the whole sweep.</p>
      <Select label="Base job (its settings, code folder and resources are repeated)" value={base} onChange={(e) => changeBase(e.target.value)}>
        {bases.map((j) => <option key={j.id} value={j.id}>{j.name} · {j.template.toUpperCase()} · {j.state}</option>)}
      </Select>
      {t && t.sizing === "rohsa_crop_table" ? <p className={s.note}>ROHSA jobs share one source folder on the host, so the sweep is approved to run one job at a time unless you raise that (the approval says why). Every draft requests what the base job did: the approval shows what your sizing table suggests for each N.</p> : null}
      {t ? (
        <>
          <Select label="Code commit" value={pin} onChange={(e) => { setPin(e.target.value as "base" | "on" | "off"); setPreview(null); }}>
            <option value="base">As the base job did</option>
            <option value="on">{`Stop each job unless the folder is at commit ${t.repo.commit.slice(0, 7)} (the profile's)`}</option>
            <option value="off">Do not stop on the commit</option>
          </Select>
          <p className={s.note}>{PIN_RERUN_NOTE}</p>
        </>
      ) : null}
      <Input label="Sweep name (starts each job's name)" value={name} maxLength={32} onChange={(e) => { setName(e.target.value); setPreview(null); }} />
      <fieldset className={s.params}>
        <legend>Parameters</legend>
        <p className={s.note}>1 to {MAX_PARAMS} settings, each an explicit list of values; the first changes slowest.</p>
        {rows.map((r, i) => (
          <div key={i} className={s.prow}>
            <Select label={`Setting ${i + 1}`} description={KEYS.find((k) => k.key === r.key)?.description} value={r.key} onChange={(e) => edit(i, { key: e.target.value })}>
              {KEYS.map((k) => <option key={k.key} value={k.key}>{k.label}</option>)}
            </Select>
            <Input label="Label" value={r.label} maxLength={20} placeholder={r.key} onChange={(e) => edit(i, { label: e.target.value })} />
            <Input label="Values (comma separated)" value={r.values} placeholder="8, 12, 16" spellCheck={false} onChange={(e) => edit(i, { values: e.target.value })} />
          </div>
        ))}
        <div className={s.row}>
          <Button type="button" disabled={rows.length >= MAX_PARAMS} onClick={() => { setRows([...rows, { key: KEYS.find((k) => !rows.some((r) => r.key === k.key))?.key ?? KEYS[0]?.key ?? "", label: "", values: "" }]); setPreview(null); }}>Add a parameter</Button>
          {rows.length > 1 ? <Button type="button" onClick={() => { setRows(rows.slice(0, -1)); setPreview(null); }}>Remove the last</Button> : null}
        </div>
      </fieldset>
      <Input label="Why (kept with every draft)" value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} />
      <div className={s.row}>
        <Button type="button" disabled={busy || !ready} onClick={() => act(() => previewSweep(request()), setPreview)}>Count the jobs</Button>
      </div>
      {preview ? <Facts preview={preview} /> : <p className={s.note}>First count the jobs: that shows how many drafts the grid makes and what they request. Then create them.</p>}
      <div className={s.row}>
        <Button type="button" variant="primary" disabled={busy || !ready || !preview} onClick={() => act(() => createSweep(request()), onCreated)}>{preview ? `Create ${preview.count} drafts` : "Create drafts"}</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </form>
  );
}

function Facts({ preview }: { preview: SweepPreview }) {
  const t = preview.totals;
  const e = preview.estimate;
  return (
    <ul className={s.facts} aria-label="What the sweep would make">
      <li><strong>{preview.count} jobs</strong> (at most {preview.max}), as drafts: {preview.jobs.slice(0, 3).map((j) => j.label).join("; ")}{preview.jobs.length > 3 ? "; …" : ""}</li>
      <li>Each requests {t.cpusEach} CPUs, {gib(t.memBytesEach)}, up to {formatDuration(t.timeLimitSecondsEach)}; together {t.cpusTotal} CPUs, {gib(t.memBytesTotal)} and {formatDuration(t.timeLimitSecondsTotal)} of time limits.</li>
      {e ? (
        <li>
          {e.baseJobName} took {formatDuration(e.elapsedSeconds)}{e.maxRssBytes !== null ? ` and peaked at ${gib(e.maxRssBytes)}` : ""}. If each job took as long, {preview.count} in sequence would take about {formatDuration(e.elapsedSeconds * preview.count)}. {e.basis}.
        </li>
      ) : <li>The base job has no measured use, so no estimate is offered.</li>}
    </ul>
  );
}
