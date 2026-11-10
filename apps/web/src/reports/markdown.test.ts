import { describe, expect, it } from "vitest";
import { plainText, renderMarkdown, topHeading, type RenderEnv } from "./markdown";

const env: RenderEnv = {
  resolveWiki: (inner, label) =>
    inner.startsWith("report:") ? (inner === "report:aaaa" ? { kind: "link", label: label ?? "A", onOpen: "aaaa" } : { kind: "unshared", label: label ?? "?" }) : inner.startsWith("run:") ? { kind: "mention", label: "run-7", mono: true } : inner === "Known" ? { kind: "link", label: label ?? inner, onOpen: "1" } : { kind: "missing", label: label ?? inner },
  resolveImage: (src) => (src.startsWith("asset:") ? `/read-api/t/assets/${src.slice(6)}` : null),
};

describe("report markdown", () => {
  it("never renders raw HTML or script links", () => {
    const out = renderMarkdown('<img src=x onerror=alert(1)> <script>alert(1)</script> [x](javascript:alert(1)) [y](data:text/html,hi)', env);
    expect(out).not.toMatch(/<img|<script|href="javascript|href="data/);
  });
  it("renders math with KaTeX and keeps dollar amounts as text", () => {
    const out = renderMarkdown("Mean $\\langle \\cos 2\\theta \\rangle$ costs $ 5 and $5.\n\n$$\nAM = 1\n$$", env);
    expect(out).toContain("katex");
    expect(out).toContain("math-display");
    expect(out).toContain("costs $ 5");
  });
  it("refuses KaTeX commands that would need trust", () => {
    const out = renderMarkdown("$\\href{javascript:alert(1)}{x}$", env);
    expect(out).not.toContain('href="javascript');
  });
  it("resolves report links, unshared and missing ones, and inline run mentions", () => {
    const out = renderMarkdown("See [[report:aaaa|the sweep]], [[report:bbbb|notes]], [[Nope]], [[Known]] and [[run:7]].", env);
    expect(out).toContain('data-open="aaaa"');
    expect(out).toContain('class="wiki-unshared"');
    expect(out).toContain('class="wiki-missing"');
    expect(out).toContain("<code>run-7</code>");
  });
  it("keeps the report title the only h1 and draws the report's top heading level as h2", () => {
    expect(renderMarkdown("# Section\n\n## Sub", env)).toMatch(/<h2>Section<\/h2>\s*<h3>Sub<\/h3>/);
    const top = topHeading(["## Question", "text", "### Detail", "```\n# not a heading\n```"]);
    expect(top).toBe(2);
    expect(renderMarkdown("## Question\n\n### Detail", { ...env, topHeading: top })).toMatch(/<h2>Question<\/h2>\s*<h3>Detail<\/h3>/);
  });
  it("shows only resolved images, and opens external links safely", () => {
    const out = renderMarkdown("![plot](asset:" + "a".repeat(64) + ") ![ext](https://example.org/x.png) [paper](https://arxiv.org/abs/1)", env);
    expect(out).toContain('src="/read-api/t/assets/');
    expect(out).not.toContain("example.org/x.png");
    expect(out).toContain('rel="noopener noreferrer"');
  });
});

describe("plainText", () => {
  it("shows a link's label, or its target without the kind prefix, and drops markdown marks", () => {
    expect(plainText("See [[report:aaaa|the sweep]] and ![[figure:f1]] and [[Known]], **bold** `code` $x$.")).toBe("See the sweep and f1 and Known, bold code x.");
  });
  it("leaves what the renderer would not turn into a link alone", () => {
    expect(plainText("[[a [b]]")).toBe("[[a [b]]");
    expect(plainText("[[a\nb]]")).toBe("[[a b]]");
  });
});

// A block can be 64 KiB and the reader renders what it is given, so no input may cost more than a moment. The limits in
// markdown.ts are what these pin: the first used to be cubic (hours at this size) and the second quadratic (seconds).
describe("adversarial blocks", () => {
  const fast = (f: () => unknown, ms = 1500) => {
    const t = performance.now();
    f();
    expect(performance.now() - t).toBeLessThan(ms);
  };
  it("plainText on 64 KiB of opening brackets", () => {
    fast(() => plainText("[[".repeat(32768)));
    fast(() => plainText("[[a ".repeat(16384) + "]]"));
    fast(() => plainText("[[a|".repeat(16384)));
  });
  it("rendering 64 KiB of unclosed or nested openers", () => {
    fast(() => renderMarkdown("$$ a\n".repeat(13000), env));
    fast(() => renderMarkdown("[[".repeat(32768), env));
    fast(() => renderMarkdown("[[a ".repeat(16384) + "]]", env));
    fast(() => renderMarkdown("![[".repeat(21000), env));
    fast(() => renderMarkdown("$x ".repeat(21000), env));
  });
  it("a math block is limited to a few hundred lines; a normal one still renders", () => {
    const lines = (n: number) => "$$\n" + "x = 1\n".repeat(n) + "$$";
    expect(renderMarkdown(lines(10), env)).toContain("math-display");
    expect(renderMarkdown(lines(300), env)).not.toContain("math-display");
  });
  it("a link target longer than any title is not a link, but a full-length title is", () => {
    expect(renderMarkdown(`[[${"a".repeat(500)}]]`, env)).not.toContain("wiki-missing");
    expect(renderMarkdown(`[[${"a".repeat(200)}]]`, env)).toContain("wiki-missing");
  });
  it("a published link to a report keeps working when its shown text is a full-length title", () => {
    // freeze() writes [[report:<16 hex>|shown]]; the shown text is at most a title (200 characters, 400 UTF-16 units)
    const key = "aaaa";
    expect(renderMarkdown(`[[report:${key}|${"t".repeat(200)}]]`, env)).toContain('data-open="aaaa"');
    expect(renderMarkdown(`[[report:${key}|${"😀".repeat(200)}]]`, env)).toContain('data-open="aaaa"');
    expect(renderMarkdown(`[[Known|${"x".repeat(500)}]]`, env)).not.toContain('data-open');
    expect(plainText(`[[report:${key}|${"t".repeat(200)}]]`)).toBe("t".repeat(200));
  });
  it("brackets that only look like a wiki link end inside the run they are in and keep the text around them", () => {
    for (const text of ["[see [[Known]]](http://x.example) after", "[[[a]](http://x) and [[Known]]", "[x [[a]](http://y)", "[[a]](http://z)]"]) {
      const out = renderMarkdown(text, env);
      expect(out).toContain("http://");
      expect(out.startsWith("<p>")).toBe(true);
    }
  });
});
