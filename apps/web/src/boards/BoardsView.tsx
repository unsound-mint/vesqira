import { useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Trash2 } from "lucide-react";
import { Button, Checkbox, IconButton, Input, Select } from "../ui";
import { addToBoard as _unused, createBoard, figurePreviewUrl, listBoards, listSnapshots, moveItem, publish, removeItem, revokeSnapshot, setNote, snapshotComments, ownerReply, resolveComment, preflight, OUTCOME_TEXT } from "../api/boards";
import { Threads } from "./Threads";
import { PresentView } from "./PresentView";
import { markSeen, readSeen } from "./seen";
import type { Board, Comment, Preflight, Snapshot } from "../api/boards";
import { EM } from "../catalog/format";
import { RunLine } from "./RunLine";
import s from "./BoardsView.module.css";

void _unused;
const BASE_KEY = "vesqira.reviewBase";
const readBase = () => {
  try {
    return localStorage.getItem(BASE_KEY) ?? "http://127.0.0.1:8788";
  } catch {
    return "http://127.0.0.1:8788";
  }
};

export function provenanceLine(p: Board["items"][number]["figure"]["provenance"]): string {
  const v = p.view;
  const parts = [p.source.name];
  if (v.channel !== undefined) parts.push(`channel ${v.channel + 1}`);
  if (v.kind) parts.push(v.kind);
  return parts.join(" · ");
}

/** Research board: a mutable draft of pinned figures. Sharing freezes it. */
export function BoardsView() {
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
  const [seenAt, setSeenAt] = useState<Record<number, number>>({}); // when each snapshot was last opened, before this visit

  const reload = useCallback(async (keep?: number | null) => {
    try {
      const bs = await listBoards();
      setBoards(bs);
      setId((cur) => keep ?? cur ?? bs[0]?.id ?? null);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not load boards.");
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);
  const board = boards.find((b) => b.id === id) ?? null;
  useEffect(() => {
    setLink(null);
    if (board) listSnapshots(board.id).then(setSnaps).catch(() => setSnaps([]));
    else setSnaps([]);
  }, [board?.id]); // eslint-disable-line react-hooks/exhaustive-deps

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
          {boards.map((b) => <option key={b.id} value={b.id}>{b.title}</option>)}
        </Select>
        <form className={s.newBoard} onSubmit={(e) => { e.preventDefault(); if (name.trim()) void createBoard(name.trim()).then((b) => { setName(""); return reload(b.id); }).catch((x: Error) => setErr(x.message)); }}>
          <Input label="New board" value={name} onChange={(e) => setName(e.target.value)} />
          <Button type="submit" disabled={!name.trim()}>Create</Button>
        </form>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {!board ? <p className={s.note}>Create a board, then add figures to it from the viewer (Figure section).</p> : null}
      {board && board.items.length === 0 ? <p className={s.note}>This board has no figures. Open a dataset and use “Save as figure”, then add it here.</p> : null}
      {board && board.items.length > 0 ? (
        <div className={s.row}>
          <Button onClick={() => setPresenting(true)}>Present</Button>
          <a className={s.link2} href={`#/print/${board.id}`} target="_blank" rel="noopener noreferrer">Print or save as PDF</a>
        </div>
      ) : null}
      {presenting && board ? <PresentView board={board} onClose={() => setPresenting(false)} /> : null}
      <ol className={s.items} aria-label="Figures on the board">
        {board?.items.map((it, i) => (
          <li key={it.position} className={s.card}>
            <img className={s.img} src={figurePreviewUrl(it.figure.id)} alt={`${it.figure.title}. ${provenanceLine(it.figure.provenance)}`} width={it.figure.width} height={it.figure.height} />
            <div className={s.meta}>
              <h3 className={s.title}>{it.figure.title}</h3>
              <p className={s.src}>{provenanceLine(it.figure.provenance)}</p>
              {it.figure.caption ? <p className={s.cap}>{it.figure.caption}</p> : null}
              <details className={s.det}>
                <summary>Where this came from</summary>
                {it.figure.provenance.run ? <RunLine run={it.figure.provenance.run} /> : null}
                <ul>{it.figure.provenance.statements.map((t) => <li key={t}>{t}</li>)}</ul>
                <p className={s.src}>{it.figure.provenance.source.identity}. Revision <span className="mono">{it.figure.revision.slice(0, 12)}</span></p>
              </details>
              <label className={s.noteLabel}>
                Note for the reviewer
                <textarea className={s.area} rows={2} defaultValue={it.note} maxLength={2000} onBlur={(e) => { if (e.target.value !== it.note) void run(() => setNote(board.id, it.position, e.target.value)); }} />
              </label>
              <div className={s.row}>
                <IconButton icon={ArrowUp} label="Move up" disabled={i === 0} onClick={() => run(() => moveItem(board.id, it.position, -1))} />
                <IconButton icon={ArrowDown} label="Move down" disabled={i === board.items.length - 1} onClick={() => run(() => moveItem(board.id, it.position, 1))} />
                <a className={s.link2} href={`/api/figures/${it.figure.id}/download`} download>Download PNG</a>
                <IconButton icon={Trash2} label="Remove from board" onClick={() => run(() => removeItem(board.id, it.position))} />
              </div>
            </div>
          </li>
        ))}
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
                {pre.frozen.items.map((it) => <li key={it.revision}><strong>{it.title}</strong>{it.caption ? ` · ${it.caption}` : ""}{it.note ? ` · note: ${it.note}` : ""}</li>)}
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
                        onResolve={async (id, resolved) => { await resolveComment(id, resolved); setComments({ ...comments, [sn.id]: await snapshotComments(sn.id) }); }}
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
