import { useCallback, useEffect, useId, useState } from "react";
import { answerNeed, projectNeeds } from "../api/projects";
import type { Need, NeedOption, ProjectSummary } from "../api/projects";
import { Button, Switch } from "../ui";
import { num } from "./format";
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
          Answered: {need.answer!.answer === "unknown" ? "left unknown" : `${need.answer!.answer}${need.answer!.value ? ` ${need.answer!.value}` : ""}`} · declared by you, {need.answer!.date.slice(0, 10)}
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

  const open = (needs ?? []).filter((n) => n.state === "open");
  const done = (needs ?? []).filter((n) => n.state === "answered");
  const list = all ? needs ?? [] : open;
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
          {list.map((n) => (
            <NeedCard key={n.id} need={n} project={project} note={notes[n.id]} onAnswered={(r, note) => answered(r, n.id, note)} />
          ))}
        </ul>
      </div>
    </div>
  );
}
