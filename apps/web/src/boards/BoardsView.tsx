import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Download, Printer, Trash2 } from "lucide-react";
import { Button, Checkbox, IconButton, Input, LinkButton, Select } from "../ui";
import { addToBoard, createBoard, figurePreviewUrl, getBoard, listBoards, listSnapshots, publish, revokeSnapshot, snapshotComments, ownerReply, preflight, OUTCOME_TEXT } from "../api/boards";
import { boardStack, moveItem, removeItem, resolveComment, setNote } from "../undo/boardCommands";
import { useUndoStack } from "../undo/useUndoStack";
import { useUndoKeys } from "../undo/keys";
import { UndoBar } from "../undo/UndoBar";
import { Threads } from "./Threads";
// loaded when someone presents: it brings the modal primitives (ADR 0033), which the rest of the app does not need
const PresentView = lazy(() => import("./PresentView").then((m) => ({ default: m.PresentView })));
import { markSeen, readSeen } from "./seen";
import type { Board, Comment, Figure, FigureItem, Preflight, Snapshot } from "../api/boards";
import type { NewerNotice } from "../api/catalog";
import { NewerNotices } from "../catalog/NewerNotices";
import { FigureRedraw } from "./FigureRedraw";
import { listDatasets } from "../api/catalog";
import type { Dataset } from "../api/catalog";
import { listRuns } from "../api/runs";
import type { Run } from "../api/runs";
import { SweepGrid } from "../sweeps/SweepGrid";
import { defaultOutput, JobFrame, JobLines, jobIsActive, LiveResult } from "./JobPanel";
import { EM } from "../catalog/format";
import { EntryActions } from "../catalog/EntryActions";
import { RunLine } from "./RunLine";
import { BoardCompare } from "./BoardCompare";
import s from "./BoardsView.module.css";

const BASE_KEY = "vesqira.reviewBase";
const readBase = () => {
  try {
    return localStorage.getItem(BASE_KEY) ?? "http://127.0.0.1:8788";
  } catch {
    return "http://127.0.0.1:8788";
  }
};

export function provenanceLine(p: Figure["provenance"]): string {
  const v = p.view;
  const parts = [p.source.name];
  if (v.channel !== undefined) parts.push(`channel ${v.channel + 1}`);
  if (v.kind) parts.push(v.kind);
  return parts.join(" · ");
}

/** One line over the figures when some of them were drawn from data that has a newer result or changed (ADR 0053). Never swaps anything. */
function BoardNewerLine({ board, newer }: { board: Board; newer: Record<string, NewerNotice[]> }) {
  const n = board.items.filter((it) => it.kind !== "job" && (newer[String(it.figure.id)]?.length ?? 0) > 0).length;
  if (n === 0) return null;
  return <p role="status" className={s.note}>{n === 1 ? "1 figure on this board was" : `${n} figures on this board were`} drawn from data that has a newer result or has changed. Each says what differs; nothing was redrawn or swapped.</p>;
}

/** Research board: a mutable draft of pinned figures. Sharing freezes it. */
export function BoardsView({ onCompare, newer }: { onCompare?: (datasetIdA: number, datasetIdB: number) => void; newer?: Record<string, NewerNotice[]> } = {}) {
  const [boards, setBoards] = useState<Board[]>([]);
  const [id, setId] = useState<number | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [snaps, setSnaps] = useState<Snapshot[]>([]);
  const [label, setLabel] = useState("");
  const [hours, setHours] = useState("336");
  const [link, setLink] = useState<string | null>(null);
  const [base, setBase] = useState(readBase);
  const [comments, setComments] = useState<Record<number, Comment[]>>({});
  const [question, setQuestion] = useState("");
  const [replaces, setReplaces] = useState(false);
  const [reason, setReason] = useState("");
  const [pre, setPre] = useState<Preflight | null>(null);
  const [presenting, setPresenting] = useState(false);
  const [showArchived, setShowArchived] = useState(false);
  const [ticked, setTicked] = useState<number[]>([]); // item positions ticked for "what differs", oldest first; at most two
  const now = Date.now(); // elapsed times are as of the last render; the 30 s poll refreshes them
  const [seenAt, setSeenAt] = useState<Record<number, number>>({}); // when each snapshot was last opened, before this visit

  const reload = useCallback(async (keep?: number | null) => {
    try {
      const bs = await listBoards(showArchived ? { archived: "all" } : {});
      // the list says where each job is but not how far; keep the progress already read until the next poll replaces it
      setBoards((prev) => bs.map((nb) => {
        const old = prev.find((x) => x.id === nb.id);
        return old ? { ...nb, items: nb.items.map((it) => {
          const was = old.items.find((o) => o.kind === "job" && it.kind === "job" && o.job.id === it.job.id);
          return it.kind === "job" && was?.kind === "job" && was.job.progress && jobIsActive(it.job) && !it.job.progress ? { ...it, job: { ...it.job, progress: was.job.progress } } : it;
        }) } : nb;
      }));
      setId((cur) => keep ?? cur ?? bs[0]?.id ?? null);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not load boards.");
    }
  }, [showArchived]);
  useEffect(() => {
    void reload();
  }, [reload]);
  const board = boards.find((b) => b.id === id) ?? null;
  const picked = board ? ticked.map((pos) => board.items.find((x) => x.position === pos)).filter((x): x is FigureItem => x !== undefined && x.kind !== "job") : [];

  // Panels that follow a job: the list carries the job's state, the single-board call also reads its log for progress.
  const replaceBoard = useCallback((b: Board) => setBoards((cur) => cur.map((x) => (x.id === b.id ? b : x))), []);
  const live = board?.items.some((it) => it.kind === "job" && jobIsActive(it.job)) ?? false;
  const boardId = board?.id ?? null;
  useEffect(() => {
    if (!live || boardId === null) return;
    let stopped = false;
    const poll = () => getBoard(boardId).then((b) => { if (!stopped) replaceBoard(b); }).catch(() => undefined); // the next poll tries again
    poll();
    const t = setInterval(poll, 30_000);
    return () => { stopped = true; clearInterval(t); };
  }, [live, boardId, replaceBoard]);

  // A job that became a run: the run and its dataset are needed to draw the result.
  const [lookup, setLookup] = useState<{ runs: Run[]; datasets: Dataset[] }>({ runs: [], datasets: [] });
  const wantRuns = board?.items.some((it) => it.kind === "job" && it.job.runId !== null && !lookup.runs.some((r) => r.id === it.job.runId)) ?? false;
  useEffect(() => {
    if (!wantRuns) return;
    let stopped = false;
    void Promise.all([listRuns(), listDatasets()]).then(([runs, datasets]) => { if (!stopped) setLookup({ runs, datasets }); }).catch((e: Error) => setErr(e.message));
    return () => { stopped = true; };
  }, [wantRuns, board?.items.length]); // eslint-disable-line react-hooks/exhaustive-deps
  // A third tick replaces the oldest one, so a keyboard user never meets a disabled box and has to untick first.
  const tick = (pos: number, on: boolean) => setTicked((cur) => (on ? [...cur.filter((x) => x !== pos), pos].slice(-2) : cur.filter((x) => x !== pos)));
  useEffect(() => {
    setLink(null);
    setTicked([]);
    if (board) listSnapshots(board.id).then(setSnaps).catch(() => setSnaps([]));
    else setSnaps([]);
  }, [board?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  // Undo and redo of this board's edits (ADR 0055). The stack outlives this screen; publishing and replies are not commands.
  const stack = board ? boardStack(board.id) : null;
  const undo = useUndoStack(stack);
  const afterUndo = async () => {
    await reload(id);
    const open = Object.keys(comments).map(Number); // threads on screen show the state the undo just changed
    if (open.length > 0) setComments(Object.fromEntries(await Promise.all(open.map(async (sid) => [sid, await snapshotComments(sid)] as const))));
  };
  const doUndo = () => { if (!stack || !undo.undoName || undo.busy) return false; void stack.undo().then(afterUndo).catch(() => undefined); return true; };
  const doRedo = () => { if (!stack || !undo.redoName || undo.busy) return false; void stack.redo().then(afterUndo).catch(() => undefined); return true; };
  useUndoKeys(!presenting, { undo: doUndo, redo: doRedo });

  async function run(f: () => Promise<unknown>) {
    try {
      await f();
      await reload(id);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    }
  }

  async function share() {
    if (!board) return;
    try {
      const prev = snaps.find((x) => !x.revoked && !x.supersededBy);
      const r = await publish(board.id, label, Number(hours), { question, ...(replaces && prev ? { supersedes: prev.id, reason } : {}) });
      setPre(null);
      try {
        localStorage.setItem(BASE_KEY, base);
      } catch {
        /* storage may be unavailable; the link is still shown */
      }
      setLink(`${base.replace(/\/$/, "")}${r.reviewPath}`);
      setSnaps(await listSnapshots(board.id));
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not share the board.");
    }
  }

  const status = (sn: Snapshot) => (sn.revoked ? "Revoked" : new Date(sn.expiresAt) < new Date() ? "Expired" : "Active");

  return (
    <div className={s.root}>
      <div className={s.bar}>
        <Select label="Board" value={id ?? ""} onChange={(e) => setId(Number(e.target.value))}>
          {boards.length === 0 ? <option value="">No boards yet</option> : null}
          {boards.map((b) => <option key={b.id} value={b.id}>{b.title}{b.archived ? " (archived)" : ""}</option>)}
        </Select>
        <form className={s.newBoard} onSubmit={(e) => { e.preventDefault(); if (name.trim()) void createBoard(name.trim()).then((b) => { setName(""); return reload(b.id); }).catch((x: Error) => setErr(x.message)); }}>
          <Input label="New board" value={name} onChange={(e) => setName(e.target.value)} />
          <Button type="submit" disabled={!name.trim()}>Create</Button>
        </form>
      </div>
      <div className={s.row}>
        <Checkbox label="Show archived boards" checked={showArchived} onChange={(e) => setShowArchived(e.target.checked)} />
        {board ? <EntryActions key={board.id} kind="boards" id={board.id} name={board.title} archived={board.archived === true} onChanged={async (c) => { if (c === "deleted" || (c === "archived" && !showArchived)) setId(null); await reload(c === "restored" ? board.id : null); }} /> : null}
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {board ? <UndoBar surface="board" undoName={undo.undoName} redoName={undo.redoName} busy={undo.busy} error={undo.error} onUndo={doUndo} onRedo={doRedo} onDismiss={() => stack?.clearError()} /> : null}
      {!board ? <p className={s.note}>Create a board, then add figures to it from the viewer (Figure section).</p> : null}
      {board && board.items.length === 0 ? <p className={s.note}>This board has no figures. Open a dataset and use “Save as figure”, then add it here.</p> : null}
      {board && board.items.length > 0 ? (
        <div className={s.row}>
          <Button onClick={() => setPresenting(true)}>Present</Button>
          <LinkButton icon={Printer} href={`#/print/${board.id}`} target="_blank" rel="noopener noreferrer">Print or save as PDF</LinkButton>
        </div>
      ) : null}
      {presenting && board ? <Suspense fallback={null}><PresentView board={board} onClose={() => setPresenting(false)} /></Suspense> : null}
      {picked.length === 1 ? <p className={s.note} role="status">Tick a second panel to see what differs between the two.</p> : null}
      {picked.length === 2 ? <BoardCompare a={picked[0]!.figure} b={picked[1]!.figure} {...(onCompare ? { onCompare } : {})} /> : null}
      {board ? (() => {
        const sid = board.items.find((x) => x.kind === "job" && x.job.sweepId != null);
        return sid && sid.kind === "job" && sid.job.sweepId != null ? <SweepGrid sweepId={sid.job.sweepId} refreshKey={board.items.filter((x) => x.kind === "job" && x.job.runId != null).length} /> : null;
      })() : null}
      {board && newer ? <BoardNewerLine board={board} newer={newer} /> : null}
      <ol className={s.items} aria-label="Figures on the board">
        {board?.items.map((it, i) => {
          const f = it.kind === "job" ? null : it.figure;
          const job = it.kind === "job" ? it.job : null;
          const jobRun = job?.runId != null ? lookup.runs.find((r) => r.id === job.runId) : undefined;
          const out = jobRun ? defaultOutput(jobRun) : undefined;
          const outData = out ? lookup.datasets.find((d) => d.id === out.datasetId) : undefined;
          return (
          <li key={it.position} className={`${s.card} ${job && !(jobRun && outData) ? s.cardJob : ""}`}>
            {f ? <img className={s.img} src={figurePreviewUrl(f.id)} alt={`${f.title}. ${provenanceLine(f.provenance)}`} width={f.width} height={f.height} /> : null}
            {job && jobRun && outData ? <LiveResult key={`${job.id}-${it.position}`} boardId={board.id} position={it.position} job={job} run={jobRun} dataset={outData} onPinned={replaceBoard} /> : null}
            {job && !(jobRun && outData) ? <JobFrame job={job} /> : null}
            <div className={s.meta}>
              {f ? (
                <>
                  <h3 className={s.title}>{f.title}</h3>
                  <p className={s.src}>{provenanceLine(f.provenance)}</p>
                  <NewerNotices notices={newer?.[String(f.id)]} />
                  {f.caption ? <p className={s.cap}>{f.caption}</p> : null}
                  <details className={s.det}>
                    <summary>Where this came from</summary>
                    {f.provenance.run ? <RunLine run={f.provenance.run} /> : null}
                    <ul>{f.provenance.statements.map((t) => <li key={t}>{t}</li>)}</ul>
                    <p className={s.src}>{f.provenance.source.identity}. Revision <span className="mono">{f.revision.slice(0, 12)}</span></p>
                    {f.identity?.redrawnFrom ? <p className={s.src}>Drawn again from its stored description; a redrawing of figure {f.identity.redrawnFrom.figureId} (revision <span className="mono">{f.identity.redrawnFrom.revision.slice(0, 12)}</span>), which is kept as it was.</p> : null}
                  </details>
                </>
              ) : null}
              {job ? (
                <>
                  <h3 className={s.title}>{job.label ? `${job.label} · ` : ""}Run {job.name}</h3>
                  <JobLines job={job} now={now} />
                  {job.state === "completed" && job.runId === null ? <p className={s.cap}>Finished; import its results on the Jobs screen to pin the result.</p> : null}
                  {job.state === "completed" && job.runId !== null && !(jobRun && outData) ? <p className={s.src}>Finished. Loading the result to pin it…</p> : null}
                  {job.state === "failed" || job.state === "cancelled" ? <p className={s.cap}>This run {job.state === "failed" ? "failed" : "was cancelled"}; there is no result to pin. Remove the panel when you no longer need it.</p> : null}
                  {job.state === "unknown" ? <p className={s.cap}>This job is no longer in the catalog. Remove the panel.</p> : null}
                </>
              ) : null}
              <label className={s.noteLabel}>
                Note for the reviewer
                <textarea key={`${it.position}:${it.note}`} className={s.area} rows={2} defaultValue={it.note} maxLength={2000} onBlur={(e) => { if (e.target.value !== it.note) void run(() => setNote(board.id, board, it.position, e.target.value)); }} />
              </label>
              {f ? <Checkbox label="Compare" aria-label={`Compare ${f.title}`} checked={ticked.includes(it.position)} onChange={(e) => tick(it.position, e.target.checked)} /> : null}
              <div className={s.row}>
                <IconButton icon={ArrowUp} label="Move up" disabled={i === 0} onClick={() => run(() => moveItem(board.id, board, it.position, -1))} />
                <IconButton icon={ArrowDown} label="Move down" disabled={i === board.items.length - 1} onClick={() => run(() => moveItem(board.id, board, it.position, 1))} />
                {f ? <LinkButton icon={Download} href={`/api/figures/${f.id}/download`} download>Download PNG</LinkButton> : null}
                <IconButton icon={Trash2} label="Remove from board" onClick={() => run(() => removeItem(board.id, board, it.position))} />
              </div>
              {f ? <FigureRedraw figure={f} addTo={(figureId) => run(() => addToBoard(board.id, figureId))} /> : null}
            </div>
          </li>
          );
        })}
      </ol>
      {board && board.items.length > 0 ? (
        <section className={s.share} aria-label="Share for review">
          <h3 className={s.h}>Share for review</h3>
          <p className={s.note}>Sharing freezes the board as it is now. A reviewer sees the frozen pictures, captions, notes and provenance, and can comment. They never receive the data, file paths or any credential.</p>
          <div className={s.shareForm}>
            <Input label="Label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. Draft for the group meeting" />
            <label className={s.noteLabel}>
              What should the reviewer look at?
              <textarea className={s.area} rows={2} maxLength={1000} value={question} onChange={(e) => { setQuestion(e.target.value); setPre(null); }} placeholder="e.g. Is the stretch hiding the edge of the structure?" />
            </label>
            {snaps.some((x) => !x.revoked && !x.supersededBy) ? (
              <>
                <Checkbox label="This replaces the previous shared version" checked={replaces} onChange={(e) => setReplaces(e.target.checked)} />
                {replaces ? <Input label="Why it was replaced (the old link will say this)" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} /> : null}
              </>
            ) : null}
            <Select label="Link valid for" value={hours} onChange={(e) => setHours(e.target.value)}>
              <option value="24">1 day</option>
              <option value="168">7 days</option>
              <option value="336">14 days</option>
              <option value="720">30 days</option>
            </Select>
            <Input label="Review address" value={base} onChange={(e) => setBase(e.target.value)} />
            <Button onClick={() => void run(async () => setPre(await preflight(board.id, question)))}>Check what will be shared</Button>
            <Button variant="primary" onClick={share}>Freeze and create link</Button>
          </div>
          {pre ? (
            <div className={s.link} role="region" aria-label="What the reviewer will see">
              <p className={s.note}>The reviewer will see exactly this and nothing else:</p>
              {pre.warnings.length > 0 ? <ul className={s.comments}>{pre.warnings.map((w) => <li key={w}>{w}</li>)}</ul> : <p className={s.note}>Nothing to flag.</p>}
              <ol className={s.comments}>
                {pre.frozen.items.map((it, n) => it.kind === "job"
                  ? <li key={`job-${n}`}><strong>{it.title}</strong> · a run that is {it.state} now; shown as a placeholder that will not change{it.note ? ` · note: ${it.note}` : ""}</li>
                  : <li key={it.revision}><strong>{it.title}</strong>{it.caption ? ` · ${it.caption}` : ""}{it.note ? ` · note: ${it.note}` : ""}</li>)}
              </ol>
              {pre.frozen.question ? <p className={s.note}>Question: {pre.frozen.question}</p> : null}
            </div>
          ) : null}
          {link ? (
            <div role="status" className={s.link}>
              <p className={s.note}>Shown once. Anyone with this link can read this frozen board; revoke it to end access.</p>
              <input className={s.linkInput} readOnly value={link} aria-label="Review link" onFocus={(e) => e.currentTarget.select()} />
              <Button onClick={() => void navigator.clipboard?.writeText(link)}>Copy link</Button>
            </div>
          ) : null}
          <h4 className={s.h}>Shared snapshots</h4>
          {snaps.length === 0 ? <p className={s.note}>{EM} Nothing shared yet.</p> : null}
          <ul className={s.snaps}>
            {snaps.map((sn) => (
              <li key={sn.id}>
                <span>{sn.label || `Snapshot ${sn.id}`}</span> <span className={s.src}>· {sn.items} figures · {sn.comments} comment{sn.comments === 1 ? "" : "s"} · {status(sn)}{sn.supersededBy ? " · replaced by a newer version" : ""} · expires {new Date(sn.expiresAt).toLocaleDateString()}</span>
                {sn.lastActivity && new Date(sn.lastActivity).getTime() > (seenAt[sn.id] ?? readSeen(`snapshot.${sn.id}`)) ? <p className={s.note} role="status"><strong>New since you last looked:</strong> {sn.outcome ? "the reviewer's outcome or " : ""}comments from the reviewer.</p> : null}
                <p className={s.note}>{sn.outcome ? `Reviewer: ${OUTCOME_TEXT[sn.outcome]}${sn.outcomeBy ? ` (${sn.outcomeBy})` : ""}${sn.outcomeNote ? ` · ${sn.outcomeNote}` : ""}` : "Not yet reviewed."}</p>
                <div className={s.row}>
                  <Button onClick={() => void Promise.all([snapshotComments(sn.id), listSnapshots(board.id)]).then(([c, list]) => { setComments({ ...comments, [sn.id]: c }); setSnaps(list); setSeenAt({ ...seenAt, [sn.id]: readSeen(`snapshot.${sn.id}`) }); markSeen(`snapshot.${sn.id}`); })}>Comments and outcome</Button>
                  <Button disabled={sn.revoked} onClick={() => run(async () => { await revokeSnapshot(sn.id); setSnaps(await listSnapshots(board.id)); })}>Revoke link</Button>
                </div>
                {comments[sn.id] ? (
                  <div className={s.comments}>
                    {comments[sn.id]!.length === 0 ? <p className={s.note}>No comments yet.</p> : null}
                    {[...new Set(comments[sn.id]!.map((c) => c.figureRevision))].map((rev) => (
                      <Threads
                        key={rev}
                        comments={comments[sn.id]!.filter((c) => c.figureRevision === rev)}
                        isNew={(c) => !c.fromOwner && new Date(c.createdAt).getTime() > (seenAt[sn.id] ?? 0)}
                        onReply={async (parentId, body) => { await ownerReply(sn.id, parentId, body); setComments({ ...comments, [sn.id]: await snapshotComments(sn.id) }); }}
                        onResolve={async (id, resolved) => { await resolveComment(board.id, id, resolved); setComments({ ...comments, [sn.id]: await snapshotComments(sn.id) }); }}
                      />
                    ))}
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}
