import { Suspense, lazy, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { authState, serverMode, signOut } from "../api/accounts";
import { setWorkspace } from "../api/http";
import { workspaceContext } from "../api/workspaces";
import type { WorkspaceContext } from "../api/workspaces";
import { Button, LinkButton } from "../ui";
import { PermissionsContext } from "./permissions";
import s from "./Account.module.css";

// The sign-in and account pages exist only on the group server, so local mode never downloads them.
const SignInPage = lazy(() => import("./SignInPage").then((m) => ({ default: m.SignInPage })));
const SetupPage = lazy(() => import("./SignInPage").then((m) => ({ default: m.SetupPage })));
const AccountPage = lazy(() => import("./AccountPage").then((m) => ({ default: m.AccountPage })));
const MembersPage = lazy(() => import("./MembersPage").then((m) => ({ default: m.MembersPage })));
const InvitePage = lazy(() => import("./InvitePage").then((m) => ({ default: m.InvitePage })));

const STORED = "vesqira.workspace";
const stored = (): number | null => {
  try {
    const v = Number(localStorage.getItem(STORED));
    return Number.isInteger(v) && v > 0 ? v : null;
  } catch {
    return null;
  }
};
const store = (id: number) => {
  try {
    localStorage.setItem(STORED, String(id));
  } catch {
    /* the choice is a convenience; without storage the oldest membership is used */
  }
};

function useHash(): string {
  const [hash, setHash] = useState(location.hash);
  useEffect(() => {
    const on = () => setHash(location.hash);
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return hash;
}

/**
 * Decides what the workbench address shows (ADR 0050 slice 2). In local mode it renders the workbench exactly as before, with
 * no account control. On the group server it shows the setup page for a bootstrap link, the sign-in steps until a session is
 * full, then the workbench with the account's name in the title bar, or the account page at #/account.
 */
export function AccountGate({ children }: { children: (account: ReactNode) => ReactNode }) {
  const hash = useHash();
  const [mode, setMode] = useState<"loading" | "local" | "group" | "signin">("loading");
  const [name, setName] = useState("");
  const [ws, setWs] = useState<WorkspaceContext | null>(null);
  const [wsLoaded, setWsLoaded] = useState(false);
  useEffect(() => {
    let live = true;
    void serverMode().then((m) => {
      if (live) setMode(m);
    });
    return () => {
      live = false;
    };
  }, []);
  useEffect(() => {
    if (mode !== "group") return;
    void authState().then((s) => setName(s.account?.name ?? ""), () => setName(""));
    // the workspace this browser last chose, if the account still belongs to it; otherwise the server's default
    const want = stored();
    setWorkspace(want);
    void workspaceContext()
      .then((c) => {
        if (want !== null && c?.id !== want) {
          // the stored choice is not (or no longer) one of this account's workspaces: forget it and use the default
          setWorkspace(null);
          try {
            localStorage.removeItem(STORED);
          } catch {
            /* nothing stored to forget */
          }
          return workspaceContext();
        }
        return c;
      })
      .then(
        (c) => {
          if (c) setWorkspace(c.id);
          setWs(c);
          setWsLoaded(true);
        },
        () => {
          setWs(null);
          setWsLoaded(true);
        },
      );
  }, [mode]);

  if (hash.startsWith("#/setup/")) {
    return (
      <Suspense fallback={null}>
        <SetupPage token={decodeURIComponent(hash.slice("#/setup/".length))} />
      </Suspense>
    );
  }
  if (hash.startsWith("#/invite/") && mode !== "loading") {
    return (
      <Suspense fallback={null}>
        <InvitePage token={decodeURIComponent(hash.slice("#/invite/".length))} signedIn={mode === "group"} />
      </Suspense>
    );
  }
  if (mode === "loading") return null;
  if (mode === "local") return <>{children(null)}</>;
  if (mode === "signin") {
    return (
      <Suspense fallback={null}>
        <SignInPage />
      </Suspense>
    );
  }
  if (hash === "#/account") {
    return (
      <Suspense fallback={null}>
        <AccountPage />
      </Suspense>
    );
  }
  const can = (p: string) => !!ws?.permissions.includes(p);
  if (mode === "group" && wsLoaded && !ws && hash !== "#/account") {
    return (
      <main className={s.page}>
        <p role="status" className={s.note}>You are signed in as {name || "this account"} but are not a member of any workspace. Ask a workspace Owner for an invitation, or open <a href="#/account">your account</a>.</p>
      </main>
    );
  }
  if (hash === "#/members" && ws) {
    return (
      <PermissionsContext.Provider value={can}>
        <Suspense fallback={null}>
          <MembersPage myRole={ws.role} />
        </Suspense>
      </PermissionsContext.Provider>
    );
  }
  const choose = (id: number) => {
    store(id);
    location.reload(); // every open view belongs to the workspace it was loaded in
  };
  const account = (
    <>
      {ws && ws.workspaces.length > 1 ? (
        <select aria-label="Workspace" value={ws.id} onChange={(e) => choose(Number(e.target.value))}>
          {ws.workspaces.map((w) => <option key={w.workspaceId} value={w.workspaceId}>{w.workspaceName}</option>)}
        </select>
      ) : null}
      {ws ? <LinkButton href="#/members" variant="ghost" aria-label={`Members of ${ws.name}`}>{ws.workspaces.length > 1 ? "Members" : `${ws.name} · members`}</LinkButton> : null}
      <LinkButton href="#/account" variant="ghost" aria-label={`Account: ${name || "signed in"}`}>{name || "Account"}</LinkButton>
      <Button variant="ghost" onClick={() => void signOut().finally(() => location.reload())}>Sign out</Button>
    </>
  );
  return <PermissionsContext.Provider value={can}>{children(account)}</PermissionsContext.Provider>;
}
