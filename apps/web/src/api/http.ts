/**
 * The one fetch wrapper for every client in api/. Both listeners answer errors their own way: the owner API sends JSON
 * `{error}`, the reviewer listener and the proxies in front of either send text/plain (400, 415, 429, 500). Both end as an
 * ApiError whose message is the server's own words. It imports nothing, so the reviewer bundle can reach it.
 */

export class ApiError extends Error {
  /** `status` is 0 when no response arrived; `body` is the parsed JSON error body (or null), for callers with a special case such as a 409 carrying `current`. */
  constructor(message: string, readonly reasons: string[] = [], readonly status = 0, readonly body: unknown = null, /** The server's `X-Request-Id`, to quote when reporting a problem; absent when no response arrived. */ readonly requestId?: string) {
    super(message);
  }
}

type ErrorListener = (e: ApiError & { requestId: string }) => void;
const errorListeners = new Set<ErrorListener>();

/** Calls `listener` with every thrown ApiError that carries a request id (never an abort). Returns the unsubscribe. */
export function onApiError(listener: ErrorListener): () => void {
  errorListeners.add(listener);
  return () => void errorListeners.delete(listener);
}

function announce(e: ApiError): ApiError {
  if (e.requestId) for (const l of [...errorListeners]) l(e as ApiError & { requestId: string });
  return e;
}

const MAX_TEXT = 300;
const REVIEW_PREFIX = "/review-api";

function unreachable(url: string): ApiError {
  // A reviewer is a stranger on someone else's link: the owner's local address means nothing to them.
  return new ApiError(url.startsWith(REVIEW_PREFIX) ? "Cannot reach the Vesqira API." : "Cannot reach the Vesqira API. Is it running on 127.0.0.1:8787?");
}

async function failure(res: Response, url: string): Promise<ApiError> {
  const fallback = `Request failed (${res.status})`;
  const rid = res.headers.get("x-request-id")?.trim() || undefined;
  if (res.status === 404 && url.startsWith(REVIEW_PREFIX)) return new ApiError("This review link is not valid, has expired, or was revoked.", [], 404, null, rid);
  const type = res.headers.get("content-type") ?? "";
  const text = await res.text().catch(() => "");
  // A body that says it is JSON, or looks like an object, is read as one; anything else is the message itself.
  const isJSON = /json/i.test(type);
  // A body that says it is JSON, or looks like an object, is read as one; anything else is the message itself.
  if (isJSON || text.trimStart().startsWith("{")) {
    let body: unknown = null;
    try {
      body = JSON.parse(text);
    } catch {
      /* an unreadable body is treated as no body */
    }
    if (body !== null || isJSON) {
      const o = typeof body === "object" ? (body as { error?: unknown; reasons?: unknown }) : null;
      const message = typeof o?.error === "string" && o.error ? o.error : fallback;
      return new ApiError(message, Array.isArray(o?.reasons) ? (o.reasons as string[]) : [], res.status, body, rid);
    }
  }
  const t = text.trim();
  return new ApiError(t ? (t.length > MAX_TEXT ? `${t.slice(0, MAX_TEXT)}…` : t) : fallback, [], res.status, null, rid);
}

export type RequestOptions = {
  /** Statuses that are returned to the caller instead of thrown (only for requestRaw). */
  allow?: number[];
  /** Not announced to onApiError (a background poll whose failure is nobody's business). */
  quiet?: boolean;
};

/** The checked Response, for callers that read the body themselves (binary planes). An abort is rethrown as it is. */
export async function requestRaw(url: string, init?: RequestInit, opts: RequestOptions = {}): Promise<Response> {
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw unreachable(url);
  }
  if (!res.ok && !opts.allow?.includes(res.status)) { const e = await failure(res, url); throw opts.quiet ? e : announce(e); }
  return res;
}

/** The parsed JSON body; an empty or unreadable success body is null. */
export async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await requestRaw(url, init);
  return (await res.json().catch(() => null)) as T;
}

const withBody = (method: string, body: unknown, signal?: AbortSignal, requestId?: string): RequestInit => ({ method, headers: { "Content-Type": "application/json", ...idHeader(requestId) }, body: JSON.stringify(body), signal });

/** The id a client gives a slow request (`X-Request-Id`), so GET /api/workload can say why it is waiting. [A-Za-z0-9._-], 8 to 64 chars. */
export function newRequestId(): string {
  const c = globalThis.crypto;
  if (typeof c?.randomUUID === "function") return c.randomUUID().replace(/-/g, "");
  return Array.from({ length: 4 }, () => Math.floor(Math.random() * 0x100000000).toString(16).padStart(8, "0")).join("");
}

/** The request header carrying an id, or nothing. */
export const idHeader = (requestId?: string): Record<string, string> => (requestId ? { "X-Request-Id": requestId } : {});

export const getJSON = <T,>(url: string, signal?: AbortSignal, requestId?: string) => request<T>(url, { signal, headers: idHeader(requestId) });
export const postJSON = <T,>(url: string, body: unknown = {}, signal?: AbortSignal, requestId?: string) => request<T>(url, withBody("POST", body, signal, requestId));
export const putJSON = <T,>(url: string, body: unknown, signal?: AbortSignal) => request<T>(url, withBody("PUT", body, signal));
export const del = <T,>(url: string, signal?: AbortSignal) => request<T>(url, { method: "DELETE", signal });
