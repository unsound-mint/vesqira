import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { ApiError } from "../api/http";
import { acceptInvitation, joinWithInvitation, peekInvitation, roleName } from "../api/workspaces";
import type { InvitationPeek } from "../api/workspaces";
import { Button, Input, LinkButton } from "../ui";
import s from "./Account.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");

/**
 * Accepting an invitation (ADR 0050 slice 3). Someone without an account chooses a name, a username and a password, then
 * enrols the second factor on the sign-in page; someone already signed in joins the workspace with one button.
 */
export function InvitePage({ token, signedIn }: { token: string; signedIn: boolean }) {
  const [peek, setPeek] = useState<InvitationPeek | null>(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void peekInvitation(token).then(setPeek, (e) => setError(message(e)));
  }, [token]);
  const done = () => {
    history.replaceState(null, "", location.pathname);
    location.reload();
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    acceptInvitation({ token, name, username, password }).then(done, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <main className={s.page}>
      <h1 className={s.h1}>Join a workspace</h1>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {peek ? (
        <section className={s.card}>
          <p>
            You are invited to <strong>{peek.workspace}</strong> as {roleName(peek.role)}.{peek.note ? ` (${peek.note})` : ""} The link works once, until {peek.expiresAt.slice(0, 10)}.
          </p>
          {signedIn ? (
            <div className={s.row}>
              <Button variant="primary" disabled={busy} onClick={() => { setBusy(true); void joinWithInvitation(token).then(done, (e) => setError(message(e))).finally(() => setBusy(false)); }}>Join {peek.workspace}</Button>
              <LinkButton href="#/">Not now</LinkButton>
            </div>
          ) : (
            <form className={s.form} onSubmit={submit}>
              <Input label="Your name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
              <Input label="Username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required hint="letters, digits, dot, underscore or hyphen" />
              <Input label="Password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={12} hint="at least 12 characters; next you set up a code app" />
              <div className={s.row}><Button type="submit" variant="primary" disabled={busy}>Create my account</Button></div>
            </form>
          )}
        </section>
      ) : null}
    </main>
  );
}
