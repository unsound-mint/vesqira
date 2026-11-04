import { useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Trash2 } from "lucide-react";
import { Button, IconButton, Input, Select } from "../ui";
import { addToBoard as _unused, createBoard, figurePreviewUrl, listBoards, listSnapshots, moveItem, publish, removeItem, revokeSnapshot, setNote, snapshotComments, ownerReply, resolveComment } from "../api/boards";
import { Threads } from "./Threads";
import type { Board, Comment, Snapshot } from "../api/boards";
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
      const r = await publish(board.id, label, Number(hours));
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
            <Select label="Link valid for" value={hours} onChange={(e) => setHours(e.target.value)}>
              <option value="24">1 day</option>
              <option value="168">7 days</option>
              <option value="336">14 days</option>
              <option value="720">30 days</option>
            </Select>
            <Input label="Review address" value={base} onChange={(e) => setBase(e.target.value)} />
            <Button variant="primary" onClick={share}>Freeze and create link</Button>
          </div>
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
                <span>{sn.label || `Snapshot ${sn.id}`}</span> <span className={s.src}>· {sn.items} figures · {status(sn)} · expires {new Date(sn.expiresAt).toLocaleDateString()}</span>
                <div className={s.row}>
                  <Button onClick={() => void snapshotComments(sn.id).then((c) => setComments({ ...comments, [sn.id]: c }))}>Comments</Button>
                  <Button disabled={sn.revoked} onClick={() => run(async () => { await revokeSnapshot(sn.id); setSnaps(await listSnapshots(board.id)); })}>Revoke link</Button>
                </div>
                {comments[sn.id] ? (
                  <div className={s.comments}>
                    {comments[sn.id]!.length === 0 ? <p className={s.note}>No comments yet.</p> : null}
                    {[...new Set(comments[sn.id]!.map((c) => c.figureRevision))].map((rev) => (
                      <Threads
                        key={rev}
                        comments={comments[sn.id]!.filter((c) => c.figureRevision === rev)}
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
