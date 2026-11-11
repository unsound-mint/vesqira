import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { fromEditorText, hasLoneCR, lineEnding, looksEditable, toEditorText } from "../api/editor";
import EditorScreen from "./EditorScreen";

// Monaco does not run in jsdom; the screen's own behaviour is what is tested, through the surface's contract.
vi.mock("./CodeSurface", () => ({
  languageFor: () => "plaintext",
  CodeSurface: ({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) => <textarea aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} />,
}));

const PATH = "/data/run/pipeline.toml";
const file = (content: string, sha: string, mtime = "1") => ({ path: PATH, name: "pipeline.toml", content, size: content.length, mtime, sha256: sha });
const json = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

type Call = { url: string; method: string; body: { path: string; content: string; base: { sha256: string } } | null };
function server(handler: (c: Call) => Promise<Response>) {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", (url: string, init?: RequestInit) => {
    const c: Call = { url, method: init?.method ?? "GET", body: init?.body ? (JSON.parse(String(init.body)) as Call["body"]) : null };
    calls.push(c);
    return handler(c);
  });
  return calls;
}

describe("the editor screen", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("saves over the version it opened and takes the answer as the new base", async () => {
    const calls = server((c) => (c.method === "GET" ? json(200, file("ker = 8\n", "aaa")) : json(200, file(c.body!.content, "bbb", "2"))));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    const box = await screen.findByLabelText("Contents of pipeline.toml");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    await userEvent.type(box, "x");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("saved")).toBeTruthy());
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body?.base.sha256).toBe("aaa");
    expect(put.body?.content).toBe("ker = 8\nx");
  });

  it("shows both versions when the file changed on disk, and keeping mine needs a confirmation and sends the new base", async () => {
    let puts = 0;
    const calls = server((c) => {
      if (c.method === "GET") return json(200, file("mine = 0\n", "aaa"));
      puts += 1;
      if (puts === 1) return json(409, { error: "the file changed on disk since you opened it; nothing was written", current: file("theirs = 1\n", "ccc", "5") });
      return json(200, file(c.body!.content, "ddd", "6"));
    });
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    const box = await screen.findByLabelText("Contents of pipeline.toml");
    await userEvent.type(box, "edit");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("heading", { name: "Changed on disk" })).toBeTruthy();
    expect((screen.getByLabelText("Your version") as HTMLTextAreaElement).value).toBe("mine = 0\nedit");
    expect((screen.getByLabelText("On disk now") as HTMLTextAreaElement).value).toBe("theirs = 1\n");
    expect(puts).toBe(1);

    await userEvent.click(screen.getByRole("button", { name: "Keep mine…" }));
    expect(puts).toBe(1); // nothing is sent until the overwrite is confirmed
    await userEvent.click(screen.getByRole("button", { name: "Overwrite the file on disk" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Changed on disk" })).toBeNull());
    const second = calls.filter((c) => c.method === "PUT")[1]!;
    expect(second.body?.base.sha256).toBe("ccc"); // the version the conflict showed, not the stale one
    expect(second.body?.content).toBe("mine = 0\nedit");
  });

  it("takes theirs without writing anything", async () => {
    const calls = server((c) => (c.method === "GET" ? json(200, file("mine = 0\n", "aaa")) : json(409, { error: "changed", current: file("theirs = 1\n", "ccc") })));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    await userEvent.type(await screen.findByLabelText("Contents of pipeline.toml"), "z");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.click(await screen.findByRole("button", { name: "Take theirs" }));
    expect((await screen.findByLabelText("Contents of pipeline.toml") as HTMLTextAreaElement).value).toBe("theirs = 1\n");
    expect(screen.getByText("saved")).toBeTruthy();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1);
  });

  it("says why a file cannot be opened and offers no editor surface", async () => {
    server(() => json(415, { error: "that is a FITS file; the editor never opens or writes FITS" }));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    expect((await screen.findByRole("alert")).textContent).toContain("FITS");
    expect(screen.queryByLabelText("Contents of pipeline.toml")).toBeNull();
  });

  it("asks before discarding unsaved changes", async () => {
    server(() => json(200, file("a\n", "aaa")));
    const onClose = vi.fn();
    render(<EditorScreen path={PATH} onClose={onClose} />);
    await userEvent.type(await screen.findByLabelText("Contents of pipeline.toml"), "b");
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(onClose).toHaveBeenCalled();
  });
});

describe("bytes the editor surface cannot show", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("keeps a UTF-8 byte order mark: hidden from the editor, written back first and once", async () => {
    const calls = server((c) => (c.method === "GET" ? json(200, file("\uFEFFker = 8\n", "aaa")) : json(200, file(c.body!.content, "bbb", "2"))));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    const box = (await screen.findByLabelText("Contents of pipeline.toml")) as HTMLTextAreaElement;
    expect(box.value).toBe("ker = 8\n");
    await userEvent.type(box, "x");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("saved")).toBeTruthy());
    expect(calls.find((c) => c.method === "PUT")!.body?.content).toBe("\uFEFFker = 8\nx");
  });

  it("writes no byte order mark to a file that had none", async () => {
    const calls = server((c) => (c.method === "GET" ? json(200, file("a\n", "aaa")) : json(200, file(c.body!.content, "bbb", "2"))));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    await userEvent.type(await screen.findByLabelText("Contents of pipeline.toml"), "b");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("saved")).toBeTruthy());
    expect(calls.find((c) => c.method === "PUT")!.body?.content).toBe("a\nb");
  });

  it("does not open a file with a bare CR, which the editor would rewrite as LF", async () => {
    server(() => json(200, file("a\rb\r\nc\n", "aaa")));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    expect((await screen.findByRole("alert")).textContent).toContain("carriage-return");
    expect(screen.queryByLabelText("Contents of pipeline.toml")).toBeNull();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("still opens a CRLF file and puts CRLF back", async () => {
    const calls = server((c) => (c.method === "GET" ? json(200, file("a\r\nb\r\n", "aaa")) : json(200, file(c.body!.content, "bbb", "2"))));
    render(<EditorScreen path={PATH} onClose={() => {}} />);
    await userEvent.type(await screen.findByLabelText("Contents of pipeline.toml"), "c");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("saved")).toBeTruthy());
    expect(calls.find((c) => c.method === "PUT")!.body?.content).toBe("a\r\nb\r\nc");
  });
});

describe("editor helpers", () => {
  it("tells a bare CR from CRLF", () => {
    expect(hasLoneCR("a\rb")).toBe(true);
    expect(hasLoneCR("a\r\nb\r")).toBe(true);
    expect(hasLoneCR("a\r\nb\n")).toBe(false);
  });
  it("puts the file's own line endings back", () => {
    const crlf = "a\r\nb\r\n";
    expect(lineEnding(crlf)).toEqual({ eol: "\r\n", mixed: false });
    expect(fromEditorText(toEditorText(crlf), "\r\n")).toBe(crlf);
    expect(lineEnding("a\nb\n")).toEqual({ eol: "\n", mixed: false });
    expect(lineEnding("a\r\nb\nc\n")).toEqual({ eol: "\n", mixed: true });
  });
  it("offers only what the server would open", () => {
    expect(["a.toml", "job.slurm", "Makefile", "notes.MD"].every(looksEditable)).toBe(true);
    expect(["cube.fits", "a.png", ".bashrc"].some(looksEditable)).toBe(false);
  });
});
