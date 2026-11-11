import { useCallback, useEffect, useRef, useState } from "react";
import { Plus } from "lucide-react";
import { ApiError } from "../api/http";
import { importApply, importPreview, importRules, saveImportRules, writeImportRules } from "../api/projects";
import type { ImportApplyReply, ImportGroup, ImportPreview, ImportRule, ImportRulesState, ProjectSummary } from "../api/projects";
import { bytes } from "../catalog/format";
import { Button, Input } from "../ui";
import { RuleEditor } from "./RuleEditor";
import { blankRule, freshName, roleLabel, sameRules, tidy } from "./importFormat";
import { num } from "./format";
import s from "./Projects.module.css";

const DEBOUNCE_MS = 350;

const fileNote: Record<ImportRulesState["file"]["state"], string> = {
  none: "Not in .vesqira/project.toml yet.",
  same: "The same rules are in .vesqira/project.toml.",
  differs: "The rules in .vesqira/project.toml differ from the saved ones.",
  foreign: "project.toml has [[import.rules]] you wrote there; Vesqira does not rewrite them.",
};

/** One group of the scan preview: files of one extension with one proposed role and one author, and why. */
function GroupRow({ g, onOpen, open }: { g: ImportGroup; onOpen: () => void; open: boolean }) {
  const more = g.count - g.samples.length;
  return (
    <li className={s.group}>
      <div className={s.groupHead}>
        <span className={s.groupRole}>{roleLabel(g.role)}</span>
        <span className="mono">{g.ext}</span>
        <span className={s.dim}>{num(g.count)} {g.count === 1 ? "file" : "files"} · {bytes(g.bytes)}</span>
        <span className={g.by === "built-in" ? s.dim : s.byRule}>{g.by === "built-in" ? "built-in rule" : `your rule ${g.by}`}</span>
        <span className={s.spacer} />
        <button type="button" className={s.link} aria-expanded={open} onClick={onOpen}>{open ? "Show fewer" : "Show files"}</button>
      </div>
      <p className={s.dim}>{g.why}</p>
      {open ? (
        <ul className={s.paths} aria-label={`Files: ${roleLabel(g.role)} ${g.ext}`}>
          {g.samples.map((p) => (
            <li key={p.path}>
              <span className="mono">{p.path}</span>
              {p.declares && p.declares.length > 0 ? <span className={s.dim}> · {p.declares.join("; ")}</span> : null}
              {p.warnings && p.warnings.length > 0 ? <span className={s.warnText}> · {p.warnings.join("; ")}</span> : null}
              {p.matched && p.matched.length > 1 ? <span className={s.dim}> · also matched by {p.matched.slice(1).join(", ")}</span> : null}
            </li>
          ))}
          {more > 0 ? <li className={s.dim}>and {num(more)} more</li> : null}
        </ul>
      ) : null}
    </li>
  );
}

/**
 * Generic import (ADR 0058): a read-only scan preview of a folder (what the built-in rules recognise and what they do not, with the
 * role each file would get and why), the researcher's own rules with a live preview of what each matches, and an explicit apply.
 * Nothing here runs by itself; every declaration a rule makes is labelled as declared by that rule.
 */
export function ImportView({ project, version, onChanged }: { project: ProjectSummary; version: number; onChanged: () => void }) {
  const pid = project.id;
  const [folder, setFolder] = useState("");
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<ImportRule[] | null>(null);
  const [draft, setDraft] = useState<ImportRule[]>([]);
  const [fileState, setFileState] = useState<ImportRulesState["file"]["state"]>("none");
  const [problems, setProblems] = useState<string[]>([]);
  const [notice, setNotice] = useState("");
  const [expand, setExpand] = useState("");
  const [confirmWrite, setConfirmWrite] = useState(false);
  const [applying, setApplying] = useState(false);
  const [applied, setApplied] = useState<ImportApplyReply | null>(null);
  const latest = useRef(0);

  const dirty = saved !== null && !sameRules(saved, draft);

  const run = useCallback(
    async (rescan: boolean, rules: ImportRule[] | undefined, folderNow: string, expandNow: string) => {
      const n = ++latest.current;
      setBusy(true);
      try {
        const p = await importPreview(pid, { folder: folderNow, rescan, rules: rules?.map(tidy), expand: expandNow || undefined });
        if (n !== latest.current) return; // a newer request is on its way: this answer is stale
        setPreview(p);
        setError(null);
      } catch (e) {
        if (n !== latest.current) return;
        setError(e instanceof Error ? e.message : "Could not scan the folder.");
      } finally {
        if (n === latest.current) setBusy(false);
      }
    },
    [pid],
  );

  // the saved rules, then the first scan
  useEffect(() => {
    let live = true;
    importRules(pid)
      .then((st) => {
        if (!live) return;
        setSaved(st.rules);
        setDraft(st.rules);
        setFileState(st.file.state);
        void run(true, undefined, "", "");
      })
      .catch((e) => live && setError(e instanceof Error ? e.message : "Could not load the rules."));
    return () => {
      live = false;
    };
  }, [pid, run]);

  // a finished scan walks again; unsaved edits are kept
  const seen = useRef(version);
  useEffect(() => {
    if (seen.current === version) return;
    seen.current = version;
    if (saved !== null) void run(true, dirty ? draft : undefined, folder, expand);
  }, [version, saved, dirty, draft, folder, expand, run]);

  // editing a rule re-matches the walk already made (on the server: one matcher), after a short pause
  useEffect(() => {
    if (saved === null) return;
    const t = setTimeout(() => void run(false, dirty ? draft : undefined, folder, expand), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [draft, dirty, saved, folder, expand, run]);

  const change = (i: number, r: ImportRule) => {
    setDraft((d) => d.map((x, k) => (k === i ? r : x)));
    setNotice("");
    setProblems([]);
  };
  const move = (i: number, delta: -1 | 1) =>
    setDraft((d) => {
      const out = [...d];
      [out[i], out[i + delta]] = [out[i + delta]!, out[i]!];
      return out;
    });

  async function save() {
    setBusy(true);
    setProblems([]);
    try {
      const st = await saveImportRules(pid, draft.map(tidy));
      setSaved(st.rules);
      setDraft(st.rules);
      setFileState(st.file.state);
      setNotice(`Saved ${num(st.rules.length)} ${st.rules.length === 1 ? "rule" : "rules"} in the catalog. project.toml is unchanged.`);
      onChanged();
    } catch (e) {
      setProblems(e instanceof ApiError && e.reasons.length > 0 ? e.reasons : [e instanceof Error ? e.message : "Could not save the rules."]);
    } finally {
      setBusy(false);
    }
  }

  async function write() {
    setConfirmWrite(false);
    try {
      const r = await writeImportRules(pid);
      setFileState(r.rules === 0 ? "none" : "same");
      setNotice(r.rules === 0 ? `Removed the rules section from ${r.path}. Nothing else in the file changed and nothing was committed.` : `Wrote ${num(r.rules)} ${r.rules === 1 ? "rule" : "rules"} to ${r.path}. Nothing was committed.`);
      onChanged();
    } catch (e) {
      setNotice("");
      setProblems([e instanceof Error ? e.message : "Could not write project.toml."]);
    }
  }

  async function apply(paths?: string[], asNew?: string[]) {
    setApplying(true);
    setProblems([]);
    try {
      const r = await importApply(pid, { folder, paths, asNew });
      setApplied((cur) => (paths && cur ? { ...r, results: [...cur.results.filter((x) => !paths.includes(x.path)), ...r.results] } : r));
      onChanged();
      void run(false, undefined, folder, expand);
    } catch (e) {
      setProblems([e instanceof Error ? e.message : "Could not apply the rules."]);
    } finally {
      setApplying(false);
    }
  }

  const pending = preview?.selected.pending ?? 0;
  const canApply = saved !== null && !dirty && !applying && pending > 0;
  const notDone = applied?.results.filter((r) => r.outcome !== "registered" && r.outcome !== "declared") ?? [];
  const summary = project.importRules;

  return (
    <div className={s.root}>
      <div className={s.page}>
        <div className={s.head}>
          <h2 className={s.pageTitle}>Import rules</h2>
          <span className={s.dim}>{project.name}</span>
        </div>
        <p className={s.note}>
          Vesqira lists what its built-in rules recognise and what they do not. Your rules map a path to a role and to declarations about the files (a unit, an angle frame, catalog columns).
          What a rule declares is shown as declared by that rule, never as the file's own. Nothing is registered until you press Register.
        </p>
        {summary && summary.rules > 0 && (summary.new > 0 || summary.pending > 0) ? (
          <p className={s.saved} role="status">{summary.new > 0 ? `${num(summary.new)} new ${summary.new === 1 ? "file matches" : "files match"} your rules.` : `${num(summary.pending)} ${summary.pending === 1 ? "file matches" : "files match"} your rules and ${summary.pending === 1 ? "is" : "are"} not registered yet.`}</p>
        ) : null}

        <section className={s.card} aria-labelledby="imp-scan">
          <h3 id="imp-scan" className={s.cardTitle}>Scan preview</h3>
          <form className={s.inline} onSubmit={(e) => { e.preventDefault(); void run(true, dirty ? draft : undefined, folder, expand); }}>
            <Input label="Folder inside the project (empty: the whole project)" value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="validation_output/rohsa" spellCheck={false} />
            <Button type="submit" disabled={busy}>Scan</Button>
          </form>
          {error ? <p role="alert" className={s.err}>{error}</p> : null}
          {preview ? (
            <>
              <p className={s.note} role="status">
                {num(preview.files)} {preview.files === 1 ? "file" : "files"} listed in {preview.folder || "the project folder"}
                {preview.truncated ? <span className={s.warnText}> · incomplete: {preview.truncatedWhy}</span> : null}
                {preview.othersTruncated ? <span className={s.warnText}> · more unrecognised files exist than are listed</span> : null}
                {preview.rulesFrom === "draft" ? <span className={s.dim}> · matched against your unsaved edits</span> : null}
              </p>
              <ul className={s.groups} aria-label="Files by role and extension">
                {preview.groups.map((g) => {
                  const key = `${g.role}|${g.ext}|${g.by}`;
                  return <GroupRow key={key} g={g} open={expand === key} onOpen={() => setExpand(expand === key ? "" : key)} />;
                })}
              </ul>
              <p className={s.dim}>
                Skipped: {num(preview.skipped.dirCount)} folder{preview.skipped.dirCount === 1 ? "" : "s"} (caches, environments, your skip globs) · {num(preview.skipped.archiveCount)} archive{preview.skipped.archiveCount === 1 ? "" : "s"} listed, never opened ·{" "}
                {num(preview.skipped.neverRead.length)} never-read name{preview.skipped.neverRead.length === 1 ? "" : "s"} · {num(preview.skipped.links)} link{preview.skipped.links === 1 ? "" : "s"} not followed
              </p>
            </>
          ) : busy ? <p className={s.note}>Reading the folder…</p> : null}
        </section>

        <section className={s.card} aria-labelledby="imp-rules">
          <h3 id="imp-rules" className={s.cardTitle}>Your rules</h3>
          <p className={s.note}>The first matching rule decides, so the order is the priority. Paths are relative to the project folder. A rule only sees files the scan listed.</p>
          {draft.length === 0 && saved !== null ? <p className={s.note}>No rules yet.</p> : null}
          <ol className={s.rules} aria-label="Rules in priority order">
            {draft.map((r, i) => (
              <RuleEditor key={i} rule={r} index={i} count={draft.length} report={preview?.rules[i]} onChange={(x) => change(i, x)} onMove={(d) => move(i, d)} onRemove={() => setDraft((d) => d.filter((_, k) => k !== i))} />
            ))}
          </ol>
          {preview && preview.conflictCount > 0 ? (
            <div className={s.conflicts} role="status">
              <p className={s.warnNote}>{num(preview.conflictCount)} {preview.conflictCount === 1 ? "file matches" : "files match"} more than one rule; the first rule decides.</p>
              <ul className={s.paths} aria-label="Files matched by more than one rule">
                {preview.conflicts.map((c) => (
                  <li key={c.path}><span className="mono">{c.path}</span>: {c.rules.join(" > ")} · {c.differ ? "they would say different things" : "they say the same"}</li>
                ))}
                {preview.conflictCount > preview.conflicts.length ? <li className={s.dim}>and {num(preview.conflictCount - preview.conflicts.length)} more</li> : null}
              </ul>
            </div>
          ) : null}
          <div className={s.actions}>
            <Button icon={Plus} onClick={() => setDraft((d) => [...d, { ...blankRule(d.length + 1), name: freshName(d) }])}>Add a rule</Button>
            <Button variant="primary" disabled={!dirty || busy} onClick={() => void save()}>Save rules</Button>
            {dirty ? <span className={s.dim}>Unsaved edits: the preview above uses them; Register uses the saved rules.</span> : null}
          </div>
          {problems.length > 0 ? <div role="alert"><ul className={s.ruleProblems} aria-label="Problems">{problems.map((p) => <li key={p}>{p}</li>)}</ul></div> : null}
          <p className={s.saved} role="status">{notice}</p>

          <div className={s.rowBlock}>
            <p className={s.note}>{fileNote[fileState]}</p>
            {saved && !dirty && fileState !== "same" && fileState !== "foreign" && (saved.length > 0 || fileState === "differs") ? (
              confirmWrite ? (
                <div className={s.actions}>
                  <span className={s.note}>{saved.length === 0 ? "Remove the rules section from .vesqira/project.toml?" : "Add the rules to .vesqira/project.toml?"} Only a marked section of that file changes. It is never committed.</span>
                  <Button variant="primary" onClick={() => void write()}>{saved.length === 0 ? "Remove the rules" : "Write the rules"}</Button>
                  <Button onClick={() => setConfirmWrite(false)}>Cancel</Button>
                </div>
              ) : <div><Button onClick={() => setConfirmWrite(true)}>{saved.length === 0 ? "Remove the rules from project.toml…" : "Write the rules to project.toml…"}</Button></div>
            ) : null}
          </div>
        </section>

        <section className={s.card} aria-labelledby="imp-apply">
          <h3 id="imp-apply" className={s.cardTitle}>Register what your rules select</h3>
          {preview ? (
            <p className={s.note}>
              {num(preview.selected.files)} {preview.selected.files === 1 ? "file is" : "files are"} selected by your saved rules; {num(preview.selected.pending)} not applied yet.
              {preview.selected.files > 0 ? ` ${Object.entries(preview.selected.byRole).map(([r, n]) => `${num(n)} ${roleLabel(r).toLowerCase()}`).join(" · ")}` : ""}
            </p>
          ) : null}
          <p className={s.dim}>
            Files go through the usual registration. A file whose content a dataset already holds is offered, not registered twice. A declaration of yours is never replaced,
            and a file's own BUNIT always wins over a unit.
          </p>
          <div className={s.actions}>
            <Button variant="primary" disabled={!canApply} onClick={() => void apply()}>
              {applying ? "Registering…" : `Register ${num(pending)} ${pending === 1 ? "file" : "files"}`}
            </Button>
            {dirty ? <span className={s.dim}>Save the rules first.</span> : null}
          </div>
          {applied ? (
            <div role="status" aria-label="Import summary">
              <p className={s.note}>
                Registered {num(applied.summary.registered)} · declared on {num(applied.summary.declared)} already registered · {num(applied.summary.duplicates)} offered as duplicates · {num(applied.summary.skipped)} skipped · {num(applied.summary.failed)} failed
                {applied.summary.remaining > 0 ? ` · ${num(applied.summary.remaining)} left: press Register again` : ""}
              </p>
              {notDone.length > 0 ? (
                <ul className={s.paths} aria-label="Files not registered, and why">
                  {notDone.map((r) => (
                    <li key={r.path}>
                      <span className="mono">{r.path}</span> · {r.outcome} · {r.detail}
                      {r.outcome === "duplicate" ? <> <Button disabled={applying} onClick={() => void apply([r.path], [r.path])}>Register separately</Button></> : null}
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}
        </section>
      </div>
    </div>
  );
}
