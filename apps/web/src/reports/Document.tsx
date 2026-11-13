import { useEffect, useMemo, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { MessageSquare, MessageSquarePlus, TextQuote } from "lucide-react";
import "katex/dist/katex.min.css";
import type { Provenance } from "../api/boards";
import { channelText } from "../boards/channelText";
import type { Metric } from "../api/metrics";
import { metricText } from "../api/metrics";
import { Icon } from "../ui/Icon";
import { plainText, renderMarkdown, topHeading, type RenderEnv } from "./markdown";
import s from "./Document.module.css";

/** The reviewer-safe summary of a run, as a figure's provenance and a published report carry it. */
export type RunSummary = { name: string; kind: string; gitCommit?: string; dirty?: boolean; generatedAt?: string; params?: { key: string; value: string }[]; metrics?: Metric[] | null; review?: { qc: string; interpretation: string; note?: string } };

export type DocFigure = { src: string; width: number; height: number; title: string; caption: string; provenance?: Provenance | null };

export type DocBlock = { id: string; kind: "text" | "code"; text: string } | { id: string; kind: "figure"; figure: DocFigure | null } | { id: string; kind: "run"; run: RunSummary | null };

const QC: Record<string, string> = { not_assessed: "not assessed", passed: "QC passed", failed: "QC failed", inconclusive: "QC inconclusive" };

/**
 * A report as it reads (reports mock B): one title, a quiet meta line, the text at a reading measure, figures with a
 * short provenance line, runs as one line each. The same component draws the owner's page and the reader's, so what
 * the owner sees is what the supervisor gets. Comments sit in a narrow gutter and open in a side panel; they never
 * cover the text.
 */
export function Document({
  title,
  summary,
  meta,
  blocks,
  env,
  counts,
  activeBlock,
  onComment,
  commentLabel = "Comment",
  allowNew = true,
  onOpenLink,
  editIndex,
  editor,
  onEditBlock,
  footer,
  changed,
  changedLabel = "Changed",
  onQuote,
  quotes,
}: {
  title: ReactNode;
  /** One line under the title (front matter summary), or the owner's editor for it. */
  summary?: ReactNode;
  meta?: ReactNode;
  blocks: DocBlock[];
  env: RenderEnv;
  /** Open threads per block id. */
  counts?: Record<string, number>;
  activeBlock?: string | null;
  onComment?: (blockId: string) => void;
  commentLabel?: string;
  /** false: only parts that already have comments get a gutter button (the owner answers; readers start threads). */
  allowNew?: boolean;
  onOpenLink?: (target: string) => void;
  /** Owner editing: the block at editIndex is replaced by editor; clicking another block asks to edit it. */
  editIndex?: number | null;
  editor?: ReactNode;
  onEditBlock?: (index: number) => void;
  footer?: ReactNode;
  /** Blocks that are new or changed since the revision the reader compares with; marked in the margin. */
  changed?: Set<string>;
  changedLabel?: string;
  /** Selecting text in a part offers "Comment on this"; called with the block and the selected words. */
  onQuote?: (blockId: string, quote: string) => void;
  /** Phrases open threads are about, highlighted where the browser can (CSS Custom Highlight API). */
  quotes?: { block: string; quote: string }[];
}) {
  const ref = useRef<HTMLElement>(null);
  const [sel, setSel] = useState<{ block: string; quote: string; top: number; left: number } | null>(null);
  useEffect(() => {
    if (!onQuote) return;
    // Track the selection while it is inside one part's text; the button sits just above where it ends.
    const update = () => {
      const art = ref.current;
      const s = window.getSelection();
      if (!art || !s || s.isCollapsed || s.rangeCount === 0) return setSel(null);
      const r = s.getRangeAt(0);
      const start = (r.startContainer instanceof Element ? r.startContainer : r.startContainer.parentElement)?.closest("[data-prose]");
      const end = (r.endContainer instanceof Element ? r.endContainer : r.endContainer.parentElement)?.closest("[data-prose]");
      const block = start?.closest("[data-block]")?.getAttribute("data-block");
      const quote = s.toString().replace(/\s+/g, " ").trim();
      if (!start || start !== end || !block || !art.contains(start) || quote.length < 2) return setSel(null);
      const rect = r.getBoundingClientRect();
      const box = art.getBoundingClientRect();
      setSel({ block, quote: [...quote].slice(0, 500).join(""), top: rect.top - box.top, left: Math.min(Math.max(rect.left - box.left + rect.width / 2, 48), box.width - 48) });
    };
    document.addEventListener("selectionchange", update);
    return () => document.removeEventListener("selectionchange", update);
  }, [onQuote]);
  useEffect(() => {
    const art = ref.current;
    const hl = (globalThis as { CSS?: { highlights?: Map<string, unknown> } }).CSS?.highlights;
    const HL = (globalThis as { Highlight?: new (...r: Range[]) => unknown }).Highlight;
    if (!art || !hl || !HL) return;
    const ranges: Range[] = [];
    for (const q of quotes ?? []) {
      const el = art.querySelector(`[data-block="${CSS.escape(q.block)}"] [data-prose]`);
      const r = el ? findText(el, q.quote) : null;
      if (r) ranges.push(r);
    }
    if (ranges.length) hl.set("vq-quote", new HL(...ranges));
    else hl.delete("vq-quote");
    return () => void hl.delete("vq-quote");
  });
  let figNo = 0;
  const html = useMemo(() => {
    const top = topHeading(blocks.flatMap((b) => (b.kind === "text" ? [b.text] : [])));
    const e = { ...env, topHeading: top };
    return blocks.map((b) => (b.kind === "text" || b.kind === "code" ? renderMarkdown(b.text, e) : ""));
  }, [blocks, env]);
  function click(e: MouseEvent<HTMLElement>, i: number) {
    const a = (e.target as HTMLElement).closest("a");
    if (a?.classList.contains("wiki")) {
      e.preventDefault();
      onOpenLink?.(a.getAttribute("data-open") ?? "");
      return;
    }
    if (a) return; // ordinary links open normally
    if (onEditBlock && (window.getSelection()?.toString() ?? "") === "") onEditBlock(i);
  }
  return (
    <article className={s.doc} ref={ref}>
      <header className={s.head}>
        <h1 className={s.title}>{title}</h1>
        {summary ? <div className={s.summary}>{summary}</div> : null}
        {meta ? <div className={s.meta}>{meta}</div> : null}
      </header>
      {sel && onQuote ? (
        <button
          type="button"
          className={s.quoteBtn}
          style={{ top: sel.top, left: sel.left }}
          onMouseDown={(e) => e.preventDefault() /* keep the selection */}
          onClick={() => { onQuote(sel.block, sel.quote); setSel(null); }}
        >
          <Icon icon={TextQuote} />Comment on this
        </button>
      ) : null}
      {blocks.length === 0 && editIndex == null ? <p className={s.emptyDoc}>Nothing written yet.</p> : null}
      {blocks.map((b, i) => {
        if (b.kind === "figure" && b.figure) figNo++;
        const n = counts?.[b.id] ?? 0;
        const editing = editIndex === i;
        return (
          <section key={b.id + ":" + i} className={s.block} data-block={b.id} data-active={activeBlock === b.id ? "true" : undefined} data-changed={changed?.has(b.id) ? "true" : undefined} data-editable={onEditBlock && !editing ? "true" : undefined}>
            {changed?.has(b.id) ? <span className={s.vh}>{changedLabel}: </span> : null}
            <div className={s.body} onClick={editing ? undefined : (e) => click(e, i)} onKeyDown={onEditBlock && !editing ? (e) => { if (e.key === "Enter" && e.target === e.currentTarget) { e.preventDefault(); onEditBlock(i); } } : undefined} tabIndex={onEditBlock && !editing ? 0 : undefined} aria-label={onEditBlock && !editing ? `Edit: ${editLabel(b)}` : undefined}>
              {editing ? editor : b.kind === "figure" ? <FigureView fig={b.figure} n={figNo} /> : b.kind === "run" ? <RunLine run={b.run} /> : <div className={b.kind === "code" ? s.prose + " " + s.codeBlock : s.prose} data-prose="" dangerouslySetInnerHTML={{ __html: html[i] ?? "" }} />}
            </div>
            {onComment && !editing && (allowNew || n > 0) ? (
              <div className={s.gutter}>
                <button type="button" className={n > 0 ? s.count : s.add} onClick={() => onComment(b.id)} aria-label={n > 0 ? `${n} comment${n === 1 ? "" : "s"} on this part` : `${commentLabel} on this part`} aria-pressed={activeBlock === b.id}>
                  <Icon icon={n > 0 ? MessageSquare : MessageSquarePlus} />
                  {n > 0 ? <span>{n}</span> : null}
                </button>
              </div>
            ) : null}
          </section>
        );
      })}
      {editIndex != null && editIndex >= blocks.length ? <section className={s.block}><div className={s.body}>{editor}</div></section> : null}
      {footer ? <footer className={s.footer}>{footer}</footer> : null}
    </article>
  );
}

function editLabel(b: DocBlock): string {
  if (b.kind === "figure") return b.figure ? `figure “${b.figure.title}”` : "a missing figure";
  if (b.kind === "run") return b.run ? `run ${b.run.name}` : "a missing run";
  const t = plainText(b.text).replace(/\s+/g, " ").trim();
  return t.length > 60 ? t.slice(0, 59) + "…" : t || "an empty part";
}

/** The first place quote occurs in el's text, whitespace-insensitive, as a Range; null if the text changed. */
function findText(el: Element, quote: string): Range | null {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  let text = "";
  const map: { node: Text; offset: number }[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = n as Text;
    nodes.push(t);
    for (let i = 0; i < t.data.length; i++) {
      const c = /\s/.test(t.data[i] as string) ? " " : (t.data[i] as string);
      if (c === " " && text.endsWith(" ")) continue;
      text += c;
      map.push({ node: t, offset: i });
    }
  }
  const at = text.indexOf(quote);
  if (at < 0 || !map[at] || !map[at + quote.length - 1]) return null;
  const r = document.createRange();
  r.setStart(map[at]!.node, map[at]!.offset);
  const last = map[at + quote.length - 1]!;
  r.setEnd(last.node, last.offset + 1);
  return r;
}

function provenanceLine(p: Provenance | null | undefined): string[] {
  if (!p) return [];
  const parts = [p.source.name];
  const v = p.view;
  if (v.spectralValue != null && v.spectralUnit) parts.push(`${Number(v.spectralValue.toPrecision(6))} ${v.spectralUnit}`);
  else if (channelText(p)) parts.push(channelText(p));
  if (p.run) parts.push(`run ${p.run.name}`);
  if (p.interpretation) parts.push(p.interpretation.kind === "axial" ? "axial orientation (0° = 180°)" : p.interpretation.kind);
  return parts;
}

function FigureView({ fig, n }: { fig: DocFigure | null; n: number }) {
  if (!fig) return <p className={s.missing}>A figure this report referenced no longer exists.</p>;
  const line = provenanceLine(fig.provenance);
  return (
    <figure className={s.figure}>
      <img className={s.img} src={fig.src} width={fig.width} height={fig.height} alt={fig.title} loading="lazy" />
      <figcaption className={s.caption}>
        <span className={s.figNo}>Figure {n}.</span> <span className={s.figTitle}>{fig.title}.</span> {fig.caption}
      </figcaption>
      {line.length ? (
        <details className={s.prov}>
          <summary>
            {line.join(" · ")} · <span className={s.provNote}>a picture, not data; header values as declared</span>
          </summary>
          <ul>{fig.provenance?.statements.map((t) => <li key={t}>{t}</li>)}</ul>
        </details>
      ) : null}
    </figure>
  );
}

function RunLine({ run }: { run: RunSummary | null }) {
  if (!run) return <p className={s.missing}>A run this report referenced no longer exists.</p>;
  const metrics = (run.metrics ?? []).filter((m) => m.value !== null).slice(0, 4);
  const qc = QC[run.review?.qc ?? "not_assessed"] ?? run.review?.qc ?? "not assessed";
  return (
    <div className={s.run}>
      <div className={s.runHead}>
        <code className={s.runName}>{run.name}</code>
        <span className={s.runMeta}>{[run.kind.toUpperCase(), run.generatedAt ? run.generatedAt.slice(0, 10) : null, run.dirty ? "uncommitted code" : null].filter(Boolean).join(" · ")}</span>
        <span className={s.runQc} data-qc={run.review?.qc ?? "not_assessed"}>{qc}</span>
      </div>
      {metrics.length ? (
        <dl className={s.metrics}>
          {metrics.map((m) => (
            <div key={m.key}>
              <dt title={m.definition || undefined}>{m.key}</dt>
              <dd>{metricText(m)}</dd>
            </div>
          ))}
        </dl>
      ) : null}
    </div>
  );
}
