import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { Impact } from "../api/lifecycle";
import type { Run } from "../api/runs";
import { EntryActions } from "./EntryActions";
import { matchDataset, matchRun } from "./filter";
import { datasetVocabulary } from "./vocabulary";
import { withFacts } from "./testDatasets";

const impact = (over: Partial<Impact> = {}): Impact => ({
  kind: "dataset", id: 7, name: "a.fits", archived: false, blocked: false, blockers: [], digest: "d1",
  fileNote: "The file on disk is not deleted.", ownedBytes: null,
  groups: [
    { key: "figures", effect: "stays", text: "2 figures were made from this dataset. They stay.", count: 2, items: [{ id: 1, label: "Peak" }, { id: 2, label: "Edge" }] },
    { key: "runs", effect: "unlinked", text: "1 run lists this dataset as an output. The run stays; this link is removed.", count: 1, items: [{ id: 3, label: "lmc_ker8" }] },
  ],
  ...over,
});

type Call = { url: string; body: unknown };
function fakeApi(handlers: Record<string, (body: unknown) => { status?: number; json: unknown }>) {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url, body });
    const h = handlers[url];
    if (!h) return new Response(JSON.stringify({ error: `unexpected ${url}` }), { status: 500 });
    const r = h(body);
    return new Response(JSON.stringify(r.json), { status: r.status ?? 200 });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("archive and delete", () => {
  it("archives at once, says so, and undoes it", async () => {
    const calls = fakeApi({ "/api/datasets/7/archive": () => ({ json: { archived: true } }), "/api/datasets/7/restore": () => ({ json: { archived: false } }) });
    const onChanged = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(<EntryActions kind="datasets" id={7} name="a.fits" archived={false} onChanged={onChanged} />);
    await user.click(screen.getByRole("button", { name: "Archive" }));
    expect(onChanged).toHaveBeenCalledWith("archived");
    rerender(<EntryActions kind="datasets" id={7} name="a.fits" archived onChanged={onChanged} />);
    expect(await screen.findByRole("status")).toHaveTextContent("hidden from the lists");
    await user.click(screen.getByRole("button", { name: "Undo" }));
    expect(onChanged).toHaveBeenLastCalledWith("restored");
    expect(calls.map((c) => c.url)).toEqual(["/api/datasets/7/archive", "/api/datasets/7/restore"]);
  });

  it("shows what depends on the entry and that the file is not deleted, then sends the digest it showed", async () => {
    const calls = fakeApi({ "/api/datasets/7/impact": () => ({ json: impact() }), "/api/datasets/7/delete": () => ({ json: { deleted: true, ownedBytes: "none" } }) });
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(<EntryActions kind="datasets" id={7} name="a.fits" archived={false} onChanged={onChanged} />);
    await user.click(screen.getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("dialog", {}, { timeout: 8000 }); // the first test pays for loading the lazy dialog
    expect(await screen.findByText("The file on disk is not deleted.")).toBeInTheDocument();
    expect(dialog).toHaveTextContent("2 figures were made from this dataset");
    expect(dialog).toHaveTextContent("Stays"); // the effect is a word, not only a colour
    expect(dialog).toHaveTextContent("Unlinked");
    expect(dialog).toHaveTextContent("Peak, Edge");
    await user.click(screen.getByRole("button", { name: "Delete from catalog" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith("deleted"));
    expect(calls.at(-1)).toEqual({ url: "/api/datasets/7/delete", body: { impactDigest: "d1" } });
  });

  it("shows the new impact and does not delete when the dependents changed meanwhile", async () => {
    const changed = impact({ digest: "d2", groups: [{ key: "figures", effect: "stays", text: "3 figures were made from this dataset. They stay.", count: 3, items: [] }] });
    const calls = fakeApi({
      "/api/datasets/7/impact": () => ({ json: impact() }),
      "/api/datasets/7/delete": (b) => ((b as { impactDigest: string }).impactDigest === "d1" ? { status: 409, json: { error: "changed", impact: changed } } : { json: { deleted: true, ownedBytes: "none" } }),
    });
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(<EntryActions kind="datasets" id={7} name="a.fits" archived={false} onChanged={onChanged} />);
    await user.click(screen.getByRole("button", { name: "Delete…" }));
    await user.click(await screen.findByRole("button", { name: "Delete from catalog" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("changed since you looked");
    expect(screen.getByRole("dialog")).toHaveTextContent("3 figures were made");
    expect(onChanged).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Delete from catalog" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith("deleted"));
    expect(calls.at(-1)?.body).toEqual({ impactDigest: "d2" });
  });

  it("cannot be confirmed while something blocks it, and offers archiving", async () => {
    fakeApi({ "/api/boards/4/impact": () => ({ json: impact({ kind: "board", id: 4, name: "Pinned", blocked: true, blockers: ["1 review was published from this board."], fileNote: "", groups: [{ key: "snapshots", effect: "blocks", text: "1 review was published from this board. A published review is pinned evidence: archive the board instead.", count: 1, items: [] }] }) }) });
    const user = userEvent.setup();
    render(<EntryActions kind="boards" id={4} name="Pinned" archived={false} onChanged={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Delete…" }));
    expect(await screen.findByText(/This cannot be deleted yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete from catalog" })).toBeDisabled();
  });

  it("asks separately about the bytes of a file Vesqira wrote, and sends the choice", async () => {
    const calls = fakeApi({
      "/api/datasets/9/impact": () => ({ json: impact({ id: 9, name: "copy.fits", groups: [], fileNote: "The original this was made from is not touched.", ownedBytes: { kind: "managed copy", name: "copy.fits", sizeBytes: 2048 } }) }),
      "/api/datasets/9/delete": () => ({ json: { deleted: true, ownedBytes: "removed" } }),
    });
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(<EntryActions kind="datasets" id={9} name="copy.fits" archived={false} onChanged={onChanged} />);
    await user.click(screen.getByRole("button", { name: "Delete…" }));
    const del = await screen.findByRole("button", { name: "Delete from catalog" });
    expect(screen.getByText(/managed copy that Vesqira wrote/)).toBeInTheDocument();
    expect(del).toBeDisabled(); // no choice yet: bytes are never removed by default, and never by accident
    await user.click(screen.getByLabelText(/Also remove the file/));
    await user.click(del);
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith("deleted"));
    expect(calls.at(-1)?.body).toEqual({ impactDigest: "d1", ownedBytes: "remove" });
  });
});

describe("archived in the filter", () => {
  const d = (over: Partial<Dataset>) => withFacts({ id: 1, name: "a.fits", nature: "", hostId: 0, header: { bunit: "K", bunit_status: "declared" }, unitDeclaration: null, interpretation: null, ...over }) as unknown as Dataset;
  it("hides archived datasets unless asked, and archived:yes shows only them", () => {
    expect(matchDataset(d({}), "", undefined)).toBe(true);
    expect(matchDataset(d({ archived: true }), "", undefined)).toBe(false);
    expect(matchDataset(d({ archived: true }), "archived:yes", undefined)).toBe(true);
    expect(matchDataset(d({}), "archived:yes", undefined)).toBe(false);
    expect(matchDataset(d({ archived: true }), "archived:all a.fits", undefined)).toBe(true);
    expect(matchDataset(d({ archived: true }), "archived:yes nope", undefined)).toBe(false); // the other words still narrow
  });
  it("does the same for runs", () => {
    const r = (archived: boolean) => ({ id: 1, name: "r", kind: "vgt", gitCommit: "", inputName: "", params: [], metrics: [], review: { qc: "passed" }, archived }) as unknown as Run;
    expect(matchRun(r(true), "")).toBe(false);
    expect(matchRun(r(true), "archived:yes qc:passed")).toBe(true);
    expect(matchRun(r(false), "archived:yes")).toBe(false);
  });
  it("offers archived:yes only when something is archived", () => {
    expect(datasetVocabulary([d({})], []).keys.some((k) => k.key === "archived")).toBe(false);
    expect(datasetVocabulary([d({}), d({ id: 2, archived: true })], []).keys.some((k) => k.key === "archived")).toBe(true);
  });
});
