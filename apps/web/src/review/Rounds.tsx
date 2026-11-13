import type { CarriedComment, ItemChange, Review, Since } from "../api/boards";
import { lineText, MARKER, summary } from "../boards/rounds";
import s from "./Rounds.module.css";

/** "Changed since <date>" at the top of a later round: the list the author's publish froze into this link. */
export function ChangedSince({ round, since, previous }: { round: number; since: Since; previous: Review["previous"] }) {
  const open = previous?.available === true;
  const date = new Date(since.at).toLocaleDateString();
  return (
    <section className={s.since} aria-labelledby="changed-since">
      <h2 id="changed-since" className={s.h}>Changed since {date}</h2>
      <p className={s.small}>
        Round {round}{since.label ? `, after “${since.label}”` : ""} · {summary(since.items, since.removedCount)}.
        {!open ? " The earlier round is no longer available, so only what this round holds is described." : ""}
      </p>
      {since.lines.length > 0 ? (
        <ul className={s.list}>{since.lines.map((l, i) => <li key={i}>{lineText(l)}</li>)}</ul>
      ) : since.removedCount > 0 ? null : <p className={s.small}>Nothing was added, removed, redrawn or reworded.</p>}
    </section>
  );
}

/** The marker on one figure, and what it was before. The earlier picture is fetched only while the earlier link is valid. */
export function FigureRound({ change, previous, pictureUrl, round }: { change: ItemChange; previous: Review["previous"]; pictureUrl: (sha: string) => string; round: number }) {
  const m = MARKER[change.state];
  const old = change.previous;
  const text = change.changed.filter((c) => c === "title" || c === "caption" || c === "note");
  return (
    <>
      <p className={s.marker} data-state={change.state}>
        <span aria-hidden="true">{m.glyph}</span>
        {m.text}{change.state === "unchanged" ? " picture" : ""} since round {round - 1}
      </p>
      {change.state === "redrawn" ? (
        <details className={s.earlier}>
          <summary>The earlier drawing</summary>
          {old?.previewSha && previous?.available ? (
            // the earlier round's own picture, served only while that round's link is valid
            <img src={pictureUrl(old.previewSha)} width={old.width} height={old.height} alt={`${old.title}, as drawn in round ${round - 1}`} />
          ) : <p>The earlier picture is no longer available.</p>}
        </details>
      ) : null}
      {old && text.length > 0 ? (
        <p className={s.earlier}>
          {text.includes("title") ? <>Earlier title: “{old.title}”. </> : null}
          {text.includes("caption") ? <>Earlier caption: {old.caption ? `“${old.caption}”` : "none"}. </> : null}
          {text.includes("note") ? <>Earlier note: {old.note ? `“${old.note}”` : "none"}.</> : null}
        </p>
      ) : null}
    </>
  );
}

/** Comments of earlier rounds on this figure: read-only, each labelled with its round. Replying is done on the figure below. */
export function CarriedThreads({ comments, title }: { comments: CarriedComment[]; title: string }) {
  if (comments.length === 0) return null;
  const roots = comments.filter((c) => !c.parentId);
  return (
    <section aria-label={`Comments from earlier rounds on ${title}`}>
      <h3 className={s.small}>From earlier rounds</h3>
      <ul className={s.carried}>
        {roots.map((r) => (
          <li key={r.id} className={s.thread}>
            <Message c={r} />
            {comments.filter((c) => c.parentId === r.id).map((c) => <Message key={c.id} c={c} reply />)}
          </li>
        ))}
      </ul>
    </section>
  );
}

function Message({ c, reply }: { c: CarriedComment; reply?: boolean }) {
  return (
    <p className={`${s.msg} ${reply ? s.reply : ""}`}>
      <span className={s.round}>Round {c.round}</span>
      <strong>{c.author}</strong> <span className={s.when}>{new Date(c.createdAt).toLocaleString()}</span>
      {c.resolvedAt ? <span className={s.when}> · closed by the author</span> : null}
      {c.anchor ? <span className={s.when}> · marked a place on the picture</span> : null}
      {c.pictureChanged ? <span className={s.when}> · made on the earlier drawing</span> : null}
      <br />
      {c.body}
    </p>
  );
}
