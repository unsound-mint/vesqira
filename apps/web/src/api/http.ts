/**
 * The one fetch wrapper for every client in api/. Both listeners answer errors their own way: the owner API sends JSON
 * `{error}`, the reviewer listener and the proxies in front of either send text/plain (400, 415, 429, 500). Both end as an
 * ApiError whose message is the server's own words. It imports nothing, so the reviewer bundle can reach it.
 */

export class ApiError extends Error {
  /** `status` is 0 when no response arrived; `body` is the parsed JSON error body (or null), for callers with a special case such as a 409 carrying `current`. */
  constructor(message: string, readonly reasons: string[] = [], readonly status = 0, readonly body: unknown = null) {
    super(message);
  }
}

const MAX_TEXT = 300;
const REVIEW_PREFIX = "/review-api";

function unreachable(url: string): ApiError {
  // A reviewer is a stranger on someone else's link: the owner's local address means nothing to them.
  return new ApiError(url.startsWith(REVIEW_PREFIX) ? "Cannot reach the Vesqira API." : "Cannot reach the Vesqira API. Is it running on 127.0.0.1:8787?");
}

async function failure(res: Response, url: string): Promise<ApiError> {
  const fallback = `Request failed (${res.status})`;
  if (res.status === 404 && url.startsWith(REVIEW_PREFIX)) return new ApiError("This review link is not valid, has expired, or was revoked.", [], 404);
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
      return new ApiError(message, Array.isArray(o?.reasons) ? (o.reasons as string[]) : [], res.status, body);
    }
  }
  const t = text.trim();
  return new ApiError(t ? (t.length > MAX_TEXT ? `${t.slice(0, MAX_TEXT)}…` : t) : fallback, [], res.status);
}

export type RequestOptions = {
  /** Statuses that are returned to the caller instead of thrown (only for requestRaw). */
  allow?: number[];
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
  if (!res.ok && !opts.allow?.includes(res.status)) throw await failure(res, url);
  return res;
}

/** The parsed JSON body; an empty or unreadable success body is null. */
export async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await requestRaw(url, init);
  return (await res.json().catch(() => null)) as T;
}

const withBody = (method: string, body: unknown, signal?: AbortSignal): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal });

export const getJSON = <T,>(url: string, signal?: AbortSignal) => request<T>(url, { signal });
export const postJSON = <T,>(url: string, body: unknown = {}, signal?: AbortSignal) => request<T>(url, withBody("POST", body, signal));
export const putJSON = <T,>(url: string, body: unknown, signal?: AbortSignal) => request<T>(url, withBody("PUT", body, signal));
export const del = <T,>(url: string, signal?: AbortSignal) => request<T>(url, { method: "DELETE", signal });
