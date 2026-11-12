import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/http";
import { archivePushed, getPushed, listPushed, matchText, pushedAssetURL } from "../api/push";
import type { PushedItem, PushedSummary } from "../api/push";
import { Button, LinkButton } from "../ui";
import { useCan } from "../account/permissions";
import s from "../account/Account.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");
const when = (iso: string) => iso.slice(0, 16).replace("T", " ");

/**
 * What members pushed from their own computers to this workspace (ADR 0066 section 2): the newest version of each item, its
 * earlier versions, its picture and the datasets it was made from, which link to this server's datasets only on a verified
 * fingerprint. Plain on purpose: the owner will redesign it.
 */
export function PushedPage() {
  const can = useCan();
  const [items, setItems] = useState<PushedSummary[]>([]);
  const [open, setOpen] = useState<PushedItem | null>(null);
  const [error, setError] = useState("");
  const refresh = useCallback(() => listPushed().then((r) => setItems(r.items), (e) => setError(message(e))), []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const show = (id: number) => void getPushed(id).then(setOpen, (e) => setError(message(e)));
  const d = open?.item;
  return (
    <main className={`${s.page} ${s.wide}`}>
      <div className={s.row}>
        <h1 className={s.h1}>Pushed work</h1>
        <LinkButton href="#/">Back to the workbench</LinkButton>
      </div>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      <section className={s.card}>
        {items.length ? (
          <table className={s.table}>
            <thead><tr><th>Item</th><th>Kind</th><th>Pushed by</th><th>When</th><th /></tr></thead>
            <tbody>
              {items.map((x) => (
                <tr key={x.id}>
                  <td><Button variant="ghost" onClick={() => show(x.id)}>{x.title}</Button></td>
                  <td>{x.kind}</td>
                  <td>{x.pushedByName}</td>
                  <td>{when(x.pushedAt)}</td>
                  <td>{can("pushed.archive") ? <Button onClick={() => void archivePushed(x.id).then(refresh, (e) => setError(message(e)))}>Archive</Button> : null}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : <p className={s.note}>Nothing has been pushed to this workspace yet. A member links their computer under Account, Link a computer.</p>}
      </section>
      {d ? (
        <section className={s.card} aria-label={`Pushed ${d.kind}: ${d.title}`}>
          <h2 className={s.h2}>{d.title}</h2>
          <p className={s.note}>{d.kind}, pushed by {open.pushedByName} at {when(d.pushedAt)}{d.supersededBy ? "; a newer version has been pushed since" : ""}. A frozen copy: it is never edited here.</p>
          {d.item.omitted ? <p className={s.note}>{d.item.omitted} run setting(s) naming a machine, an account or an address were left out on the pusher&apos;s computer.</p> : null}
          {d.item.assets.slice(0, 12).map((sha) => <img key={sha} src={pushedAssetURL(d.id, sha)} alt={`Picture of ${d.title}`} style={{ maxWidth: "100%", height: "auto" }} />)}
          {d.references.length ? (
            <>
              <h3 className={s.h2}>Datasets</h3>
              <ul>{d.references.map((r) => <li key={r.n}>{r.name} ({r.role}): {matchText(r)}</li>)}</ul>
            </>
          ) : null}
          {d.lineage.length > 1 ? (
            <>
              <h3 className={s.h2}>Versions</h3>
              <ol>{d.lineage.map((v) => <li key={v.id}><Button variant="ghost" onClick={() => show(v.id)}>{when(v.pushedAt)}</Button>{v.supersededBy ? " (superseded)" : " (newest)"}</li>)}</ol>
            </>
          ) : null}
        </section>
      ) : null}
    </main>
  );
}
