import { Suspense, lazy, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { authState, serverMode, signOut } from "../api/accounts";
import { Button, LinkButton } from "../ui";

// The sign-in and account pages exist only on the group server, so local mode never downloads them.
const SignInPage = lazy(() => import("./SignInPage").then((m) => ({ default: m.SignInPage })));
const SetupPage = lazy(() => import("./SignInPage").then((m) => ({ default: m.SetupPage })));
const AccountPage = lazy(() => import("./AccountPage").then((m) => ({ default: m.AccountPage })));

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
  }, [mode]);

  if (hash.startsWith("#/setup/")) {
    return (
      <Suspense fallback={null}>
        <SetupPage token={decodeURIComponent(hash.slice("#/setup/".length))} />
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
  const account = (
    <>
      <LinkButton href="#/account" variant="ghost" aria-label={`Account: ${name || "signed in"}`}>{name || "Account"}</LinkButton>
      <Button variant="ghost" onClick={() => void signOut().finally(() => location.reload())}>Sign out</Button>
    </>
  );
  return <>{children(account)}</>;
}
