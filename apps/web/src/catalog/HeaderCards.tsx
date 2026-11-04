import { useState } from "react";
import { headerCards } from "../api/catalog";
import s from "./DatasetInspector.module.css";

type Cards = Awaited<ReturnType<typeof headerCards>>;

/** The file's own header text, one card per line, loaded when opened. Nothing in it is interpreted. */
export function HeaderCards({ id }: { id: number }) {
  const [data, setData] = useState<Cards | null>(null);
  const [err, setErr] = useState<string | null>(null);
  function toggle(e: React.SyntheticEvent<HTMLDetailsElement>) {
    if (!e.currentTarget.open || data) return;
    headerCards(id).then(setData).catch((x: Error) => setErr(x.message));
  }
  return (
    <details className={s.note} onToggle={toggle}>
      <summary>Raw header cards</summary>
      {err ? <p role="alert">{err}</p> : null}
      {!data && !err ? <p>Loading…</p> : null}
      {data ? (
        <>
          <pre className={s.cards} tabIndex={0} aria-label="FITS header cards">{data.cards.join("\n")}</pre>
          {data.truncated ? <p>Only the first {data.cards.length} cards are shown.</p> : null}
          <a className={s.save} href={`/api/datasets/${id}/header-cards?format=text`} download>Save as text</a>
        </>
      ) : null}
    </details>
  );
}
