import { useCallback, useEffect, useId, useState } from "react";
import { answerNeed, answerTree, projectNeeds } from "../api/projects";
import type { Need, NeedOption, ProjectSummary, TreeAnswer } from "../api/projects";
import { Button, Switch } from "../ui";
import { num } from "./format";
import { groupNeeds } from "./needGroups";
import type { NeedGroup } from "./needGroups";
import s from "./Projects.module.css";

const WRITTEN = "Stored in project.toml as declared by you";

function NeedCard({ need, project, note, onAnswered }: { need: Need; project: ProjectSummary; note?: string; onAnswered: (n: Need | null, note: string) => void }) {
  const id = useId();
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [showPaths, setShowPaths] = useState(false);
  const textOption = need.answers.find((o) => o.input === "text");
  const more = need.scope.count - need.scope.paths.length;

  async function submit(o: NeedOption, value: string) {
    setBusy(true);
    setErr(null);
    try {
      const r = await answerNeed(project.id, need.id, o.id, value);
      onAnswered(r.need, r.writtenToProject ? WRITTEN : r.note || "Stored in the catalog as declared by you; not written to project.toml.");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not save the answer.");
    } finally {
      setBusy(false);
    }
  }

  const answered = need.state === "answered" && need.answer;
  return (
    <li className={s.need} aria-labelledby={`${id}-m`}>
      <p id={`${id}-m`} className={s.needMessage}>{need.message}</p>
      <p className={s.dim}>source: {need.source}</p>
      <div className={s.scope}>
        <span>
          {need.scope.folder ? <span className="mono">{need.scope.folder}</span> : null}
          {need.scope.folder ? " · " : ""}
          {num(need.scope.count)} {need.scope.count === 1 ? "item" : "items"}
        </span>
        {need.scope.paths.length > 0 ? (
          <button type="button" className={s.link} aria-expanded={showPaths} onClick={() => setShowPaths(!showPaths)}>{showPaths ? "Hide list" : "Show list"}</button>
        ) : null}
      </div>
      {showPaths ? (
        <ul className={s.paths} aria-label="Items this applies to">
          {need.scope.paths.map((p) => <li key={p} className="mono">{p}</li>)}
          {more > 0 ? <li className={s.dim}>and {num(more)} more</li> : null}
        </ul>
      ) : null}

      {answered ? (
        <p className={s.answered} role="status">
          Answered: {need.answer!.answer === "unknown" ? "left unknown" : `${need.answer!.answer}${need.answer!.value ? ` ${need.answer!.value}` : ""}`} · declared by {need.answer!.declaredBy}, {need.answer!.date.slice(0, 10)}
          <span className={s.dim}> · {note ?? (need.answer!.writtenToProject ? WRITTEN : "stored in the catalog, not written to project.toml")}</span>
        </p>
      ) : (
        <>
          {need.suggestion && textOption ? (
            <p className={s.suggest}>
              Suggestion: <span className="mono">{need.suggestion}</span>{" "}
              <Button onClick={() => setValues((v) => ({ ...v, [textOption.id]: need.suggestion ?? "" }))}>Use</Button>
              <span className={s.dim}> (fills the box; nothing is saved until you press {textOption.label})</span>
            </p>
          ) : null}
          <div className={s.answers}>
            {need.answers.map((o) =>
              o.input === "text" ? (
                <form key={o.id} className={s.answerText} onSubmit={(e) => { e.preventDefault(); if ((values[o.id] ?? "").trim()) void submit(o, (values[o.id] ?? "").trim()); }}>
                  <label htmlFor={`${id}-${o.id}`} className={s.areaLabel}>{o.label}</label>
                  {o.hint ? <span id={`${id}-${o.id}-h`} className={s.dim}>{o.hint}</span> : null}
                  <div className={s.inline}>
                    <input id={`${id}-${o.id}`} className={s.text} value={values[o.id] ?? ""} onChange={(e) => setValues((v) => ({ ...v, [o.id]: e.target.value }))} spellCheck={false} aria-describedby={o.hint ? `${id}-${o.id}-h` : undefined} />
                    <Button type="submit" variant="primary" disabled={busy || !(values[o.id] ?? "").trim()}>Declare</Button>
                  </div>
                </form>
              ) : o.input === "choice" ? (
                (o.choices ?? []).map((c) => <Button key={`${o.id}-${c}`} disabled={busy} onClick={() => void submit(o, c)}>{c}</Button>)
              ) : (
                <Button key={o.id} disabled={busy} onClick={() => void submit(o, "")}>{o.label}</Button>
              ),
            )}
          </div>
        </>
      )}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </li>
  );
}

const plural = (n: number, one: string, many = one + "s") => `${num(n)} ${n === 1 ? one : many}`;

/**
 * Many "no BUNIT" questions under one top-level folder, answered once. The scope is chosen from folders the group really
 * has, a preview says how many files the answer would reach (and that answered folders are left alone), and only the
 * confirmation changes anything. The answer is stored per folder as yours, for the folder tree.
 */
function NeedGroupCard({ group, project, onApplied, onAnswered, notes }: { group: NeedGroup; project: ProjectSummary; onApplied: (r: TreeAnswer) => void; onAnswered: (n: Need | null, id: string, note: string) => void; notes: Record<string, string> }) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [scope, setScope] = useState(group.ancestor);
  const [unit, setUnit] = useState("");
  const [pending, setPending] = useState<{ answer: "declare" | "unknown"; value: string; preview: TreeAnswer } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const where = scope === "." ? "the project" : scope;
  const reach = group.scopes.find((c) => c.folder === scope) ?? group.scopes[0]!;

  async function ask(answer: "declare" | "unknown", value: string) {
    setBusy(true);
    setErr(null);
    try {
      setPending({ answer, value, preview: await answerTree(project.id, scope, answer, value, false) });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not count the files.");
    } finally {
      setBusy(false);
    }
  }
  async function confirm() {
    if (!pending) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await answerTree(project.id, scope, pending.answer, pending.value, true);
      setPending(null);
      onApplied(r);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not save the answers.");
    } finally {
      setBusy(false);
    }
  }
  const p = pending?.preview;
  const said = pending ? (pending.answer === "unknown" ? "left with no unit" : `declared as ${pending.value}`) : "";
  return (
    <li className={s.need} aria-labelledby={`${id}-m`}>
      <p id={`${id}-m`} className={s.needMessage}>
        {plural(group.files, "FITS file")} in {plural(group.folders, "folder")} under <span className="mono">{group.ancestor === "." ? group.key : group.ancestor}</span> have no BUNIT.
      </p>
      <p className={s.dim}>source: the FITS headers: no BUNIT card. One answer for the folder tree is stored for each folder as declared by you, for that tree; the files are not changed.</p>
      <div className={s.scope}>
        <button type="button" className={s.link} aria-expanded={open} aria-controls={`${id}-list`} onClick={() => setOpen(!open)}>
          {open ? "Hide the folders" : `Show the ${plural(group.folders, "folder")} one by one`}
        </button>
      </div>
      {!pending ? (
        <div className={s.answerText}>
          {group.scopes.length > 1 ? (
            <label className={s.areaLabel}>
              Answer for
              <select className={s.text} value={scope} onChange={(e) => setScope(e.target.value)}>
                {group.scopes.map((c) => <option key={c.folder} value={c.folder}>{c.folder === "." ? "the whole project" : c.folder} ({plural(c.folders, "folder")}, {plural(c.files, "file")})</option>)}
              </select>
            </label>
          ) : null}
          <form className={s.answerText} onSubmit={(e) => { e.preventDefault(); if (unit.trim()) void ask("declare", unit.trim()); }}>
            <label htmlFor={`${id}-u`} className={s.areaLabel}>Every file under {where} without BUNIT is in</label>
            <span id={`${id}-h`} className={s.dim}>You see how many files this reaches before anything is saved. Folders you already answered are left as they are.</span>
            <div className={s.inline}>
              <input id={`${id}-u`} className={s.text} value={unit} onChange={(e) => setUnit(e.target.value)} spellCheck={false} aria-describedby={`${id}-h`} />
              <Button type="submit" variant="primary" disabled={busy || !unit.trim()}>Count the files</Button>
              <Button disabled={busy} onClick={() => void ask("unknown", "")}>Leave unknown for all</Button>
            </div>
          </form>
          <span className={s.dim}>Reaches {plural(reach.folders, "folder")}, {plural(reach.files, "file")}.</span>
        </div>
      ) : (
        <div role="group" aria-label="Confirm the answer for the folder tree" className={s.answerText}>
          <p className={s.needMessage} role="status">
            {p!.folders === 0
              ? `Nothing under ${where} is left to answer.`
              : `${plural(p!.files, "file")} in ${plural(p!.folders, "folder")} under ${where} would be ${said}. Each folder is stored as yours, for this folder tree.`}
            {p!.answered > 0 ? ` ${plural(p!.answered, "folder")} you already answered ${p!.answered === 1 ? "is" : "are"} left as ${p!.answered === 1 ? "it is" : "they are"}.` : ""}
          </p>
          <div className={s.inline}>
            <Button variant="primary" disabled={busy || p!.folders === 0} onClick={() => void confirm()}>Confirm for {plural(p!.folders, "folder")}</Button>
            <Button disabled={busy} onClick={() => setPending(null)}>Cancel</Button>
          </div>
        </div>
      )}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {open ? (
        <ul id={`${id}-list`} className={s.needs} aria-label={`Folders under ${group.key}`}>
          {group.needs.map((n) => <NeedCard key={n.id} need={n} project={project} note={notes[n.id]} onAnswered={(r, note) => onAnswered(r, n.id, note)} />)}
        </ul>
      ) : null}
    </li>
  );
}

/** Screen 4: everything Vesqira could not know. Answers are yours, stored as declared by you; nothing is prefilled. */
export function NeedsView({ project, version, onChanged }: { project: ProjectSummary; version: number; onChanged: () => void }) {
  const [needs, setNeeds] = useState<Need[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [all, setAll] = useState(false);
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setNeeds((await projectNeeds(project.id)).needs);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load the questions.");
    }
  }, [project.id]);
  useEffect(() => {
    void load();
  }, [load, version]);

  function answered(need: Need | null, id: string, note: string) {
    // the answered item leaves the open list at once; "Show answered" brings it back with its note
    setNeeds((cur) => (cur ?? []).map((n) => (n.id === id ? (need ?? { ...n, state: "answered" }) : n)));
    setNotes((cur) => ({ ...cur, [id]: note }));
    const sentence = note ? note[0]!.toUpperCase() + note.slice(1) : ""; // the server sends a fragment ("not written to the project: ...")
    setSaved(`Saved. ${sentence}.`.replace(/\.\.$/, "."));
    onChanged();
  }

  function appliedTree(r: TreeAnswer) {
    const kept = r.note ? ` ${r.note[0]!.toUpperCase()}${r.note.slice(1)}.` : "";
    const failed = r.failed?.length ? ` ${plural(r.failed.length, "folder")} could not be saved; answer again to retry.` : "";
    setSaved(`Saved for ${plural(r.folders, "folder")} under ${r.folder === "." ? "the project" : r.folder}, as declared by you.${r.writtenToProject ? " Stored in project.toml." : kept}${failed}`);
    onChanged();
    void load();
  }

  const open = (needs ?? []).filter((n) => n.state === "open");
  const done = (needs ?? []).filter((n) => n.state === "answered");
  const list = all ? needs ?? [] : open;
  const items = groupNeeds(list);
  return (
    <div className={s.root}>
      <div className={s.page}>
        <div className={s.head}>
          <h2 className={s.pageTitle}>Needs your input</h2>
          <span className={s.dim}>{needs ? `${num(open.length)} to answer` : ""}</span>
          <span className={s.spacer} />
          <Switch label={`Show answered${done.length ? ` (${num(done.length)})` : ""}`} checked={all} onChange={(e) => setAll(e.target.checked)} />
        </div>
        <p className={s.note}>Vesqira does not guess these. Each answer is stored as declared by you; "Leave unknown" is a valid answer and keeps the value unknown.</p>
        {error ? <p role="alert" className={s.err}>{error}</p> : null}
        <p className={s.saved} role="status">{saved}</p>
        {needs === null && !error ? <p className={s.note}>Loading…</p> : null}
        {needs !== null && list.length === 0 ? <p className={s.note}>{open.length === 0 && done.length > 0 ? "Everything is answered." : "Nothing to answer. Nothing found so far needs you."}</p> : null}
        <ul className={s.needs} aria-label="Questions">
          {items.map((it) =>
            it.kind === "group" ? (
              <NeedGroupCard key={`group:${it.group.key}`} group={it.group} project={project} notes={notes} onApplied={appliedTree} onAnswered={answered} />
            ) : (
              <NeedCard key={it.need.id} need={it.need} project={project} note={notes[it.need.id]} onAnswered={(r, note) => answered(r, it.need.id, note)} />
            ),
          )}
        </ul>
      </div>
    </div>
  );
}
