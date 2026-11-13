import { render, screen, within } from "@testing-library/react";
import { ReviewPage } from "./ReviewPage";
import { RoundChangesList, PreflightRound } from "../boards/RoundHistory";
import { summary } from "../boards/rounds";

const prov = { source: { name: "cube.fits", sizeBytes: 10, modifiedAt: "", identity: "a reference to the file by name, size and modification time; its bytes were not hashed" }, view: { kind: "plane", channel: 3 }, statements: [] };
const item = (revision: string, title: string, extra: object = {}) => ({ revision, title, caption: "", note: "", previewSha: revision.padEnd(64, "0"), width: 8, height: 6, provenance: prov, ...extra });
const comment = (id: number, rev: string, body: string, extra: object = {}) => ({ id, parentId: 0, round: 1, figureRevision: rev, author: "Supervisor", fromOwner: false, body, pictureChanged: false, createdAt: "2026-11-01T00:00:00Z", ...extra });

const since = {
  label: "round one",
  at: "2026-11-01T00:00:00Z",
  items: [
    { state: "unchanged", changed: ["caption"], previous: { title: "A", caption: "old caption", note: "" } },
    { state: "redrawn", changed: [], previous: { title: "C", caption: "", note: "", previewSha: "c".repeat(64), width: 8, height: 6 } },
    { state: "new", changed: [] },
  ],
  removed: [{ title: "B" }],
  removedCount: 1,
  lines: [{ kind: "caption", title: "A" }, { kind: "redrawn", title: "C" }, { kind: "added", title: "D" }, { kind: "removed", title: "B" }],
  carried: [comment(1, "ra", "Is the stretch linear?"), comment(2, "ra", "Yes.", { parentId: 1, fromOwner: true, author: "Author" }), comment(3, "rc2", "The ridge looks shifted.", { pictureChanged: true })],
};
const review = (previous: object | undefined, s = since) => ({
  snapshot: {
    boardTitle: "Phase separation", label: "round two", createdAt: "2026-11-10T00:00:00Z", expiresAt: "2026-12-01T00:00:00Z",
    items: [item("ra", "A", { caption: "new caption" }), item("rc2", "C"), item("rd", "D")],
    round: { number: 2, since: s },
  },
  comments: [], outcome: { value: "" }, previous,
});

function serve(body: unknown) {
  const urls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    urls.push(url);
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return urls;
}
beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe("review rounds on the reviewer's page", () => {
  it("lists what changed since the previous round and marks each figure in words", async () => {
    serve(review({ available: true }));
    render(<ReviewPage token="tok" />);
    const head = await screen.findByRole("heading", { name: /Changed since/ });
    const box = head.closest("section")!;
    expect(within(box).getByText(/Round 2, after “round one” · 1 new, 1 redrawn, 1 unchanged, 1 removed\./)).toBeInTheDocument();
    for (const t of ["Caption changed: A", "Redrawn: C", "Added: D", "Removed: B"]) expect(within(box).getByText(t)).toBeInTheDocument();
    const marks = screen.getAllByText(/since round 1/).map((e) => e.textContent);
    expect(marks).toEqual(["=Unchanged picture since round 1", "↻Redrawn since round 1", "+New since round 1"]);
    expect(screen.getByText(/Earlier caption: “old caption”/)).toBeInTheDocument();
  });

  it("shows the earlier drawing of a redrawn figure while the earlier round is available", async () => {
    serve(review({ available: true }));
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: /Changed since/ });
    expect(screen.getByRole("img", { name: /C, as drawn in round 1/ })).toHaveAttribute("src", `/review-api/tok/preview/${"c".repeat(64)}`);
  });

  it("says the earlier picture is no longer available, and requests nothing for it", async () => {
    const urls = serve(review({ available: false }));
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: /Changed since/ });
    expect(screen.getByText("The earlier picture is no longer available.")).toBeInTheDocument();
    expect(screen.getByText(/The earlier round is no longer available/)).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /as drawn in round/ })).toBeNull();
    expect(urls.some((u) => u.includes("c".repeat(64)))).toBe(false);
  });

  it("shows earlier comments read-only on the same figure, labelled with their round", async () => {
    serve(review({ available: true }));
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: /Changed since/ });
    const onA = screen.getByRole("region", { name: "Comments from earlier rounds on A" });
    expect(within(onA).getAllByText("Round 1")).toHaveLength(2);
    expect(within(onA).getByText(/Is the stretch linear\?/)).toBeInTheDocument();
    expect(within(onA).getByText(/Yes\./)).toBeInTheDocument();
    expect(within(onA).queryByRole("button")).toBeNull();
    expect(within(onA).queryByRole("textbox")).toBeNull();
    const onC = screen.getByRole("region", { name: "Comments from earlier rounds on C" });
    expect(within(onC).getByText(/made on the earlier drawing/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Comments from earlier rounds on D" })).toBeNull();
  });

  it("shows nothing about rounds on a first round or a figure share", async () => {
    const first = review(undefined);
    serve({ ...first, snapshot: { ...first.snapshot, round: { number: 1 } } });
    render(<ReviewPage token="tok" />);
    await screen.findByRole("heading", { name: "Phase separation" });
    expect(screen.queryByRole("heading", { name: /Changed since/ })).toBeNull();
    expect(screen.queryByText(/since round/)).toBeNull();
  });
});

describe("round history for the author", () => {
  const sn = { id: 2, boardId: 1, label: "round two", createdAt: "", expiresAt: "", revoked: false, items: 3, outcome: "", comments: 0, round: 2,
    changes: { since: "2026-11-01T00:00:00Z", sinceLabel: "round one", lines: since.lines, carried: 3, removedCount: 1 } };

  it("lists each change under its round", () => {
    render(<RoundChangesList sn={sn as never} />);
    expect(screen.getByText(/Changed since round 1/)).toBeInTheDocument();
    expect(screen.getByText("Removed: B")).toBeInTheDocument();
    expect(screen.getByText(/3 earlier comments are shown beside the same figure/)).toBeInTheDocument();
  });

  it("shows nothing for round 1", () => {
    const { container } = render(<RoundChangesList sn={{ ...sn, round: 1, changes: undefined } as never} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("previews the round a publish would make", () => {
    render(<PreflightRound pre={{ frozen: { round: { number: 3, since } } } as never} />);
    expect(screen.getByText(/This will be round 3\. Since round 2/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Changes since the last round" })).toBeInTheDocument();
  });

  it("summarises only the parts that are not zero", () => {
    expect(summary([{ state: "new", changed: [] }], 0)).toBe("1 new");
    expect(summary([], 0)).toBe("nothing");
  });
});
