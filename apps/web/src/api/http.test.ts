import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, postJSON, request, requestRaw } from "./http";

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
