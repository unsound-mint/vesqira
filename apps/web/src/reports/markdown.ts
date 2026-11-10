import MarkdownIt from "markdown-it";
import type StateInline from "markdown-it/lib/rules_inline/state_inline.mjs";
import type StateBlock from "markdown-it/lib/rules_block/state_block.mjs";
import katex from "katex";

/**
 * Report Markdown (ADR 0036): CommonMark with GitHub tables and $…$ / $$…$$ math. Raw HTML is never rendered, KaTeX
 * runs with trust off, images come only from what the caller resolves (attachments on the owner's side, published
 * assets on the reader's), and links only go to http, https or mailto. [[…]] references are resolved by the caller,
 * because the owner's app and the reader's page know different things about them.
 */
export type WikiTarget =
  | { kind: "link"; label: string; onOpen: string } // onOpen is passed back to the page through data-open
  | { kind: "unshared"; label: string } // the reader's space does not hold it
  | { kind: "missing"; label: string } // names nothing
  | { kind: "mention"; label: string; mono?: boolean }; // an inline run or figure mention

export type RenderEnv = {
  resolveWiki: (inner: string, label: string | null) => WikiTarget;
  resolveImage: (src: string) => string | null;
  /** The highest heading level used anywhere in the report (1 for #); it is drawn as h2 under the title. */
  topHeading?: number;
};

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function tex(src: string, display: boolean): string {
  try {
    return katex.renderToString(src, { displayMode: display, throwOnError: false, trust: false, strict: "ignore", maxSize: 20, maxExpand: 200, output: "htmlAndMathml" });
  } catch {
    return `<code>${escapeHtml(src)}</code>`;
  }
}

function mathInline(state: StateInline, silent: boolean): boolean {
  const src = state.src;
  const start = state.pos;
  if (src[start] !== "$" || src[start + 1] === "$") return false;
  // an opening $ followed by a space or a digit-dollar pattern (prices) is not math
  if (src[start + 1] === " " || src[start + 1] === undefined) return false;
  let end = start + 1;
  while (end < state.posMax) {
    if (src[end] === "\\") {
      end += 2;
      continue;
    }
    if (src[end] === "$") break;
    end++;
  }
  if (end >= state.posMax || src[end - 1] === " ") return false;
  if (!silent) {
    const t = state.push("math_inline", "math", 0);
    t.content = src.slice(start + 1, end);
  }
  state.pos = end + 1;
  return true;
}

function mathBlock(state: StateBlock, startLine: number, endLine: number, silent: boolean): boolean {
  const first = state.src.slice(state.bMarks[startLine]! + state.tShift[startLine]!, state.eMarks[startLine]);
  if (!first.startsWith("$$")) return false;
  let content = first.slice(2);
  let line = startLine;
  let closed = content.trimEnd().endsWith("$$");
  if (closed) content = content.trimEnd().slice(0, -2);
  while (!closed && ++line < endLine) {
    const l = state.src.slice(state.bMarks[line]! + state.tShift[line]!, state.eMarks[line]);
    if (l.trimEnd().endsWith("$$")) {
      content += "\n" + l.trimEnd().slice(0, -2);
      closed = true;
    } else content += "\n" + l;
  }
  if (!closed) return false;
  if (silent) return true;
  const t = state.push("math_block", "math", 0);
  t.content = content.trim();
  t.map = [startLine, line + 1];
  state.line = line + 1;
  return true;
}

function wiki(state: StateInline, silent: boolean): boolean {
  const src = state.src;
  if (!src.startsWith("[[", state.pos)) return false;
  const end = src.indexOf("]]", state.pos + 2);
  if (end < 0) return false;
  const inner = src.slice(state.pos + 2, end);
  if (inner.includes("[") || inner.includes("\n") || inner.trim() === "") return false;
  if (!silent) {
    const bar = inner.indexOf("|");
    const t = state.push("wiki", "", 0);
    t.content = bar >= 0 ? inner.slice(0, bar).trim() : inner.trim();
    t.meta = { label: bar >= 0 ? inner.slice(bar + 1).trim() || null : null };
  }
  state.pos = end + 2;
  return true;
}

/** An inline figure mention, ![[figure:…]] inside a paragraph, is handled before images see it. */
function wikiEmbed(state: StateInline, silent: boolean): boolean {
  if (state.src[state.pos] !== "!" || !state.src.startsWith("[[", state.pos + 1)) return false;
  state.pos++;
  if (wiki(state, silent)) return true;
  state.pos--;
  return false;
}

const md = new MarkdownIt({ html: false, linkify: false, typographer: false, breaks: false });
md.inline.ruler.before("escape", "math_inline", mathInline);
md.block.ruler.before("fence", "math_block", mathBlock, { alt: ["paragraph", "reference", "blockquote", "list"] });
md.inline.ruler.before("image", "wiki_embed", wikiEmbed);
md.inline.ruler.before("link", "wiki", wiki);
md.validateLink = (url: string) => /^(https?:|mailto:|#|attachment:|asset:)/i.test(url.trim());

md.renderer.rules.math_inline = (tokens, i) => tex(tokens[i]!.content, false);
md.renderer.rules.math_block = (tokens, i) => `<div class="math-display">${tex(tokens[i]!.content, true)}</div>\n`;
md.renderer.rules.wiki = (tokens, i, _o, env: RenderEnv) => {
  const t = tokens[i]!;
  const r = env.resolveWiki(t.content, (t.meta as { label: string | null }).label);
  const label = escapeHtml(r.label);
  switch (r.kind) {
    case "link":
      return `<a href="#" class="wiki" data-open="${escapeHtml(r.onOpen)}">${label}</a>`;
    case "unshared":
      return `<span class="wiki-unshared" title="Not shared with you">${label}</span>`;
    case "missing":
      return `<span class="wiki-missing" title="No report has this title">${label}</span>`;
    default:
      return r.mono ? `<code>${label}</code>` : `<em>${label}</em>`;
  }
};
md.renderer.rules.image = (tokens, i, _o, env: RenderEnv) => {
  const t = tokens[i]!;
  const alt = escapeHtml(t.content);
  const src = env.resolveImage(t.attrGet("src") ?? "");
  if (!src) return `<span class="img-missing">${alt || "image"}</span>`;
  return `<img src="${escapeHtml(src)}" alt="${alt}" loading="lazy">`;
};
// The report title is the page's only h1. The report's highest heading level is drawn as h2 and the rest keep their
// distance from it, so a report written with ## sections and one written with # sections look the same.
const shift = (tag: string, env: RenderEnv) => `h${Math.max(2, Math.min(6, Number(tag.slice(1)) + 2 - (env.topHeading ?? 1)))}`;
md.renderer.rules.heading_open = (tokens, i, o, env: RenderEnv, self) => {
  tokens[i]!.tag = shift(tokens[i]!.tag, env);
  return self.renderToken(tokens, i, o);
};
md.renderer.rules.heading_close = (tokens, i, o, env: RenderEnv, self) => {
  tokens[i]!.tag = shift(tokens[i]!.tag, env);
  return self.renderToken(tokens, i, o);
};
const defaultLinkOpen = md.renderer.rules.link_open ?? ((tokens, i, o, _e, self) => self.renderToken(tokens, i, o));
md.renderer.rules.link_open = (tokens, i, o, env, self) => {
  const href = tokens[i]!.attrGet("href") ?? "";
  if (/^(attachment|asset):/i.test(href)) tokens[i]!.attrSet("href", "#");
  if (/^https?:/i.test(href)) {
    tokens[i]!.attrSet("target", "_blank");
    tokens[i]!.attrSet("rel", "noopener noreferrer");
  }
  return defaultLinkOpen(tokens, i, o, env, self);
};

/** The highest heading level (1–6) among some Markdown texts, outside fenced code; 1 when there is none. */
export function topHeading(texts: string[]): number {
  let top = 7;
  for (const t of texts) {
    if (/^\s{0,3}(```|~~~)/.test(t)) continue;
    for (const m of t.matchAll(/^ {0,3}(#{1,6})(?: |$)/gm)) top = Math.min(top, m[1]!.length);
  }
  return top === 7 ? 1 : top;
}

export function renderMarkdown(text: string, env: RenderEnv): string {
  return md.render(text, env);
}

/** Plain text of a block for summaries (comments list, change lists). */
export function plainText(text: string): string {
  return text.replace(/!?\[\[([^\]|]*)\|?([^\]]*)\]\]/g, (_m, a: string, b: string) => b || a.replace(/^(report|run|figure):/, "")).replace(/[#*_`>$]/g, "").replace(/\s+/g, " ").trim();
}
