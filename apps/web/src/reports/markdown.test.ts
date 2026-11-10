import { describe, expect, it } from "vitest";
import { renderMarkdown, topHeading, type RenderEnv } from "./markdown";

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
