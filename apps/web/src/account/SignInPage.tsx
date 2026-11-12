import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { authState, confirmEnrolment, secondFactor, setup, signIn, signOut, startEnrolment } from "../api/accounts";
import type { AuthState, Enrolment } from "../api/accounts";
import { ApiError } from "../api/http";
import { Button, Input } from "../ui";
import s from "./Account.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");

/** Reloads into the workbench once the session is full. */
const enter = () => {
  history.replaceState(null, "", location.pathname);
  location.reload();
};

/** The two sign-in steps of the group server: password, then a TOTP or recovery code (or, the first time, enrolling TOTP). */
export function SignInPage() {
  const [state, setState] = useState<AuthState | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void authState().then(setState, (e) => setError(message(e)));
  }, []);
  if (!state) return <main className={s.page}>{error ? <p className={s.error} role="alert">{error}</p> : null}</main>;
  if (state.stage === "full") {
    enter();
    return null;
  }
  return (
    <main className={s.page}>
      <h1 className={s.h1}>Sign in to Vesqira</h1>
      {state.stage === "none" ? <PasswordStep state={state} onDone={setState} /> : null}
      {state.stage === "second_factor" ? <CodeStep onRestart={() => void signOut().finally(() => setState({ ...state, stage: "none", account: null }))} /> : null}
      {state.stage === "enrol" ? <EnrolStep /> : null}
    </main>
  );
}

function PasswordStep({ state, onDone }: { state: AuthState; onDone: (s: AuthState) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    signIn(username, password).then(onDone, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <section className={s.card}>
      {state.setupNeeded ? (
        <p className={s.note}>
          This server has no admin yet. On the server, run <code>vesqira-api admin bootstrap --db &lt;catalog&gt; --public-origin {location.origin}</code> and open the link it prints.
        </p>
      ) : null}
      <form className={s.form} onSubmit={submit}>
        <Input label="Username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        <Input label="Password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        {error ? <p className={s.error} role="alert">{error}</p> : null}
        <div className={s.row}>
          <Button type="submit" variant="primary" disabled={busy}>Continue</Button>
        </div>
      </form>
    </section>
  );
}

function CodeStep({ onRestart }: { onRestart: () => void }) {
  const [code, setCode] = useState("");
  const [recovery, setRecovery] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    secondFactor(recovery ? { recoveryCode: code } : { code }).then(enter, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <section className={s.card}>
      <form className={s.form} onSubmit={submit}>
        {recovery ? (
          <Input label="Recovery code" autoComplete="off" value={code} onChange={(e) => setCode(e.target.value)} required />
        ) : (
          <Input label="Code from your authenticator app" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
        )}
        {error ? <p className={s.error} role="alert">{error}</p> : null}
        <div className={s.row}>
          <Button type="submit" variant="primary" disabled={busy}>Sign in</Button>
          <Button variant="ghost" onClick={() => { setRecovery(!recovery); setCode(""); }}>{recovery ? "Use the app's code" : "Use a recovery code"}</Button>
          <Button variant="ghost" onClick={onRestart}>Start again</Button>
        </div>
      </form>
    </section>
  );
}

/** Shows a new TOTP secret and its otpauth link, takes the first code, then shows the recovery codes once. */
export function TotpEnrolment({ start, confirm, onFinished }: { start: () => Promise<Enrolment>; confirm: (code: string) => Promise<string[]>; onFinished: () => void }) {
  const [en, setEn] = useState<Enrolment | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return; // each start replaces the pending secret on the server: ask once, even when an effect runs twice
    started.current = true;
    void start().then(setEn, (e) => setError(message(e)));
  }, [start]);
  if (codes) return <RecoveryCodes codes={codes} onDone={onFinished} />;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    confirm(code).then(setCodes, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <div className={s.form}>
      <p className={s.note}>Add this account to an authenticator app (any app that supports TOTP). Open the link on the phone, or type the key.</p>
      {en ? (
        <>
          <a className={s.link} href={en.uri}>Open in an authenticator app</a>
          <p className={s.secret} aria-label="Setup key">{en.secret.replace(/(.{4})/g, "$1 ").trim()}</p>
        </>
      ) : null}
      <form className={s.form} onSubmit={submit}>
        <Input label="First code the app shows" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
        {error ? <p className={s.error} role="alert">{error}</p> : null}
        <div className={s.row}>
          <Button type="submit" variant="primary" disabled={busy || !en}>Confirm</Button>
        </div>
      </form>
    </div>
  );
}

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  return (
    <div className={s.form}>
      <h2 className={s.h2}>Recovery codes</h2>
      <p className={s.note}>Each code signs you in once if the phone is lost. They are shown only now: save them somewhere safe, away from the phone.</p>
      <ul className={s.codes} aria-label="Recovery codes">
        {codes.map((c) => <li key={c}>{c}</li>)}
      </ul>
      <div className={s.row}>
        <Button variant="primary" onClick={onDone}>I saved them</Button>
      </div>
    </div>
  );
}

function EnrolStep() {
  return (
    <section className={s.card}>
      <h2 className={s.h2}>Set up the second factor</h2>
      <TotpEnrolment start={startEnrolment} confirm={(code) => confirmEnrolment(code).then((r) => r.recoveryCodes)} onFinished={enter} />
    </section>
  );
}

/** The page the bootstrap command's link opens: create the admin, then enrol TOTP. */
export function SetupPage({ token }: { token: string }) {
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [created, setCreated] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (password !== again) {
      setError("The two passwords differ.");
      return;
    }
    setBusy(true);
    setError("");
    setup({ token, name, username, password }).then(() => {
      history.replaceState(null, "", location.pathname); // the link is spent: keep it out of the address bar and history
      setCreated(true);
    }, (err) => setError(message(err))).finally(() => setBusy(false));
  };
  return (
    <main className={s.page}>
      <h1 className={s.h1}>Create the admin account</h1>
      {created ? (
        <EnrolStep />
      ) : (
        <section className={s.card}>
          <form className={s.form} onSubmit={submit}>
            <Input label="Your name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} required />
            <Input label="Username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
            <Input label="Password" type="password" autoComplete="new-password" minLength={12} value={password} onChange={(e) => setPassword(e.target.value)} required />
            <Input label="Password again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required />
            <p className={s.note}>A username is letters, digits, dot, underscore or hyphen. A password is at least 12 characters; a few unrelated words work well.</p>
            {error ? <p className={s.error} role="alert">{error}</p> : null}
            <div className={s.row}>
              <Button type="submit" variant="primary" disabled={busy}>Create account</Button>
            </div>
          </form>
        </section>
      )}
    </main>
  );
}
