import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, newRequestId, onApiError, postJSON, request, requestRaw } from "./http";

const answer = (body: string | null, status: number, type?: string) => vi.stubGlobal("fetch", vi.fn(async () => new Response(body, { status, headers: type ? { "Content-Type": type } : {} })));
const failure = async (p: Promise<unknown>) => (await p.then(() => null, (e: unknown) => e)) as ApiError;

afterEach(() => vi.unstubAllGlobals());

describe("request", () => {
  it("returns the JSON body, and null for an empty success", async () => {
    answer('{"a":1}', 200, "application/json");
    expect(await request("/api/x")).toEqual({ a: 1 });
    answer(null, 204);
    expect(await request("/api/x")).toBeNull();
  });

  it("uses the server's JSON error and keeps reasons, status and body", async () => {
    answer('{"error":"no good","reasons":["a","b"],"current":{"n":2}}', 409, "application/json; charset=utf-8");
    const e = await failure(request("/api/x"));
    expect(e).toBeInstanceOf(ApiError);
    expect([e.message, e.reasons, e.status, e.body]).toEqual(["no good", ["a", "b"], 409, { error: "no good", reasons: ["a", "b"], current: { n: 2 } }]);
  });

  it("uses a plain-text error as the message, trimmed and capped", async () => {
    answer("  slow down\n", 429, "text/plain; charset=utf-8");
    expect((await failure(request("/review-api/t"))).message).toBe("slow down");
    answer("x".repeat(1000), 500, "text/plain");
    expect((await failure(request("/api/x"))).message).toHaveLength(301);
  });

  it("falls back to the status when the body says nothing", async () => {
    answer(null, 502);
    expect((await failure(request("/api/x"))).message).toBe("Request failed (502)");
    answer("{}", 400, "application/json");
    expect((await failure(request("/api/x"))).message).toBe("Request failed (400)");
    answer("not json", 400, "application/json");
    expect((await failure(request("/api/x"))).message).toBe("Request failed (400)");
  });

  it("names the local address for the owner, never for a reviewer", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("down"))));
    expect((await failure(request("/api/x"))).message).toContain("127.0.0.1:8787");
    const r = await failure(request("/review-api/t"));
    expect(r.message).toBe("Cannot reach the Vesqira API.");
    expect(r.status).toBe(0);
  });

  it("explains a 404 on a review link, but not elsewhere", async () => {
    answer("not found", 404, "text/plain");
    expect((await failure(request("/review-api/t"))).message).toBe("This review link is not valid, has expired, or was revoked.");
    expect((await failure(request("/api/x"))).message).toBe("not found");
  });

  it("lets an abort through unchanged", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new DOMException("aborted", "AbortError"))));
    const e = await failure(requestRaw("/api/x", { signal: AbortSignal.abort() }));
    expect(e).not.toBeInstanceOf(ApiError);
    expect(e.name).toBe("AbortError");
  });
});

describe("requestRaw and postJSON", () => {
  it("returns allowed statuses and the unread body", async () => {
    answer(null, 501);
    expect((await requestRaw("/api/x", undefined, { allow: [501] })).status).toBe(501);
    answer("ab", 200, "application/octet-stream");
    expect(new Uint8Array(await (await requestRaw("/api/x")).arrayBuffer())).toHaveLength(2);
  });

  it("posts JSON", async () => {
    answer("{}", 200, "application/json");
    await postJSON("/api/x", { k: 1 });
    const [, init] = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [string, RequestInit];
    expect([init.method, init.body, (init.headers as Record<string, string>)["Content-Type"]]).toEqual(["POST", '{"k":1}', "application/json"]);
  });
});

describe("request id", () => {
  const withId = (body: string | null, status: number, type: string, rid: string) => vi.stubGlobal("fetch", vi.fn(async () => new Response(body, { status, headers: { "Content-Type": type, "X-Request-Id": rid } })));

  it("reads X-Request-Id from JSON and text/plain errors, and from a review link that is not valid", async () => {
    withId('{"error":"no good"}', 409, "application/json", "json-id-0001");
    expect((await failure(request("/api/x"))).requestId).toBe("json-id-0001");
    withId("slow down", 429, "text/plain", "text-id-0002");
    expect((await failure(request("/review-api/t"))).requestId).toBe("text-id-0002");
    withId("nope", 404, "text/plain", "gone-id-0003");
    expect((await failure(request("/review-api/t"))).requestId).toBe("gone-id-0003");
    answer("x", 500, "text/plain");
    expect((await failure(request("/api/x"))).requestId).toBeUndefined();
  });

  it("tells subscribers about each failed response, but not about aborts, unreachable errors or quiet requests", async () => {
    const seen: string[] = [];
    const off = onApiError((e) => seen.push(e.requestId));
    withId("boom", 500, "text/plain", "sub-id-00001");
    await failure(request("/api/x"));
    await failure(requestRaw("/api/x", undefined, { quiet: true }));
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new DOMException("aborted", "AbortError"))));
    await failure(request("/api/x"));
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("down"))));
    await failure(request("/api/x"));
    off();
    withId("boom", 500, "text/plain", "sub-id-00002");
    await failure(request("/api/x"));
    expect(seen).toEqual(["sub-id-00001"]);
  });

  it("does not announce a 4xx answer: the page shows it in place, and a 404 or 409 is not a failure to report", async () => {
    const seen: string[] = [];
    const off = onApiError((e) => seen.push(e.requestId));
    withId("not found", 404, "text/plain", "sub-id-00404");
    await failure(request("/api/x"));
    withId("decide", 409, "text/plain", "sub-id-00409");
    await failure(request("/api/x"));
    withId("boom", 502, "text/plain", "sub-id-00502");
    await failure(request("/api/x"));
    off();
    expect(seen).toEqual(["sub-id-00502"]);
  });

  it("does not announce the host being unreachable for a dataset shown from its offline copy (the banner says so), only for others", async () => {
    const seen: string[] = [];
    const off = onApiError((e) => seen.push(e.requestId));
    const mark = JSON.stringify({ copy: true, takenAt: "2026-11-12T00:00:00Z", signature: { hostId: 1, size: 1, mtimeNs: 1, identity: "" } });
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json", "X-Vesqira-Offline": mark } })));
    await request("/api/datasets/7/view");
    withId('{"error":"the host is not reachable (is the VPN up?)"}', 502, "application/json", "off-id-0007");
    await failure(request("/api/datasets/7/conversions"));
    withId('{"error":"the host is not reachable (is the VPN up?)"}', 502, "application/json", "off-id-0008");
    await failure(request("/api/datasets/8/conversions"));
    off();
    expect(seen).toEqual(["off-id-0008"]);
  });

  it("sends a given id as X-Request-Id, and newRequestId makes an acceptable one", async () => {
    const f = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", f);
    await postJSON("/api/x", {}, undefined, "abcdefgh12");
    expect(new Headers((f.mock.calls[0] as unknown as [string, RequestInit])[1].headers).get("X-Request-Id")).toBe("abcdefgh12");
    expect(newRequestId()).toMatch(/^[A-Za-z0-9._-]{8,64}$/);
  });
});
