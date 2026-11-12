import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import {
  authState, confirmReplacement, confirmSignIn, createToken, listAccounts, listTokens, newRecoveryCodes, replaceSecondFactor,
  resetSecondFactor, revokeToken, signOut, signOutEverywhere,
} from "../api/accounts";
import type { AccessToken, Account, AuthState, Scope } from "../api/accounts";
import { ApiError } from "../api/http";
import { Button, Checkbox, Input, LinkButton } from "../ui";
import { RecoveryCodes, TotpEnrolment } from "./SignInPage";
import s from "./Account.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");
const day = (iso: string | null | undefined) => (iso ? iso.slice(0, 10) : "never");
const SCOPES: { scope: Scope; label: string }[] = [
  { scope: "read", label: "read: everything a signed-in reader sees" },
  { scope: "write", label: "write: read, and the same changes you can make (never delete, publish or manage accounts)" },
  { scope: "push", label: "push: send figures and runs from a linked computer (not yet available)" },
  { scope: "relay", label: "relay: answer run requests from a linked computer (not yet available)" },
];

/** The signed-in account: sign out, confirm a recent sign-in, personal access tokens, recovery codes, the second factor, and
 * for admins the accounts with the second-factor reset. */
export function AccountPage() {
  const [state, setState] = useState<AuthState | null>(null);
  const [error, setError] = useState("");
  const refresh = useCallback(() => authState().then(setState, (e) => setError(message(e))), []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  if (!state?.account) return <main className={s.page}>{error ? <p className={s.error} role="alert">{error}</p> : null}</main>;
  const a = state.account;
  return (
    <main className={`${s.page} ${s.wide}`}>
      <div className={s.row}>
        <h1 className={s.h1}>{a.name}</h1>
        <span className={s.note}>{a.username}{a.admin ? " · admin" : ""}</span>
      </div>
      <div className={s.row}>
        <LinkButton href="#/">Back to the workbench</LinkButton>
        <Button onClick={() => void signOut().finally(() => { location.hash = ""; location.reload(); })}>Sign out</Button>
        <Button onClick={() => void signOutEverywhere().finally(() => { location.hash = ""; location.reload(); })}>Sign out everywhere</Button>
      </div>
      {state.recent ? null : <Confirm minutes={state.recentMinutes} onDone={setState} />}
      <Tokens recent={state.recent} />
      <SecondFactor recent={state.recent} left={state.recoveryCodesLeft} onChanged={() => void refresh()} />
      {a.admin ? <Accounts recent={state.recent} self={a.id} /> : null}
    </main>
  );
}

function Confirm({ minutes, onDone }: { minutes: number; onDone: (s: AuthState) => void }) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    confirmSignIn({ password, code }).then(onDone, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <section className={s.card}>
      <h2 className={s.h2}>Confirm it is you</h2>
      <p className={s.note}>Creating a token, changing the second factor, deleting, publishing outside the group and admin actions need a sign-in within the last {minutes} minutes.</p>
      <form className={s.form} onSubmit={submit}>
        <Input label="Password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        <Input label="Code from your authenticator app" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
        {error ? <p className={s.error} role="alert">{error}</p> : null}
        <div className={s.row}>
          <Button type="submit" variant="primary" disabled={busy}>Confirm</Button>
        </div>
      </form>
    </section>
  );
}

function Tokens({ recent }: { recent: boolean }) {
  const [tokens, setTokens] = useState<AccessToken[]>([]);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(["read"]);
  const [days, setDays] = useState("30");
  const [shown, setShown] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(() => listTokens().then((r) => setTokens(r.tokens), (e) => setError(message(e))), []);
  useEffect(() => {
    void load();
  }, [load]);
  const create = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    createToken({ name, scopes, days: Number(days) }).then((r) => {
      setShown(r.token);
      setName("");
      void load();
    }, (err) => setError(message(err)));
  };
  return (
    <section className={s.card}>
      <h2 className={s.h2}>Personal access tokens</h2>
      <p className={s.note}>For the agent CLI and other programs: sent as <code>Authorization: Bearer</code>. A token acts as you within its scopes until it expires or you revoke it.</p>
      {shown ? (
        <div className={s.form}>
          <p className={s.note}>Copy the token now. It is not stored and cannot be shown again.</p>
          <p className={s.secret} aria-label="New token">{shown}</p>
          <div className={s.row}><Button onClick={() => setShown("")}>Done</Button></div>
        </div>
      ) : null}
      {tokens.length ? (
        <table className={s.table}>
          <thead><tr><th>Name</th><th>Scopes</th><th>Created</th><th>Expires</th><th>Last used</th><th /></tr></thead>
          <tbody>
            {tokens.map((t) => (
              <tr key={t.id}>
                <td>{t.name}</td>
                <td>{t.scopes.join(", ")}</td>
                <td>{day(t.createdAt)}</td>
                <td>{day(t.expiresAt)}</td>
                <td>{day(t.lastUsedAt)}</td>
                <td>{t.revokedAt ? `revoked ${day(t.revokedAt)}` : <Button variant="danger" onClick={() => void revokeToken(t.id).then(load, (e) => setError(message(e)))}>Revoke</Button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : <p className={s.note}>No tokens.</p>}
      <form className={s.form} onSubmit={create}>
        <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} required />
        <fieldset className={s.form} style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className={s.note}>Scopes</legend>
          {SCOPES.map((o) => (
            <Checkbox key={o.scope} label={o.label} checked={scopes.includes(o.scope)} onChange={(e) => setScopes(e.target.checked ? [...scopes, o.scope] : scopes.filter((x) => x !== o.scope))} />
          ))}
        </fieldset>
        <Input label="Days until it expires" type="number" min={1} max={365} value={days} onChange={(e) => setDays(e.target.value)} />
        {error ? <p className={s.error} role="alert">{error}</p> : null}
        <div className={s.row}>
          <Button type="submit" variant="primary" disabled={!recent || !scopes.length}>Create token</Button>
          {recent ? null : <span className={s.note}>Confirm it is you first.</span>}
        </div>
      </form>
    </section>
  );
}

function SecondFactor({ recent, left, onChanged }: { recent: boolean; left: number; onChanged: () => void }) {
  const [codes, setCodes] = useState<string[] | null>(null);
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState("");
  const confirm = useCallback((code: string) => confirmReplacement(code).then((r) => r.recoveryCodes), []);
  return (
    <section className={s.card}>
      <h2 className={s.h2}>Second factor</h2>
      <p className={s.note}>{left} of 10 recovery codes left.</p>
      {codes ? <RecoveryCodes codes={codes} onDone={() => { setCodes(null); onChanged(); }} /> : null}
      {replacing ? <TotpEnrolment start={replaceSecondFactor} confirm={confirm} onFinished={() => { setReplacing(false); onChanged(); }} /> : null}
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {codes || replacing ? null : (
        <div className={s.row}>
          <Button disabled={!recent} onClick={() => void newRecoveryCodes().then((r) => setCodes(r.recoveryCodes), (e) => setError(message(e)))}>New recovery codes</Button>
          <Button disabled={!recent} onClick={() => setReplacing(true)}>Move to a new phone</Button>
        </div>
      )}
    </section>
  );
}

function Accounts({ recent, self }: { recent: boolean; self: string }) {
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => listAccounts().then((r) => setAccounts(r.accounts), (e) => setError(message(e))), []);
  useEffect(() => {
    if (recent) void load();
  }, [recent, load]);
  return (
    <section className={s.card}>
      <h2 className={s.h2}>Accounts</h2>
      {recent ? null : <p className={s.note}>Confirm it is you to see and manage accounts.</p>}
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {accounts ? (
        <table className={s.table}>
          <thead><tr><th>Name</th><th>Username</th><th>Second factor</th><th /></tr></thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.id}>
                <td>{a.name}{a.admin ? " (admin)" : ""}</td>
                <td>{a.username}</td>
                <td>{a.secondFactor ? "enrolled" : "not enrolled"}</td>
                <td>
                  {a.secondFactor && a.id !== self ? (
                    <Button variant="danger" onClick={() => { if (confirm(`Remove ${a.name}'s second factor and sign them out? They enrol again at their next sign-in.`)) void resetSecondFactor(a.id).then(load, (e) => setError(message(e))); }}>Reset second factor</Button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </section>
  );
}
