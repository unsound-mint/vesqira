import { useEffect, useRef, useState } from "react";
import { Check, RotateCcw, X } from "lucide-react";
import * as api from "../api/reports";
import type { ReportView, ThreadComment } from "../api/reports";
import { Button, Icon } from "../ui";
import type { DocBlock } from "./Document";
import { plainText } from "./markdown";
import { relTime } from "./time";
import s from "./ReportsView.module.css";

function excerpt(b: DocBlock | undefined): string {
  if (!b) return "";
  if (b.kind === "figure") return b.figure ? `Figure: ${b.figure.title}` : "A figure";
  if (b.kind === "run") return b.run ? `Run ${b.run.name}` : "A run";
  const t = plainText(b.text);
  return t.length > 120 ? t.slice(0, 119) + "…" : t;
}

/**
 * The supervisor's threads on this report, in the order of the page. Replies the agent drafted wait here for the
 * owner to read, edit and send; nothing is sent without the owner pressing Send. Comments arrive from the publish
 * server when the app syncs (every minute while it runs).
 */
export function ReportThreads({ view, blocks, activeBlock, onSelectBlock, onChanged, onClose }: { view: ReportView; blocks: DocBlock[]; activeBlock: string | null; onSelectBlock: (id: string) => void; onChanged: () => Promise<void> | void; onClose: () => void }) {
  const heading = useRef<HTMLHeadingElement>(null);
  const [syncing, setSyncing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => heading.current?.focus(), []);
  const byBlock = new Map(blocks.map((b, i) => [b.id, { b, i }]));
  const threads = view.comments.filter((c) => c.parentId === 0);
  const replies = (id: number) => view.comments.filter((c) => c.parentId === id);
  const order = (t: ThreadComment) => byBlock.get(t.block)?.i ?? 1e9;
  const sorted = [...threads].sort((a, b) => Number(!!a.resolvedAt) - Number(!!b.resolvedAt) || order(a) - order(b) || a.id - b.id);
  const current = sorted.filter((t) => byBlock.has(t.block));
  const older = sorted.filter((t) => !byBlock.has(t.block));

  async function sync() {
    setSyncing(true);
    setErr(null);
    try {
      await api.syncNow();
      await onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not reach the publish server.");
    } finally {
      setSyncing(false);
    }
  }

  return (
    <div className={s.panelInner}>
      <div className={s.panelHead}>
        <h2 ref={heading} tabIndex={-1}>Comments</h2>
        <button type="button" className={s.ghost} onClick={() => void sync()} disabled={syncing}>{syncing ? "Syncing…" : "Sync now"}</button>
        <button type="button" className={s.iconBtn} onClick={onClose} aria-label="Close comments"><Icon icon={X} /></button>
      </div>
      {err ? <p className={s.error} role="alert">{err}</p> : null}
      <div className={s.panelScroll}>
        {threads.length === 0 ? <p className={s.muted}>No comments yet. When your supervisor comments on a published revision, it appears here and next to the paragraph.</p> : null}
        {current.map((t) => (
          <Thread key={t.id} thread={t} replies={replies(t.id)} quote={excerpt(byBlock.get(t.block)?.b)} active={activeBlock === t.block} onSelect={() => onSelectBlock(t.block)} view={view} onChanged={onChanged} />
        ))}
        {older.length ? <h3 className={s.groupHead}>On earlier revisions</h3> : null}
        {older.map((t) => (
          <Thread key={t.id} thread={t} replies={replies(t.id)} quote="This part has changed since the comment was made." active={false} view={view} onChanged={onChanged} />
        ))}
      </div>
    </div>
  );
}

function Thread({ thread, replies, quote, active, onSelect, view, onChanged }: { thread: ThreadComment; replies: ThreadComment[]; quote: string; active: boolean; onSelect?: () => void; view: ReportView; onChanged: () => Promise<void> | void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const drafts = view.drafts.filter((d) => d.commentId === thread.id);
  const [edits, setEdits] = useState<Record<number, string>>({});
  async function run(f: () => Promise<unknown>) {
    setBusy(true);
    setErr(null);
    try {
      await f();
      await onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className={s.thread} data-active={active ? "true" : undefined} data-resolved={thread.resolvedAt ? "true" : undefined}>
      <button type="button" className={s.quote} onClick={onSelect} disabled={!onSelect}>{quote}</button>
      {[thread, ...replies].map((c) => (
        <div key={c.id} className={s.msg}>
          <div className={s.msgHead}><strong>{c.author}</strong><span>{relTime(c.createdAt)}</span></div>
          <p className={s.msgBody}>{c.body}</p>
        </div>
      ))}
      {drafts.map((d) => (
        <div key={d.id} className={s.draftReply}>
          <div className={s.msgHead}><strong>Draft reply</strong><span>by {d.author === "agent" ? "the agent" : "you"} · not sent</span></div>
          <textarea value={edits[d.id] ?? d.body} onChange={(e) => setEdits({ ...edits, [d.id]: e.target.value })} rows={3} aria-label="Draft reply" />
          <div className={s.row}>
            <Button variant="primary" disabled={busy} onClick={() => void run(() => api.sendDraft(d.id, edits[d.id]))}>Send</Button>
            <button type="button" className={s.ghost} disabled={busy} onClick={() => void run(() => api.discardDraft(d.id))}>Discard</button>
          </div>
        </div>
      ))}
      {thread.resolvedAt ? (
        <div className={s.row}>
          <span className={s.muted}>Resolved</span>
          <button type="button" className={s.ghost} disabled={busy} onClick={() => void run(() => api.resolveThread(thread.id, false))}><Icon icon={RotateCcw} />Reopen</button>
        </div>
      ) : (
        <>
          <textarea className={s.replyBox} value={text} placeholder="Reply" rows={2} onChange={(e) => setText(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && text.trim()) void run(async () => { await api.replyTo(thread.id, text); setText(""); }); }} aria-label={`Reply to ${thread.author}`} />
          <div className={s.row}>
            <Button disabled={busy || !text.trim()} onClick={() => void run(async () => { await api.replyTo(thread.id, text); setText(""); })}>Reply</Button>
            <button type="button" className={s.ghost} disabled={busy} onClick={() => void run(() => api.resolveThread(thread.id, true))}><Icon icon={Check} />Resolve</button>
          </div>
        </>
      )}
      {err ? <p className={s.error} role="alert">{err}</p> : null}
    </section>
  );
}
