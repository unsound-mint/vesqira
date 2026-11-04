import { useEffect, useState } from "react";
import { Button, Input } from "../ui";
import { RunLine } from "../boards/RunLine";
import { addReviewComment, getReview, reviewPreviewUrl } from "../api/boards";
import type { Anchor, Comment, Review } from "../api/boards";
import { Threads, threadNumbers } from "../boards/Threads";
import { AnchorImage } from "./AnchorImage";
import s from "./ReviewPage.module.css";

/** What a supervisor sees: a frozen board of published previews with provenance, and a way to comment. No data, no paths. */
export function ReviewPage({ token }: { token: string }) {
  const [rv, setRv] = useState<Review | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [author, setAuthor] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState<string | null>(null);
  const [marking, setMarking] = useState<string | null>(null); // the figure revision being marked
  const [draftAnchors, setDraftAnchor] = useState<Record<string, Anchor | null>>({});

  useEffect(() => {
    getReview(token).then(setRv).catch((e: Error) => setErr(e.message));
  }, [token]);

  async function send(rev: string) {
    const body = (drafts[rev] ?? "").trim();
    if (!body) return;
    try {
      const anchor = draftAnchors[rev];
      const c = await addReviewComment(token, { figureRevision: rev, author: author.trim(), body, ...(anchor ? { anchor } : {}) });
      setRv((cur) => (cur ? { ...cur, comments: [...cur.comments, c] } : cur));
      setDrafts({ ...drafts, [rev]: "" });
      setDraftAnchor({ ...draftAnchors, [rev]: null });
      setMarking(null);
      setMsg(null);
    } catch (e) {
      setMsg(e instanceof Error ? e.message : "Could not send the comment.");
    }
  }

  async function reply(parentId: number, body: string) {
    const c = await addReviewComment(token, { parentId, author: author.trim(), body });
    setRv((cur) => (cur ? { ...cur, comments: [...cur.comments, c] } : cur));
  }

  if (err) return <main className={s.page}><p role="alert" className={s.err}>{err}</p></main>;
  if (!rv) return <main className={s.page}><p className={s.note}>Loading the review…</p></main>;
  const snap = rv.snapshot;
  const byFigure = (rev: string): Comment[] => rv.comments.filter((c) => c.figureRevision === rev);

  return (
    <main className={s.page}>
      <header className={s.head}>
        <h1 className={s.h1}>{snap.boardTitle}</h1>
        <p className={s.note}>
          {snap.label ? `${snap.label} · ` : ""}Frozen {new Date(snap.createdAt).toLocaleString()} · link valid until {new Date(snap.expiresAt).toLocaleDateString()}. You see published previews only: not the data, file paths or any credential.
        </p>
        <Input label="Your name (shown with your comments)" value={author} onChange={(e) => setAuthor(e.target.value)} maxLength={80} />
      </header>
      {msg ? <p role="alert" className={s.err}>{msg}</p> : null}
      {snap.items.map((it) => (
        <article key={it.revision} className={s.fig}>
          <h2 className={s.h2}>{it.title}</h2>
          <AnchorImage
            src={reviewPreviewUrl(token, it.previewSha)}
            alt={`${it.title}. ${it.provenance.source.name}`}
            width={it.width}
            height={it.height}
            marks={[...threadNumbers(byFigure(it.revision))].flatMap(([id, n]) => { const c = byFigure(it.revision).find((x) => x.id === id); return c?.anchor ? [{ n, anchor: c.anchor }] : []; })}
            draft={draftAnchors[it.revision] ?? null}
            marking={marking === it.revision}
            onDraft={(a) => setDraftAnchor({ ...draftAnchors, [it.revision]: a })}
          />
          {it.caption ? <p className={s.cap}>{it.caption}</p> : null}
          {it.note ? <p className={s.noteBox}><strong>Note from the author:</strong> {it.note}</p> : null}
          <details className={s.det}>
            <summary>Where this came from and what it does not show</summary>
            {it.provenance.run ? <RunLine run={it.provenance.run} /> : null}
            <ul>
              <li>File: <span className="mono">{it.provenance.source.name}</span> ({it.provenance.source.identity}).</li>
              {it.provenance.interpretation ? <li>Interpretation declared by {it.provenance.interpretation.declaredBy}: {it.provenance.interpretation.kind}, unit {it.provenance.interpretation.unit}.</li> : null}
              {it.provenance.statements.map((t) => <li key={t}>{t}</li>)}
            </ul>
          </details>
          <section aria-label={`Comments on ${it.title}`} className={s.comments}>
            <Threads comments={byFigure(it.revision)} onReply={reply} />
            <label className={s.label}>
              Comment
              <textarea className={s.area} rows={3} maxLength={4000} value={drafts[it.revision] ?? ""} onChange={(e) => setDrafts({ ...drafts, [it.revision]: e.target.value })} />
            </label>
            <div className={s.row}>
              <Button onClick={() => setMarking(marking === it.revision ? null : it.revision)}>{marking === it.revision ? "Done marking" : "Mark a place on the picture"}</Button>
              {draftAnchors[it.revision] ? <Button onClick={() => setDraftAnchor({ ...draftAnchors, [it.revision]: null })}>Clear the mark</Button> : null}
              <Button onClick={() => send(it.revision)} disabled={!(drafts[it.revision] ?? "").trim()}>Send comment</Button>
            </div>
            {draftAnchors[it.revision] ? <p className={s.note}>The comment will point at the marked place on the picture. It marks the picture as drawn, not a sky position.</p> : null}
          </section>
        </article>
      ))}
    </main>
  );
}
