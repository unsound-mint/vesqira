import { getJSON, postJSON } from "./http";
import type { components } from "./schema";

type Schemas = components["schemas"];

/** The group server's accounts (ADR 0050 slice 2). None of this exists in local mode. */
export type Account = Schemas["Account"];
export type AuthState = Schemas["AuthState"];
export type Enrolment = Schemas["Enrolment"];
export type AccessToken = Schemas["AccessToken"];
export type AccessTokenList = Schemas["AccessTokenList"];
export type CreatedToken = Schemas["CreatedToken"];
export type Scope = Schemas["CreateTokenRequest"]["scopes"][number];

/**
 * Which kind of server this page talks to, from the capabilities call the app makes anyway: "local" (no accounts), "group"
 * (signed in), or "signin" (the group server answered 401 signin_required). A failure to reach the API counts as local, so the
 * workbench shows its own error as before.
 */
export async function serverMode(): Promise<"local" | "group" | "signin"> {
  let res: Response;
  try {
    res = await fetch("/api/capabilities");
  } catch {
    return "local";
  }
  const body = (await res.json().catch(() => null)) as { code?: string; features?: { accounts?: boolean } } | null;
  if (res.status === 401 && body?.code === "signin_required") return "signin";
  return body?.features?.accounts ? "group" : "local";
}

export const authState = () => getJSON<AuthState>("/api/auth/state");
export const setup = (b: { token: string; name: string; username: string; password: string }) => postJSON<AuthState>("/api/auth/setup", b);
export const signIn = (username: string, password: string) => postJSON<AuthState>("/api/auth/sign-in", { username, password });
export const secondFactor = (b: { code: string } | { recoveryCode: string }) => postJSON<AuthState>("/api/auth/second-factor", b);
export const startEnrolment = () => postJSON<Enrolment>("/api/auth/enrolment");
export const confirmEnrolment = (code: string) => postJSON<Schemas["EnrolmentConfirmed"]>("/api/auth/enrolment/confirm", { code });
export const signOut = () => postJSON<null>("/api/auth/sign-out");
export const signOutEverywhere = () => postJSON<null>("/api/auth/sign-out-everywhere");
export const confirmSignIn = (b: { password: string; code?: string; recoveryCode?: string }) => postJSON<AuthState>("/api/auth/confirm", b);
export const replaceSecondFactor = () => postJSON<Enrolment>("/api/account/second-factor");
export const confirmReplacement = (code: string) => postJSON<Schemas["RecoveryCodes"]>("/api/account/second-factor/confirm", { code });
export const newRecoveryCodes = () => postJSON<Schemas["RecoveryCodes"]>("/api/account/recovery-codes");
export const listTokens = () => getJSON<AccessTokenList>("/api/account/tokens");
export const createToken = (b: { name: string; scopes: Scope[]; days: number }) => postJSON<CreatedToken>("/api/account/tokens", b);
export const revokeToken = (id: string) => postJSON<AccessToken>(`/api/account/tokens/${encodeURIComponent(id)}/revoke`);
export const listAccounts = () => getJSON<Schemas["AccountList"]>("/api/accounts");
export const resetSecondFactor = (id: string) => postJSON<null>(`/api/accounts/${encodeURIComponent(id)}/reset-second-factor`);
