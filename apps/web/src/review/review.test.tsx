import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BoardsView } from "../boards/BoardsView";
import { captionLines } from "../viewer/figure";
import type { FigureFacts } from "../viewer/figure";
import { ReviewPage } from "./ReviewPage";
import type { Plane, View } from "../api/planes";

const prov = { source: { name: "cube.fits", sizeBytes: 10, modifiedAt: "", identity: "a reference to the file by name, size and modification time; its bytes were not hashed" }, view: { kind: "plane", channel: 3 }, statements: ["Header values are declared by the file and are not verified."] };
const review = { snapshot: { boardTitle: "Group meeting", label: "v1", createdAt: "2026-11-04T00:00:00Z", expiresAt: "2026-12-01T00:00:00Z", items: [{ revision: "r1", title: "Peak channel", caption: "H I", note: "Check the unit", previewSha: "a".repeat(64), width: 8, height: 6, provenance: prov }] }, comments: [], outcome: { value: "" } };

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

beforeEach(() => localStorage.clear());
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
      "/review-api/tok/comments": () => ({ status: 201, body: { id: 1, figureRevision: "r1", parentId: 0, author: "Dr. X", fromOwner: false, body: "Why this stretch?", createdAt: "2026-11-04T00:00:00Z" } }),
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

  it("shows the author's saved layout read-only: panels in reading order, placed by their columns, with no way to move them", async () => {
    const item = (rev: string, title: string, layout?: object) => ({ ...review.snapshot.items[0]!, revision: rev, title, ...(layout ? { layout } : {}) });
    const arranged = { ...review, snapshot: { ...review.snapshot, items: [item("r1", "Right", { x: 6, y: 0, w: 6, h: 4 }), item("r2", "Left", { x: 0, y: 0, w: 6, h: 4 }), item("r3", "Below", { x: 0, y: 4, w: 12, h: 3 })] } };
    mockFetch({ "/review-api/tok": () => ({ body: arranged }) });
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Group meeting" });
    const articles = screen.getAllByRole("article");
    expect(articles.map((a) => a.querySelector("h2")?.textContent)).toEqual(["Left", "Right", "Below"]);
    expect(articles[1]!.style.getPropertyValue("--col")).toBe("7 / span 6");
    expect(articles[2]!.style.getPropertyValue("--rows")).toBe("5 / span 3");
    expect(screen.queryByRole("button", { name: /Move or resize/ })).toBeNull();
  });

  it("lays out a snapshot made before layouts from its order, one panel per row", async () => {
    mockFetch({ "/review-api/tok": () => ({ body: review }) });
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Group meeting" });
    expect(screen.getByRole("article").style.getPropertyValue("--col")).toBe("1 / span 12");
  });

  it("states the access label the author gave the data, as words, and never names a dataset", async () => {
    const labelled = { ...review, snapshot: { ...review.snapshot, access: "unreleased", items: [{ ...review.snapshot.items[0]!, access: "unreleased" }] } };
    mockFetch({ "/review-api/tok": () => ({ body: labelled }) });
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Group meeting" });
    expect(screen.getByRole("note")).toHaveTextContent("Unreleased");
    expect(screen.getByRole("note")).toHaveTextContent(/marked the data shown here as unreleased/);
    expect(screen.getAllByText("Unreleased").length).toBeGreaterThan(1); // the board's and the figure's
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

describe("publishing a board that uses labelled data", () => {
  const board = { id: 1, title: "Draft", createdAt: "", updatedAt: "", items: [{ position: 0, note: "", figure: { id: 5, title: "Peak", caption: "", createdAt: "", revision: "r".repeat(64), previewSha: "b".repeat(64), width: 8, height: 6, provenance: prov, access: { label: "unreleased", via: [{ datasetId: 3, name: "lmc.fits", label: "unreleased" }] } } }] };
  const refusal = (status: number, code: string) => ({ status, body: { error: "This board uses data labelled unreleased: Peak (lmc.fits). Confirm to publish.", code, label: "unreleased", restricted: [] } });

  it("shows the label on the figure, asks for confirmation naming the data, and sends confirmAccess only after the author says yes", async () => {
    let published = 0;
    const calls = mockFetch({
      "/api/boards/1/publish": (init) => {
        const body = JSON.parse(String(init?.body));
        if (!body.confirmAccess) return refusal(409, "access_confirmation_required");
        published++;
        return { status: 201, body: { snapshot: { id: 9 }, token: "TOK", reviewPath: "/#/review/TOK", note: "" } };
      },
      "/api/boards/1/snapshots": () => ({ body: { snapshots: [] } }),
      "/api/boards": () => ({ body: { boards: [board] } }),
    });
    render(<BoardsView />);
    expect(await screen.findByText(/Made from lmc.fits/)).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Freeze and create link" }));
    expect(await screen.findByRole("alert", { name: "Labelled data" })).toHaveTextContent("lmc.fits");
    expect(published).toBe(0);
    await userEvent.click(screen.getByRole("button", { name: /I confirm: publish/ }));
    expect(await screen.findByLabelText("Review link")).toBeInTheDocument();
    const sent = calls.filter((c) => c.url.endsWith("/publish")).map((c) => JSON.parse(String(c.init!.body)).confirmAccess);
    expect(sent).toEqual([undefined, true]);
  });

  it("offers no way past a block set by the workspace's admin", async () => {
    mockFetch({
      "/api/boards/1/publish": () => refusal(403, "publishing_blocked"),
      "/api/boards/1/snapshots": () => ({ body: { snapshots: [] } }),
      "/api/boards": () => ({ body: { boards: [board] } }),
    });
    render(<BoardsView />);
    await userEvent.click(await screen.findByRole("button", { name: "Freeze and create link" }));
    expect(await screen.findByRole("alert", { name: "Labelled data" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /I confirm/ })).toBeNull();
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

  it("states the axial convention that was declared, and by whom, never an assumed one", () => {
    const interp = { kind: "axial_orientation", unit: "rad", reliabilityId: null, frame: "pixel", quantity: null, declaredBy: "user", declaredAt: "2026-11-13T00:00:00Z", stale: false, tier: "declared" } as const;
    const axial = (i: unknown) => captionLines({ ...facts, kind: "orientation" }, view, plane, { colormap: "", stretch: "", range: "", axial: true, region: null, interp: i as never }).join("\n");
    const user = axial(interp);
    expect(user).toMatch(/Axial orientation \(0° = 180°\) in radians, as declared by the author/);
    expect(user).toMatch(/\+x axis towards \+y/);
    expect(user).not.toMatch(/assum|VGT convention/);
    expect(axial({ ...interp, frame: "sky", unit: "deg", declaredBy: "profile:VGT.jl@1" })).toMatch(/in degrees, as the file’s header declares it.*east of north/s);
    expect(axial({ ...interp, frame: null })).toMatch(/not declared, so directions on this figure cannot be read/);
    expect(axial({ ...interp, declaredBy: "vgt-run-adapter" })).toMatch(/proposed by the run import, not by the file/);
    expect(axial(null)).toMatch(/cannot be read/);
  });
});

describe("threads on a review", () => {
  const root = { id: 7, figureRevision: "r1", parentId: 0, author: "Prof", fromOwner: false, body: "What is this?", anchor: { kind: "point", x: 0.25, y: 0.5 }, createdAt: "2026-11-04T00:00:00Z" };

  it("shows where a comment points, the author's reply and whether the thread is closed", async () => {
    const withThread = { ...review, comments: [root, { id: 8, figureRevision: "r1", parentId: 7, author: "Author", fromOwner: true, body: "A sidelobe.", createdAt: "2026-11-04T01:00:00Z" }, { ...root, id: 9, body: "Settled?", anchor: undefined, resolvedAt: "2026-11-04T02:00:00Z" }] };
    mockFetch({ "/review-api/tok": () => ({ body: withThread }) });
    render(<ReviewPage token="tok" />);
    expect(await screen.findByText("What is this?")).toBeInTheDocument();
    expect(screen.getAllByLabelText("Marked place 1").length).toBeGreaterThan(0);
    expect(screen.getByText("A sidelobe.")).toBeInTheDocument();
    expect(screen.getByText("Resolved by the author")).toBeInTheDocument();
    // a reviewer cannot close a thread
    expect(screen.queryByRole("button", { name: /resolved|Reopen/ })).toBeNull();
  });

  it("sends a reply to the thread and a comment with a marked place", async () => {
    const calls = mockFetch({
      "/review-api/tok/comments": (init) => {
        const b = JSON.parse(String(init!.body));
        return { status: 201, body: { id: 20, figureRevision: "r1", parentId: b.parentId ?? 0, author: b.author || "Reviewer", fromOwner: false, body: b.body, anchor: b.anchor, createdAt: "2026-11-04T03:00:00Z" } };
      },
      "/review-api/tok": () => ({ body: { ...review, comments: [root] } }),
    });
    render(<ReviewPage token="tok" />);
    await screen.findByText("What is this?");
    await userEvent.type(screen.getByLabelText("Reply to Prof"), "Thanks");
    await userEvent.click(screen.getByRole("button", { name: "Reply" }));
    await waitFor(() => expect(screen.getByText("Thanks")).toBeInTheDocument());
    expect(JSON.parse(String(calls.find((c) => c.init?.method === "POST")!.init!.body))).toEqual({ parentId: 7, author: "", body: "Thanks" });

    await userEvent.click(screen.getByRole("button", { name: "Mark a place on the picture" }));
    const layer = screen.getByRole("application");
    layer.focus();
    await userEvent.keyboard("{Enter}{ArrowRight}");
    await userEvent.type(screen.getByLabelText("Comment"), "Look here");
    await userEvent.click(screen.getByRole("button", { name: "Send comment" }));
    await waitFor(() => expect(screen.getByText("Look here")).toBeInTheDocument());
    const sent = JSON.parse(String(calls.filter((c) => c.init?.method === "POST").at(-1)!.init!.body));
    expect(sent.anchor).toEqual({ kind: "point", x: 0.52, y: 0.5 });
  });
});

describe("review request", () => {
  it("shows the author's question and says when a version has been replaced", async () => {
    mockFetch({ "/review-api/tok": () => ({ body: { ...review, snapshot: { ...review.snapshot, question: "Is the edge real?" }, superseded: { reason: "Colour bar rescaled.", at: "2026-11-05T00:00:00Z" } } }) });
    render(<ReviewPage token="tok" />);
    expect(await screen.findByText("Is the edge real?")).toBeInTheDocument();
    expect(screen.getByText(/The author has replaced this version/)).toBeInTheDocument();
    expect(screen.getByText(/Colour bar rescaled/)).toBeInTheDocument();
  });

  it("sends an outcome that is about the review, not about any run", async () => {
    const calls = mockFetch({
      "/review-api/tok/outcome": () => ({ status: 201, body: { value: "changes_requested", note: "Rescale", by: "Prof", at: "2026-11-05T00:00:00Z" } }),
      "/review-api/tok": () => ({ body: review }),
    });
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Your outcome for this version" });
    expect(screen.getByText(/not an assessment of any run/)).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Changes requested"));
    await userEvent.type(screen.getByLabelText("Note (optional)"), "Rescale");
    await userEvent.click(screen.getByRole("button", { name: "Send outcome" }));
    await waitFor(() => expect(screen.getByText(/Currently: Changes requested/)).toBeInTheDocument());
    expect(JSON.parse(String(calls.find((c) => c.init?.method === "POST")!.init!.body))).toEqual({ outcome: "changes_requested", note: "Rescale", author: "" });
  });
});

describe("what is new since the last visit", () => {
  const reply = { id: 8, figureRevision: "r1", parentId: 7, author: "Author", fromOwner: true, body: "A sidelobe.", createdAt: "2026-11-04T12:00:00Z" };
  const root = { id: 7, figureRevision: "r1", parentId: 0, author: "Prof", fromOwner: false, body: "What is this?", createdAt: "2026-11-04T10:00:00Z" };

  it("remembers the reviewer's name and flags the author's replies made after their last visit", async () => {
    localStorage.setItem("vesqira.seen.review.tok", String(new Date("2026-11-04T11:00:00Z").getTime()));
    localStorage.setItem("vesqira.reviewer.name", "Prof");
    mockFetch({ "/review-api/tok": () => ({ body: { ...review, comments: [root, reply] } }) });
    render(<ReviewPage token="tok" />);
    expect(await screen.findByText(/Since your last visit/)).toBeInTheDocument();
    expect(screen.getAllByText("New")).toHaveLength(1); // the reply, not the reviewer's own earlier comment
    expect(screen.getByLabelText(/Your name/)).toHaveValue("Prof");
  });

  it("shows nothing as new on a first visit", async () => {
    mockFetch({ "/review-api/tok": () => ({ body: { ...review, comments: [root, reply] } }) });
    render(<ReviewPage token="tok" />);
    await screen.findByText("A sidelobe.");
    expect(screen.queryByText(/Since your last visit/)).toBeNull();
    expect(screen.queryByText("New")).toBeNull();
  });
});

describe("review page error reference", () => {
  it("shows the server's reference with a copy button when a comment fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === "POST") return new Response("temporarily unavailable", { status: 500, headers: { "Content-Type": "text/plain", "X-Request-Id": "rev-ref-12345" } });
      return new Response(JSON.stringify(review), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Group meeting" });
    await userEvent.type(screen.getByLabelText("Comment"), "Why?");
    await userEvent.click(screen.getByRole("button", { name: "Send comment" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("temporarily unavailable");
    expect(alert).toHaveTextContent("Reference: rev-ref-12345");
    expect(screen.getByRole("button", { name: "Copy reference rev-ref-12345" })).toBeInTheDocument();
  });
});
