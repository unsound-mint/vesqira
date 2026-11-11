import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { ChevronRight, FileText, MessageSquare, Network, Plus, Search, Share2, Upload } from "lucide-react";
import { listFigures, figurePreviewUrl, type Figure } from "../api/boards";
import { listRuns, type Run } from "../api/runs";
import * as api from "../api/reports";
import type { ReportSummary, ReportView } from "../api/reports";
import { Button, Icon } from "../ui";
import { blockSource, joinBlocks, splitBlocks, type SourceBlock } from "./blocks";
import { Document, type DocBlock, type RunSummary } from "./Document";
import type { RenderEnv } from "./markdown";
import { Graph } from "./Graph";
import { ReportThreads } from "./ReportThreads";
import { PublishDialog } from "./PublishDialog";
import { SharePanel } from "./SharePanel";
import { ChangesReview } from "./ChangesReview";
import { InsertMenu } from "./InsertMenu";
import { relTime } from "./time";
import { listTitle, longDate, shortDay, reportDate, reportSummary, setFrontValue, withFront } from "./front";
import { ancestors, firstPage, visibleRows } from "./tree";
import { PageMenu } from "./PageMenu";
import s from "./ReportsView.module.css";

const OPEN_KEY = "vesqira.reports.open";

function loadOpen(): Set<number> {
  try {
    const v = JSON.parse(localStorage.getItem(OPEN_KEY) ?? "[]") as unknown;
    return new Set(Array.isArray(v) ? v.filter((x): x is number => typeof x === "number") : []);
  } catch {
    return new Set();
  }
}

const date = (iso: string) => new Date(iso).toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });

function runSummary(r: Run): RunSummary {
  return { name: r.name, kind: r.kind, gitCommit: r.gitCommit, dirty: r.dirty, generatedAt: r.generatedAt, params: r.params, metrics: r.metrics, review: r.review };
}

/**
 * Reports (ADR 0036, mock B approved 2026-11-10): a tree of report pages, one page at a time drawn exactly as the
 * supervisor will read it, edited block by block in place, and a graph of how reports, runs and figures link.
 * One primary action (Publish); comments and sharing open in a side panel that narrows the page instead of covering it.
 */
export function ReportsView({ onOpenRun }: { onOpenRun?: (id: number) => void }) {
  const [list, setList] = useState<ReportSummary[]>([]);
  const [current, setCurrent] = useState<number | null>(null);
  const [view, setView] = useState<ReportView | null>(null);
  const [figures, setFigures] = useState<Figure[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [mode, setMode] = useState<"page" | "graph" | "source">("page");
  const [panel, setPanel] = useState<null | "comments" | "share">(null);
  const [activeBlock, setActiveBlock] = useState<string | null>(null);
  const [editIndex, setEditIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState("");
  const [titleDraft, setTitleDraft] = useState<string | null>(null);
  const [source, setSource] = useState("");
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [publishing, setPublishing] = useState(false);
  const [reviewing, setReviewing] = useState(false);
  const [graph, setGraph] = useState<api.Graph | null>(null);
  const [state, setState] = useState<api.PublishState | null>(null);
  const [open, setOpen] = useState<Set<number>>(loadOpen);
  const [summaryDraft, setSummaryDraft] = useState<string | null>(null);
  const [dateEdit, setDateEdit] = useState(false);
  // Saves run one after another, each on the report as the previous one left it. A save made while another is in flight used to be
  // dropped (and built from the stale body), so an edit made quickly after another (a summary right after a text part) was lost silently.
  const queue = useRef<Promise<void>>(Promise.resolve());
  const viewRef = useRef<ReportView | null>(null);
  const renameNext = useRef<number | null>(null); // a report just created opens with its title ready to type

  const refreshList = useCallback(() => api.listReports().then(setList).catch((e: unknown) => setError(msg(e))), []);
  const load = useCallback(async (id: number) => {
    try {
      const v = await api.getReport(id);
      viewRef.current = v;
      setView(v);
      setSource(v.body);
      setError(null);
    } catch (e) {
      setError(msg(e));
    }
  }, []);
  useEffect(() => {
    void refreshList();
    listFigures().then(setFigures).catch(() => setFigures([]));
    listRuns().then(setRuns).catch(() => setRuns([]));
    api.publishState().then(setState).catch(() => setState(null));
  }, [refreshList]);
  useEffect(() => {
    if (current == null && list.length) setCurrent(firstPage(list));
  }, [list, current]);
  // The open page's parents are always expanded, so the tree shows where it is.
  useEffect(() => {
    if (current == null) return;
    const up = ancestors(list, current);
    if (up.some((id) => !open.has(id))) setOpen((o) => new Set([...o, ...up]));
  }, [current, list, open]);
  useEffect(() => {
    try {
      localStorage.setItem(OPEN_KEY, JSON.stringify([...open]));
    } catch {
      /* storage may be unavailable */
    }
  }, [open]);
  useEffect(() => {
    setEditIndex(null);
    setTitleDraft(null);
    setSummaryDraft(null);
    setDateEdit(false);
    setReviewing(false);
    setActiveBlock(null);
    if (current != null) void load(current);
  }, [current, load]);
  // A report just created opens with its title ready to type, but only once its page is the one on screen: until it has loaded,
  // `view` is still the previous report (or none), and a title typed then would rename that one, or be dropped.
  useEffect(() => {
    if (view && renameNext.current === view.id) {
      renameNext.current = null;
      setTitleDraft(view.title);
    }
  }, [view]);
  useEffect(() => {
    if (mode === "graph") api.reportGraph().then(setGraph).catch((e: unknown) => setError(msg(e)));
  }, [mode, list]);
  // Opening a report the agent changed shows the change summary; it is marked seen only when the owner says so.
  const summary = list.find((r) => r.id === current) ?? null;

  const figByRev = useMemo(() => new Map(figures.map((f) => [f.revision, f])), [figures]);
  const runById = useMemo(() => new Map(runs.map((r) => [r.id, r])), [runs]);
  const titles = useMemo(() => new Map(list.map((r) => [r.title.toLowerCase(), r])), [list]);
  const src: SourceBlock[] = useMemo(() => splitBlocks(view?.body ?? ""), [view?.body]);
  const blockIds = view?.blockIds ?? [];
  const blocks: DocBlock[] = useMemo(
    () =>
      src.map((b, i): DocBlock => {
        const id = blockIds[i] || `draft-${i}`;
        if (b.kind === "figure") {
          const f = figByRev.get(b.figure);
          return { id, kind: "figure", figure: f ? { src: figurePreviewUrl(f.id), width: f.width, height: f.height, title: f.title, caption: f.caption, provenance: f.provenance } : null };
        }
        if (b.kind === "run") {
          const r = runById.get(b.run);
          return { id, kind: "run", run: r ? runSummary(r) : null };
        }
        return { id, kind: b.kind, text: b.text };
      }),
    [src, blockIds, figByRev, runById],
  );
  const env: RenderEnv = useMemo(
    () => ({
      resolveImage: (u) => (u.startsWith("attachment:") ? api.attachmentUrl(u.slice("attachment:".length)) : null),
      resolveWiki: (inner, label) => {
        if (inner.startsWith("run:")) {
          const r = runById.get(Number(inner.slice(4)));
          return { kind: "mention", label: r?.name ?? "missing run", mono: true };
        }
        if (inner.startsWith("figure:")) return { kind: "mention", label: figByRev.get(inner.slice(7))?.title ?? "missing figure" };
        const t = titles.get(inner.toLowerCase());
        return t ? { kind: "link", label: label ?? t.title, onOpen: String(t.id) } : { kind: "missing", label: label ?? inner };
      },
    }),
    [runById, figByRev, titles],
  );
  const threads = view?.comments.filter((c) => c.parentId === 0) ?? [];
  const counts = useMemo(() => {
    const m: Record<string, number> = {};
    for (const t of threads) if (!t.resolvedAt) m[t.block] = (m[t.block] ?? 0) + 1;
    return m;
  }, [threads]);
  const openThreads = threads.filter((t) => !t.resolvedAt).length;
  const quotes = useMemo(() => threads.filter((t) => t.quote && !t.resolvedAt).map((t) => ({ block: t.block, quote: t.quote as string })), [threads]);

  type Body = string | undefined | ((cur: ReportView) => string | undefined);
  function save(body: Body, title?: string): Promise<void> {
    const id = viewRef.current?.id;
    if (id === undefined) return Promise.resolve();
    const run = queue.current.then(() => doSave(id, body, title));
    queue.current = run;
    return run;
  }
  async function doSave(id: number, update: Body, title?: string) {
    try {
      const cur = viewRef.current?.id === id ? viewRef.current : await api.getReport(id);
      const body = typeof update === "function" ? update(cur) : update;
      if ((body === undefined || body === cur.body) && (title === undefined || title === cur.title)) return;
      try {
        await api.saveReport(id, cur.revision, { body, title });
        if (viewRef.current?.id === id) await load(id);
        void refreshList();
      } catch (e) {
        if (e instanceof api.ConflictError) {
          setNotice(`${e.current.lastAuthor === "agent" ? "The agent" : "Someone"} saved this report while you were editing. Your text is kept below; the page shows their version.`);
          if (body !== undefined) setSource(body);
          await load(id);
          setMode("source");
        } else setError(msg(e));
      }
    } catch (e) {
      setError(msg(e));
    }
  }

  function startEdit(i: number) {
    if (editIndex !== null) commitEdit();
    const b = src[i];
    setEditIndex(i);
    setDraft(b ? blockSource(b) : "");
  }
  function commitEdit() {
    if (editIndex === null) return;
    const parts = src.map(blockSource);
    if (editIndex >= parts.length) parts.push(draft);
    else parts[editIndex] = draft;
    setEditIndex(null);
    void save((cur) => withFront(cur.body, joinBlocks(parts)));
  }
  function editKeys(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Escape") {
      e.preventDefault();
      setEditIndex(null);
    } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      commitEdit();
    }
  }
  function insert(markdown: string) {
    const parts = src.map(blockSource);
    const at = editIndex !== null ? editIndex + 1 : parts.length;
    if (editIndex !== null) parts[editIndex] = draft;
    parts.splice(at, 0, markdown);
    setEditIndex(null);
    void save((cur) => withFront(cur.body, joinBlocks(parts)));
  }

  async function newReport(parent = 0) {
    let n = 1;
    while (titles.has(`untitled ${n}`)) n++;
    try {
      const r = await api.createReport(`Untitled ${n}`, parent);
      renameNext.current = r.id;
      if (parent) setOpen((o) => new Set([...o, parent]));
      await refreshList();
      setCurrent(r.id);
      setMode("page");
    } catch (e) {
      setError(msg(e));
    }
  }

  const status = !summary ? "" : summary.published === 0 ? "Draft, not shared" : summary.publishedFrom === summary.revision ? `Published · revision ${summary.published}` : `Changed since revision ${summary.published}`;
  const byId = useMemo(() => new Map(list.map((r) => [r.id, r])), [list]);
  const filtered = query ? list.filter((r) => r.title.toLowerCase().includes(query.toLowerCase())) : null;
  const rows = useMemo(() => visibleRows(list, open), [list, open]);
  const toggle = (id: number) => setOpen((o) => { const n = new Set(o); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const pick = (id: number) => { setCurrent(id); if (mode === "graph") setMode("page"); };
  const summaryText = view ? reportSummary(view.body) : "";
  const dateValue = view ? reportDate(view.body) : "";
  const lastPub = view?.publications[0];

  return (
    <div className={s.root} data-panel={panel ? "open" : undefined}>
      <nav className={s.tree} aria-label="Reports">
        <div className={s.treeHead}>
          <label className={s.search}>
            <Icon icon={Search} />
            <input type="search" placeholder="Search reports" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search reports" />
          </label>
          <button type="button" className={s.iconBtn} onClick={() => void newReport()} aria-label="New report" title="New report"><Icon icon={Plus} /></button>
        </div>
        {filtered ? (
          <ul className={s.list} aria-label="Search results">
            {filtered.map((r) => (
              <li key={r.id}>
                <button type="button" className={s.item} aria-current={r.id === current && mode !== "graph" ? "page" : undefined} onClick={() => pick(r.id)}>
                  <Icon icon={FileText} />
                  <span className={s.itemTitle}>{r.title}{r.parent && byId.get(r.parent) ? <span className={s.itemPath}> in {byId.get(r.parent)!.title}</span> : null}</span>
                  {r.agentUnseen ? <span className={s.dot} title="Changed by the agent since you looked" aria-label="changed by the agent" /> : null}
                </button>
              </li>
            ))}
            {filtered.length === 0 ? <li className={s.treeEmpty}>No page is called that.</li> : null}
          </ul>
        ) : (
          <ul className={s.list}>
            {rows.map(({ r, depth, hasChildren }) => (
              <li key={r.id} className={s.treeRow} style={{ paddingLeft: `calc(${depth} * var(--sp-4))` }}>
                {hasChildren ? (
                  <button type="button" className={s.twisty} aria-expanded={open.has(r.id)} aria-label={`${open.has(r.id) ? "Collapse" : "Expand"} ${r.title}`} onClick={() => toggle(r.id)}>
                    <Icon icon={ChevronRight} />
                  </button>
                ) : <span className={s.twistySpace} />}
                <button type="button" className={s.item} aria-current={r.id === current && mode !== "graph" ? "page" : undefined} onClick={() => pick(r.id)} title={r.title}>
                  <span className={s.itemTitle}>{r.date ? listTitle(r.title) : r.title}</span>
                  {r.agentUnseen ? <span className={s.dot} title="Changed by the agent since you looked" aria-label="changed by the agent" /> : null}
                  {r.date ? <span className={s.itemDate}>{shortDay(r.date)}</span> : null}
                  {r.published ? <span className={s.pubMark} title={`Published, revision ${r.published}`} aria-label="published" /> : null}
                </button>
                <button type="button" className={s.addChild} aria-label={`New page inside ${r.title}`} title="New page inside" onClick={() => void newReport(r.id)}><Icon icon={Plus} /></button>
              </li>
            ))}
          </ul>
        )}
        {list.length === 0 ? <p className={s.treeEmpty}>No reports yet. Start one, ask your agent to draft one, or import a Notion export with <code>vesqira import notion</code>.</p> : null}
        <div className={s.treeFoot}>
          {state?.url ? <span>{state.lastError ? "Publish server unreachable" : state.lastSync ? `Synced ${relTime(state.lastSync)}` : "Publish server set up"}</span> : <span>No publish server yet</span>}
        </div>
      </nav>

      <section className={s.main} aria-label="Report">
        <div className={s.bar}>
          <div className={s.seg} role="group" aria-label="View">
            <button type="button" aria-pressed={mode !== "graph"} onClick={() => setMode("page")}><Icon icon={FileText} />Page</button>
            <button type="button" aria-pressed={mode === "graph"} onClick={() => setMode("graph")}><Icon icon={Network} />Graph</button>
          </div>
          <span className={s.grow} />
          {view && mode !== "graph" ? (
            <>
              <span className={s.status}>{status}</span>
              <button type="button" className={s.ghost} aria-pressed={mode === "source"} onClick={() => { if (mode === "source") { void save(source); setMode("page"); } else { setSource(view.body); setMode("source"); } }}>{mode === "source" ? "Done" : "Markdown"}</button>
              <button type="button" className={s.ghost} aria-pressed={panel === "comments"} onClick={() => setPanel(panel === "comments" ? null : "comments")}><Icon icon={MessageSquare} />Comments{openThreads ? <span className={s.badge}>{openThreads}</span> : null}</button>
              <button type="button" className={s.ghost} aria-pressed={panel === "share"} onClick={() => setPanel(panel === "share" ? null : "share")}><Icon icon={Share2} />Share</button>
              <Button variant="primary" icon={Upload} onClick={() => { if (editIndex !== null) commitEdit(); void queue.current.then(() => setPublishing(true)); }}>Publish…</Button>
              <PageMenu
                view={view}
                list={list}
                onAddChild={() => void newReport(view.id)}
                onMove={async (parent) => { try { await api.moveReport(view.id, parent); await refreshList(); await load(view.id); } catch (e) { setError(msg(e)); } }}
                onWithdraw={async () => { try { await api.withdrawReport(view.id); await load(view.id); await refreshList(); setNotice(`“${view.title}” was withdrawn: readers no longer see it.`); } catch (e) { setError(msg(e)); } }}
                onDelete={async () => { try { await api.deleteReport(view.id); setCurrent(null); viewRef.current = null; setView(null); await refreshList(); } catch (e) { setError(msg(e)); } }}
              />
            </>
          ) : null}
        </div>
        {error ? <p className={s.error} role="alert">{error} <button type="button" onClick={() => setError(null)}>Dismiss</button></p> : null}
        {notice ? <p className={s.notice} role="status">{notice} <button type="button" onClick={() => setNotice(null)}>Dismiss</button></p> : null}
        {summary?.agentUnseen && mode === "page" && !reviewing ? (
          <div className={s.agent} role="status">
            <span>{state?.agentName || "The agent"} changed this report since you last looked.</span>
            <button type="button" onClick={() => setReviewing(true)}>Review changes</button>
            <button type="button" onClick={() => { if (view) void api.markSeen(view.id, view.revision).then(refreshList); }}>Mark as read</button>
          </div>
        ) : null}

        <div className={s.scroll}>
          {mode === "graph" ? (
            graph ? <Graph graph={graph} current={current != null ? `report:${current}` : null} onOpen={(n) => { if (n.kind === "report") { setCurrent(Number(n.id.slice(7))); setMode("page"); } else if (n.kind === "run" && onOpenRun) onOpenRun(Number(n.id.slice(4))); }} /> : <p className={s.loading}>Loading…</p>
          ) : !view ? (
            list.length ? <p className={s.loading}>Loading…</p> : (
              <div className={s.empty}>
                <h2>Reports</h2>
                <p>Write up results for your supervisor: text, equations, pinned figures and runs, linked to each other. Your agent can draft them; only you publish.</p>
                <Button variant="primary" icon={Plus} onClick={() => void newReport()}>New report</Button>
              </div>
            )
          ) : reviewing ? (
            <ChangesReview report={view} agentName={state?.agentName || "the agent"} env={env} onDone={async (body) => { if (body !== null) await save(body); await api.markSeen(view.id, view.revision + (body !== null && body !== view.body ? 1 : 0)); setReviewing(false); await load(view.id); void refreshList(); }} onCancel={() => setReviewing(false)} />
          ) : mode === "source" ? (
            <div className={s.source}>
              <label htmlFor="report-source" className={s.sourceLabel}>Markdown of “{view.title}”</label>
              <textarea id="report-source" value={source} onChange={(e) => setSource(e.target.value)} spellCheck />
              <p className={s.hint}>Figures: <code>![[figure:…]]</code> on its own line · runs: <code>[[run:…]]</code> · links: <code>[[Report title]]</code> · math: <code>$…$</code>, <code>$$…$$</code></p>
            </div>
          ) : (
            <Document
              title={titleDraft !== null ? (
                <input className={s.titleInput} value={titleDraft} autoFocus aria-label="Title" onChange={(e) => setTitleDraft(e.target.value)} onBlur={() => { const t = titleDraft.trim(); setTitleDraft(null); if (t) void save(undefined, t); }} onKeyDown={(e) => { if (e.key === "Enter") (e.target as HTMLInputElement).blur(); if (e.key === "Escape") setTitleDraft(null); }} />
              ) : (
                <button type="button" className={s.titleBtn} onClick={() => setTitleDraft(view.title)} title="Rename">{view.title}</button>
              )}
              summary={
                summaryDraft !== null ? (
                  <textarea
                    className={s.summaryInput}
                    value={summaryDraft}
                    autoFocus
                    rows={2}
                    aria-label="Summary, one or two sentences under the title"
                    onChange={(e) => setSummaryDraft(e.target.value.replace(/\n/g, " "))}
                    onBlur={() => { const v = summaryDraft; setSummaryDraft(null); if (v.trim() !== summaryText) void save((cur) => setFrontValue(cur.body, "summary", v)); }}
                    onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); (e.target as HTMLTextAreaElement).blur(); } if (e.key === "Escape") setSummaryDraft(null); }}
                  />
                ) : (
                  <button type="button" className={s.summaryBtn} data-empty={summaryText ? undefined : "true"} onClick={() => setSummaryDraft(summaryText)}>
                    {summaryText || "Add a one-line summary"}
                  </button>
                )
              }
              meta={
                <>
                  {dateEdit ? (
                    <input
                      type="date"
                      className={s.dateInput}
                      autoFocus
                      defaultValue={dateValue}
                      aria-label="Date of this report"
                      onBlur={(e) => { setDateEdit(false); if (e.target.value !== dateValue) void save((cur) => setFrontValue(cur.body, "date", e.target.value)); }}
                      onKeyDown={(e) => { if (e.key === "Enter") (e.target as HTMLInputElement).blur(); if (e.key === "Escape") setDateEdit(false); }}
                    />
                  ) : (
                    <button type="button" className={s.metaBtn} onClick={() => setDateEdit(true)} title="The date this report is about">{dateValue ? longDate(dateValue) : "Add a date"}</button>
                  )}
                  <span>{lastPub ? `Revision ${lastPub.number} published ${date(lastPub.publishedAt)}` : "Not published yet"}</span>
                  <span>{view.lastAuthor === "agent" ? `Last edited by ${state?.agentName || "the agent"}` : "Last edited by you"} · {date(view.updatedAt)}</span>
                </>
              }
              blocks={blocks}
              env={env}
              counts={counts}
              activeBlock={activeBlock}
              onComment={(id) => { setActiveBlock(id); setPanel("comments"); }}
              allowNew={false}
              quotes={quotes}
              onOpenLink={(id) => setCurrent(Number(id))}
              editIndex={editIndex}
              onEditBlock={startEdit}
              editor={
                <div className={s.editor}>
                  <textarea autoFocus value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={editKeys} onBlur={commitEdit} rows={Math.min(24, Math.max(3, draft.split("\n").length + 1))} aria-label="Edit this part (Markdown). Ctrl+Enter saves, Escape cancels." />
                  <span className={s.hint}>Markdown · Ctrl+Enter to save · Esc to cancel</span>
                </div>
              }
              footer={
                <div className={s.foot}>
                  <div className={s.adds}>
                    <button type="button" onClick={() => startEdit(src.length)}><Icon icon={Plus} />Text</button>
                    <InsertMenu figures={figures} runs={runs} onInsert={insert} />
                  </div>
                  {view.backlinks.length ? (
                    <div className={s.backlinks}>
                      <h2>Linked from</h2>
                      <ul>{view.backlinks.map((b) => <li key={b.id}><button type="button" onClick={() => setCurrent(b.id)}>{b.title}</button></li>)}</ul>
                    </div>
                  ) : null}
                  {view.unresolvedLinks?.length ? <p className={s.hint}>No report is called {view.unresolvedLinks.map((t) => `“${t}”`).join(", ")}; those links show as plain text.</p> : null}
                </div>
              }
            />
          )}
        </div>
      </section>

      {panel ? (
        <section className={s.panel} aria-label={panel === "comments" ? "Comments" : "Share"}>
          {panel === "comments" && view ? (
            <ReportThreads view={view} blocks={blocks} activeBlock={activeBlock} onSelectBlock={setActiveBlock} onChanged={() => load(view.id)} onClose={() => setPanel(null)} />
          ) : panel === "share" ? (
            <SharePanel state={state} onState={setState} onClose={() => setPanel(null)} />
          ) : null}
        </section>
      ) : null}

      {publishing && view ? <PublishDialog report={view} onClose={() => setPublishing(false)} onPublished={() => { setPublishing(false); void load(view.id); void refreshList(); }} onSetup={() => { setPublishing(false); setPanel("share"); }} /> : null}
    </div>
  );
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : "Something went wrong.";
}
