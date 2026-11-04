import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BoardsView } from "../boards/BoardsView";
import { captionLines } from "../viewer/figure";
import type { FigureFacts } from "../viewer/figure";
import { ReviewPage } from "./ReviewPage";
import type { Plane, View } from "../api/planes";

const prov = { source: { name: "cube.fits", sizeBytes: 10, modifiedAt: "", identity: "a reference to the file by name, size and modification time; its bytes were not hashed" }, view: { kind: "plane", channel: 3 }, statements: ["Header values are declared by the file and are not verified."] };
const review = { snapshot: { boardTitle: "Group meeting", label: "v1", createdAt: "2026-11-04T00:00:00Z", expiresAt: "2026-12-01T00:00:00Z", items: [{ revision: "r1", title: "Peak channel", caption: "H I", note: "Check the unit", previewSha: "a".repeat(64), width: 8, height: 6, provenance: prov }] }, comments: [] };

function mockFetch(routes: Record<string, (init?: RequestInit) => { status?: number; body: unknown }>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    const r = key ? routes[key]!(init) : { status: 404, body: { error: "not found" } };
    return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("review page", () => {
  it("shows a frozen board with its provenance and says what the reviewer does not get", async () => {
    mockFetch({ "/review-api/tok": () => ({ body: review }) });
    render(<ReviewPage token="tok" />);
    expect(await screen.findByRole("heading", { name: "Group meeting" })).toBeInTheDocument();
    expect(screen.getByText(/not the data, file paths or any credential/)).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /Peak channel/ })).toHaveAttribute("src", `/review-api/tok/preview/${"a".repeat(64)}`);
    expect(screen.getByText(/Check the unit/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\/home\/|datasetId/);
  });

  it("sends a comment tied to the figure revision", async () => {
    const calls = mockFetch({
      "/review-api/tok/comments": () => ({ status: 201, body: { id: 1, figureRevision: "r1", author: "Dr. X", body: "Why this stretch?", createdAt: "2026-11-04T00:00:00Z" } }),
      "/review-api/tok": () => ({ body: review }),
    });
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Group meeting" });
    await userEvent.type(screen.getByLabelText(/Your name/), "Dr. X");
    await userEvent.type(screen.getByLabelText("Comment"), "Why this stretch?");
    await userEvent.click(screen.getByRole("button", { name: "Send comment" }));
    await waitFor(() => expect(screen.getByText("Why this stretch?")).toBeInTheDocument());
    const post = calls.find((c) => c.init?.method === "POST")!;
    expect(JSON.parse(String(post.init!.body))).toEqual({ figureRevision: "r1", author: "Dr. X", body: "Why this stretch?" });
  });

  it("gives one message for an invalid, expired or revoked link", async () => {
    mockFetch({ "/review-api/bad": () => ({ status: 404, body: { error: "not found" } }) });
    render(<ReviewPage token="bad" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("not valid, has expired, or was revoked");
  });
});

describe("boards", () => {
  it("shows the review link once, built from the review address, and warns that it grants read access", async () => {
    const board = { id: 1, title: "Draft", createdAt: "", updatedAt: "", items: [{ position: 0, note: "", figure: { id: 5, title: "Peak", caption: "", createdAt: "", revision: "r".repeat(64), previewSha: "b".repeat(64), width: 8, height: 6, provenance: prov } }] };
    mockFetch({
      "/api/boards/1/publish": () => ({ status: 201, body: { snapshot: { id: 9 }, token: "SECRETTOKEN", reviewPath: "/#/review/SECRETTOKEN", note: "" } }),
      "/api/boards/1/snapshots": () => ({ body: { snapshots: [] } }),
      "/api/boards": () => ({ body: { boards: [board] } }),
    });
    render(<BoardsView />);
    await userEvent.click(await screen.findByRole("button", { name: "Freeze and create link" }));
    const link = (await screen.findByLabelText("Review link")) as HTMLInputElement;
    expect(link.value).toBe("http://127.0.0.1:8788/#/review/SECRETTOKEN");
    expect(screen.getByText(/never receive the data, file paths or any credential/)).toBeInTheDocument();
  });
});

describe("figure captions", () => {
  const facts: FigureFacts = { dataset: "cube.fits", kind: "plane", channelLine: "", displayLine: "", limits: [0, 1], limitsUnit: "K" };
  const view = { channels: 64, spectral: { resolved: true, ctype: "VRAD", name: "radio velocity", unit: "m/s" } } as unknown as View;
  const plane = { head: { channel: 40, spectral_value: 8000 } } as unknown as Plane;

  it("states the display, the no-value rule and that header values are not verified", () => {
    const t = captionLines(facts, view, plane, { colormap: "viridis", stretch: "linear", range: "percentile", axial: false, region: null }).join("\n");
    expect(t).toContain("Channel 41 of 64, VRAD 8 km/s");
    expect(t).toContain("1st to 99th percentile of finite pixels in K");
    expect(t).toMatch(/never drawn as zero/);
    expect(t).toMatch(/not verified/);
  });

  it("calls an axial figure axial, unit declared, convention assumed", () => {
    const t = captionLines({ ...facts, kind: "orientation" }, view, plane, { colormap: "", stretch: "", range: "", axial: true, region: null }).join("\n");
    expect(t).toMatch(/Axial orientation \(0° = 180°\); unit declared by the author/);
    expect(t).toMatch(/assumed/);
  });
});
