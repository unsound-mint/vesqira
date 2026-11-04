import { useState } from "react";
import { Button } from "../ui";
import type { Comment } from "../api/boards";
import s from "./Threads.module.css";

/** Numbers the anchored threads of one figure in the order they were written, so a marker and its thread share a number. */
export function threadNumbers(comments: Comment[]): Map<number, number> {
  const m = new Map<number, number>();
  for (const c of comments) if (!c.parentId && c.anchor) m.set(c.id, m.size + 1);
  return m;
}

type Props = {
  comments: Comment[]; // the comments of one figure
  onReply: (parentId: number, body: string) => Promise<void>;
  onResolve?: (id: number, resolved: boolean) => Promise<void>; // only the author can close a thread
  isNew?: (c: Comment) => boolean; // comments to flag as new since the person last looked
};

export function Threads({ comments, onReply, onResolve, isNew }: Props) {
  const [drafts, setDrafts] = useState<Record<number, string>>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const numbers = threadNumbers(comments);
  const roots = comments.filter((c) => !c.parentId);
  async function run(f: () => Promise<void>) {
    setBusy(true);
    try {
      await f();
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    }
    setBusy(false);
  }
  if (roots.length === 0) return null;
  return (
    <ul className={s.threads}>
      {roots.map((r) => {
        const replies = comments.filter((c) => c.parentId === r.id);
        const n = numbers.get(r.id);
        return (
          <li key={r.id} className={`${s.thread} ${r.resolvedAt ? s.resolved : ""}`}>
            <p className={s.msg}>
              {n ? <span className={s.badge} aria-label={`Marked place ${n}`}>{n}</span> : null}
              <strong>{r.author}</strong> <span className={s.when}>{new Date(r.createdAt).toLocaleString()}</span>
              {isNew?.(r) ? <span className={s.fresh}>New</span> : null}
              {r.resolvedAt ? <span className={s.state}>Resolved by the author</span> : null}
              <br />
              {r.body}
            </p>
            {replies.map((c) => (
              <p key={c.id} className={`${s.msg} ${s.reply}`}>
                <strong>{c.author}</strong> <span className={s.when}>{new Date(c.createdAt).toLocaleString()}</span>
                {isNew?.(c) ? <span className={s.fresh}>New</span> : null}
                <br />
                {c.body}
              </p>
            ))}
            <div className={s.actions}>
              <input
                className={s.input}
                aria-label={`Reply to ${r.author}`}
                placeholder="Reply"
                maxLength={4000}
                value={drafts[r.id] ?? ""}
                onChange={(e) => setDrafts({ ...drafts, [r.id]: e.target.value })}
              />
              <Button
                disabled={busy || !(drafts[r.id] ?? "").trim()}
                onClick={() => void run(async () => { await onReply(r.id, (drafts[r.id] ?? "").trim()); setDrafts({ ...drafts, [r.id]: "" }); })}
              >
                Reply
              </Button>
              {onResolve ? (
                <Button disabled={busy} onClick={() => void run(() => onResolve(r.id, !r.resolvedAt))}>{r.resolvedAt ? "Reopen" : "Mark resolved"}</Button>
              ) : null}
            </div>
          </li>
        );
      })}
      {err ? <li role="alert" className={s.err}>{err}</li> : null}
    </ul>
  );
}
