import { useEffect, useState } from "react";
import type { CSSProperties } from "react";
import { AccessBadge, Button, ErrorReference, Input } from "../ui";
import { ApiError } from "../api/http";
import { tierLine } from "../api/tier";
import { RunLine } from "../boards/RunLine";
import { addReviewComment, getReview, OUTCOME_TEXT, reviewPreviewUrl, setOutcome } from "../api/boards";
import type { OutcomeValue } from "../api/boards";
import type { Anchor, Comment, Review } from "../api/boards";
import { Threads, threadNumbers } from "../boards/Threads";
import { placed } from "../boards/layout";
import type { Rect } from "../boards/layout";
import { AnchorImage } from "./AnchorImage";
import { CarriedThreads, ChangedSince, FigureRound } from "./Rounds";
import { markSeen, readName, readSeen, saveName } from "../boards/seen";
import s from "./ReviewPage.module.css";

type Problem = { text: string; id?: string };
const problem = (e: unknown, fallback: string): Problem => ({ text: e instanceof Error ? e.message : fallback, id: e instanceof ApiError ? e.requestId : undefined });
const place = (r: Rect): CSSProperties => ({ "--col": `${r.x + 1} / span ${r.w}`, "--rows": `${r.y + 1} / span ${r.h}` } as CSSProperties);
const Shown = ({ p }: { p: Problem }) => <>{p.text}{p.id ? <> <ErrorReference id={p.id} /></> : null}</>;

/** What a supervisor sees: a frozen board of published previews with provenance, and a way to comment. No data, no paths. */
export function ReviewPage({ token }: { token: string }) {
  const [rv, setRv] = useState<Review | null>(null);
  const [err, setErr] = useState<Problem | null>(null);
  const [author, setAuthor] = useState(readName);
  const [since] = useState(() => readSeen(`review.${token.slice(0, 16)}`)); // when this reviewer last opened this link
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState<Problem | null>(null);
  const [verdict, setVerdict] = useState<OutcomeValue>("commented");
  const [verdictNote, setVerdictNote] = useState("");
  const [marking, setMarking] = useState<string | null>(null); // the figure revision being marked
  const [draftAnchors, setDraftAnchor] = useState<Record<string, Anchor | null>>({});

  useEffect(() => {
    getReview(token).then((r) => { setRv(r); markSeen(`review.${token.slice(0, 16)}`); }).catch((e: unknown) => setErr(problem(e, "Could not load the review.")));
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
      setMsg(problem(e, "Could not send the comment."));
    }
  }

  async function reply(parentId: number, body: string) {
    const c = await addReviewComment(token, { parentId, author: author.trim(), body });
    setRv((cur) => (cur ? { ...cur, comments: [...cur.comments, c] } : cur));
  }

  async function sendOutcome() {
    try {
      const o = await setOutcome(token, { outcome: verdict, note: verdictNote.trim(), author: author.trim() });
      setRv((cur) => (cur ? { ...cur, outcome: o } : cur));
      setMsg(null);
    } catch (e) {
      setMsg(problem(e, "Could not send your outcome."));
    }
  }

  if (err) return <main className={s.page}><p role="alert" className={s.err}><Shown p={err} /></p></main>;
  if (!rv) return <main className={s.page}><p className={s.note}>Loading the review…</p></main>;
  const snap = rv.snapshot;
  const byFigure = (rev: string): Comment[] => rv.comments.filter((c) => c.figureRevision === rev);
  const round = snap.round;
  const delta = round?.since; // the comparison with the round before: present from round 2 of a board on
  const changeOf = (it: (typeof snap.items)[number]) => delta?.items[snap.items.indexOf(it)];

  return (
    <main className={s.page}>
      <header className={s.head}>
        <h1 className={s.h1}>{snap.boardTitle}</h1>
        <p className={s.note}>
          {snap.label ? `${snap.label} · ` : ""}Frozen {new Date(snap.createdAt).toLocaleString()} · link valid until {new Date(snap.expiresAt).toLocaleDateString()}. You see published previews only: not the data, file paths or any credential.
        </p>
        {snap.access ? (
          <p role="note" className={s.noteBox}>
            <AccessBadge label={snap.access} /> The author marked the data shown here as {snap.access === "unreleased" ? "unreleased" : "for the collaboration only"}. Do not share or cite what you see here outside that agreement.
          </p>
        ) : null}
        <Input label="Your name (shown with your comments)" value={author} onChange={(e) => { setAuthor(e.target.value); saveName(e.target.value); }} maxLength={80} />
      </header>
      {since > 0 && rv.comments.some((c) => c.fromOwner && new Date(c.createdAt).getTime() > since) ? <p role="status" className={s.noteBox}><strong>Since your last visit:</strong> the author has replied. New replies are marked.</p> : null}
      {rv.superseded ? <p role="status" className={s.noteBox}><strong>The author has replaced this version.</strong> {rv.superseded.reason} Ask for the new link; this one stays readable.</p> : null}
      {round && delta ? <ChangedSince round={round.number} since={delta} previous={rv.previous} /> : null}
      {snap.question ? <p className={s.noteBox}><strong>What the author asks you to look at:</strong> {snap.question}</p> : null}
      {msg ? <p role="alert" className={s.err}><Shown p={msg} /></p> : null}
      {/* the author's saved layout, read-only: the same columns and reading order; rows size to their content here */}
      <div className={s.board} data-arranged={snap.items.some((x) => x.layout) || undefined}>
      {placed(snap.items).map(({ item: it, rect }, n) => it.kind === "job" ? (
        <article key={`job-${n}`} className={s.fig} style={place(rect)} aria-label={`Run ${it.title}`}>
          <h2 className={s.h2}>Run {it.title}</h2>
          <p className={s.noteBox}>Run {it.title}: {it.state} when this snapshot was published ({new Date(it.at).toLocaleString()}). Its result was not on the board then and this page will not change.</p>
          {it.note ? <p className={s.noteBox}><strong>Note from the author:</strong> {it.note}</p> : null}
        </article>
      ) : (
        <article key={it.revision} className={s.fig} style={place(rect)}>
          {/* a figure shared on its own has its title as the page heading already: keep the heading for structure, not on screen */}
          <h2 className={snap.items.length === 1 && it.title === snap.boardTitle ? s.sr : s.h2}>{it.title}</h2>
          {it.access ? <p className={s.note}><AccessBadge label={it.access} /></p> : null}
          {round && delta && changeOf(it) ? <FigureRound change={changeOf(it)!} previous={rv.previous} pictureUrl={(sha) => reviewPreviewUrl(token, sha)} round={round.number} /> : null}
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
          <details className={s.det} open={it.provenance.sky ? true : undefined}>
            <summary>Where this came from and what it does not show</summary>
            {it.provenance.run ? <RunLine run={it.provenance.run} /> : null}
            <ul>
              <li>{it.provenance.runPlot || it.provenance.sky ? "Source" : "File"}: <span className="mono">{it.provenance.source.name}</span> ({it.provenance.source.identity}).</li>
              {it.provenance.interpretation ? <li>Interpretation declared by {it.provenance.interpretation.declaredBy}: {it.provenance.interpretation.kind}, unit {it.provenance.interpretation.unit}.</li> : null}
              {(it.provenance.tiers ?? []).map((t) => <li key={t.what} data-tier={t.tier}>{tierLine(t)}</li>)}
              {it.provenance.statements.map((t) => <li key={t}>{t}</li>)}
            </ul>
          </details>
          <CarriedThreads comments={(delta?.carried ?? []).filter((c) => c.figureRevision === it.revision)} title={it.title} />
          <section aria-label={`Comments on ${it.title}`} className={s.comments}>
            <Threads comments={byFigure(it.revision)} onReply={reply} isNew={(c) => c.fromOwner && since > 0 && new Date(c.createdAt).getTime() > since} />
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
      </div>
      <section className={s.fig} aria-label="Your outcome">
        <h2 className={s.h2}>Your outcome for this version</h2>
        <p className={s.note}>This says what you thought of the review itself. It is not an assessment of any run: the author keeps that separately.{rv.outcome?.value ? ` Currently: ${OUTCOME_TEXT[rv.outcome.value]}.` : " Not sent yet."}</p>
        <div className={s.row} role="radiogroup" aria-label="Outcome">
          {(Object.keys(OUTCOME_TEXT) as OutcomeValue[]).map((v) => (
            <label key={v} className={s.note}><input type="radio" name="outcome" checked={verdict === v} onChange={() => setVerdict(v)} /> {OUTCOME_TEXT[v]}</label>
          ))}
        </div>
        <label className={s.label}>
          Note (optional)
          <textarea className={s.area} rows={2} maxLength={1000} value={verdictNote} onChange={(e) => setVerdictNote(e.target.value)} />
        </label>
        <div className={s.row}><Button onClick={sendOutcome}>Send outcome</Button></div>
      </section>
    </main>
  );
}
