import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { ApiError } from "../api/http";
import { linkGroup, outbox, retryOutbox, unlinkGroup } from "../api/push";
import type { GroupLink, OutboxEntry } from "../api/push";
import { Button, Input, LinkButton } from "../ui";
import { forgetLink } from "./PushButton";
import s from "../account/Account.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");

/**
 * The local instance's link to a group server and its push queue (ADR 0066 sections 1 and 2). Linking takes the server's https
 * address and the token from "Link a computer" there; the token is kept in a file beside the catalog and never shown again.
 * Plain on purpose: the owner will redesign it.
 */
export function GroupPage() {
  const [link, setLink] = useState<GroupLink | null>(null);
  const [entries, setEntries] = useState<OutboxEntry[]>([]);
  const [origin, setOrigin] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = useCallback(() => outbox().then((o) => { setLink(o.link); setEntries(o.entries); }, (e) => setError(message(e))), []);
  useEffect(() => {
    void refresh();
    const t = setInterval(() => void refresh(), 5000);
    return () => clearInterval(t);
  }, [refresh]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    linkGroup(origin, token).then((l) => { forgetLink(); setLink(l); setToken(""); void refresh(); }, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <main className={`${s.page} ${s.wide}`}>
      <div className={s.row}>
        <h1 className={s.h1}>Group server</h1>
        <LinkButton href="#/">Back to the workbench</LinkButton>
      </div>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {link?.linked ? (
        <section className={s.card}>
          <h2 className={s.h2}>Linked to {link.workspace || "a workspace"}</h2>
          <p className={s.note}>{link.origin}, since {link.linkedAt?.slice(0, 10)}. Pushing sends frozen copies (the picture, the reviewer-safe provenance, the run line, a board snapshot or a report revision) and never a path, host name, cluster user name or this computer&apos;s ids.</p>
          <div className={s.row}>
            <Button onClick={() => void retryOutbox().then(refresh, (e) => setError(message(e)))}>Try waiting items now</Button>
            <Button variant="danger" onClick={() => { if (confirm("Unlink this computer? The token file is deleted and the queue emptied. Your work here is untouched, what was pushed stays on the server, and linking again to the same server continues where it left off.")) void unlinkGroup().then(() => { forgetLink(); return refresh(); }, (e) => setError(message(e))); }}>Unlink</Button>
          </div>
        </section>
      ) : (
        <section className={s.card}>
          <h2 className={s.h2}>Link this computer</h2>
          <p className={s.note}>On the group server, open your account and choose Link a computer. Paste its address and the token here.</p>
          <form className={s.form} onSubmit={submit}>
            <Input label="Group server address (https)" placeholder="https://lab.example.org" value={origin} onChange={(e) => setOrigin(e.target.value)} required />
            <Input label="Token" type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} required />
            <div className={s.row}><Button type="submit" variant="primary" disabled={busy}>{busy ? "Checking…" : "Link"}</Button></div>
          </form>
        </section>
      )}
      <section className={s.card}>
        <h2 className={s.h2}>Pushed from this computer</h2>
        {entries.length ? (
          <table className={s.table}>
            <thead><tr><th>Item</th><th>Kind</th><th>Status</th><th>Queued</th></tr></thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id}>
                  <td>{e.title}</td>
                  <td>{e.kind}</td>
                  <td role="status">{e.status}{e.state === "waiting" && e.nextAt ? ` (next try ${e.nextAt.slice(11, 16)} UTC)` : ""}</td>
                  <td>{e.createdAt.slice(0, 16).replace("T", " ")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : <p className={s.note}>Nothing pushed yet. Use “Push to …” on a board, a figure, a run or a report.</p>}
      </section>
    </main>
  );
}
