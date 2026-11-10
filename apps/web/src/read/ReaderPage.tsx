import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Check, ChevronDown, Menu, MessageSquare, Moon, Sun, X } from "lucide-react";
import { Button, IconButton } from "../ui";
import { Icon } from "../ui/Icon";
import { Document, type DocBlock } from "../reports/Document";
import { plainText, type RenderEnv } from "../reports/markdown";
import { assetUrl, getReport, getSpace, postComment, ReadError, type ReaderComment, type ReaderRevision, type ReportView, type SpaceView } from "./api";
import { longDate, relative, shortDate } from "./dates";
import s from "./ReaderPage.module.css";

type Route = { key: string | null; rev: number };

function parseHash(): Route {
  const p = new URLSearchParams(location.hash.replace(/^#/, ""));
  const rev = Number(p.get("rev"));
  return { key: p.get("r"), rev: Number.isInteger(rev) && rev > 0 ? rev : 0 };
}

const THEME_KEY = "vesqira.theme";

function initialTheme(): "light" | "dark" {
  try {
    const v = localStorage.getItem(THEME_KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    /* storage may be unavailable */
  }
  return matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

const STALE_MS = 15 * 60 * 1000;
const quote = (t: string) => (t.length > 120 ? t.slice(0, 117).trimEnd() + "…" : t);

function blockText(b: ReaderRevision["blocks"][number]): string {
  if (b.kind === "text") return plainText(b.text ?? "");
  if (b.kind === "figure") return b.figure?.title ? `Figure: ${b.figure.title}` : "Figure";
  return "Run";
}

/** What a professor sees from a personal link: the reports shared with them, and a way to comment. Nothing else. */
export function ReaderPage({ token }: { token: string }) {
  const [space, setSpace] = useState<SpaceView | null>(null);
  const [spaceErr, setSpaceErr] = useState<ReadError | null>(null);
  const [route, setRoute] = useState<Route>(parseHash);
  const [view, setView] = useState<ReportView | null>(null);
  const [viewErr, setViewErr] = useState<ReadError | null>(null);
  const [theme, setTheme] = useState(initialTheme);
  const [panel, setPanel] = useState(false);
  const [drawer, setDrawer] = useState(false);
  const [revMenu, setRevMenu] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    try {
      localStorage.setItem(THEME_KEY, theme);
    } catch {
      /* storage may be unavailable */
    }
  }, [theme]);

  useEffect(() => {
    const on = () => setRoute(parseHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  useEffect(() => {
    setSpaceErr(null);
    getSpace(token).then(setSpace, (e: unknown) => setSpaceErr(e instanceof ReadError ? e : new ReadError("other", "Something went wrong.")));
  }, [token, attempt]);

  const key = route.key && space?.reports.some((r) => r.key === route.key) ? route.key : (space?.reports[0]?.key ?? null);

  const loadReport = useCallback(() => {
    if (!key) return Promise.resolve();
    return getReport(token, key, route.rev || undefined).then(
      (v) => { setView(v); setViewErr(null); },
      (e: unknown) => setViewErr(e instanceof ReadError ? e : new ReadError("other", "Something went wrong.")),
    );
  }, [token, key, route.rev]);

  useEffect(() => {
    setView(null);
    setSelected(null);
    setRevMenu(false);
    void loadReport();
  }, [loadReport]);

  useEffect(() => {
    if (!panel && !drawer && !revMenu) return;
    const esc = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setPanel(false);
      setDrawer(false);
      setRevMenu(false);
    };
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [panel, drawer, revMenu]);

  const open = useCallback((k: string, rev = 0) => {
    location.hash = `r=${encodeURIComponent(k)}${rev ? `&rev=${rev}` : ""}`;
    setDrawer(false);
  }, []);

  const rev = view?.revision;
  const env = useMemo<RenderEnv>(() => {
    const shared = new Set<string>(space?.reports.map((r) => r.key) ?? []);
    for (const b of rev?.blocks ?? []) for (const k of b.shared ?? []) shared.add(k);
    return {
      resolveImage: (src) => (src.startsWith("asset:") ? assetUrl(token, src.slice("asset:".length)) : null),
      resolveWiki: (inner, label) => {
        if (inner.startsWith("report:")) {
          const k = inner.slice("report:".length);
          const text = label || space?.reports.find((r) => r.key === k)?.title || k;
          return shared.has(k) ? { kind: "link", label: text, onOpen: k } : { kind: "unshared", label: text };
        }
        return { kind: "missing", label: label ?? inner };
      },
    };
  }, [space, rev, token]);

  const blocks = useMemo<DocBlock[]>(
    () =>
      (rev?.blocks ?? []).map((b): DocBlock => {
        if (b.kind === "figure") {
          const f = b.figure;
          return { id: b.id, kind: "figure", figure: f ? { src: assetUrl(token, f.asset), width: f.width, height: f.height, title: f.title, caption: f.caption, provenance: f.provenance } : null };
        }
        if (b.kind === "run") return { id: b.id, kind: "run", run: b.run ?? null };
        return { id: b.id, kind: "text", text: b.text ?? "" };
      }),
    [rev, token],
  );

  const threads = useMemo(() => {
    const roots = (view?.comments ?? []).filter((c) => !c.parentId).sort((a, b) => a.id - b.id);
    const replies = (id: number) => (view?.comments ?? []).filter((c) => c.parentId === id).sort((a, b) => a.id - b.id);
    const t = roots.map((root) => ({ root, replies: replies(root.id) }));
    const order = new Map(blocks.map((b, i) => [b.id, i]));
    return {
      current: t.filter((x) => order.has(x.root.block)).sort((a, b) => (order.get(a.root.block) ?? 0) - (order.get(b.root.block) ?? 0)),
      earlier: t.filter((x) => !order.has(x.root.block)),
    };
  }, [view, blocks]);

  const counts = useMemo(() => {
    const m: Record<string, number> = {};
    for (const t of threads.current) m[t.root.block] = (m[t.root.block] ?? 0) + 1;
    return m;
  }, [threads]);

  if (spaceErr || viewErr) return <Problem err={(spaceErr ?? viewErr) as ReadError} onRetry={() => { setAttempt((n) => n + 1); void loadReport(); }} />;
  if (!space) return <main className={s.center}><p className={s.quiet}>Loading…</p></main>;
  if (space.reports.length === 0) return <main className={s.center}><h1 className={s.calmTitle}>{space.title}</h1><p className={s.quiet}>Nothing has been shared here yet.</p></main>;

  const latest = space.reports.find((r) => r.key === key)?.latest ?? 0;
  const old = rev && latest && rev.number < latest;
  const selText = selected ? quote(blockText(rev?.blocks.find((b) => b.id === selected) ?? { id: "", kind: "text" })) : "";
  const stale = space.lastSync && Date.now() - new Date(space.lastSync).getTime() > STALE_MS;
  const nComments = threads.current.length + threads.earlier.length;

  async function send(body: string, block: string, parentId?: number) {
    if (!rev) return;
    await postComment(token, { report: rev.report, revision: rev.id, block, ...(parentId ? { parentId } : {}), body });
    await loadReport();
  }

  return (
    <div className={s.app} data-panel={panel ? "open" : "closed"}>
      <aside className={s.side} data-open={drawer ? "true" : undefined}>
        <div className={s.sideHead}>
          <p className={s.spaceTitle}>{space.title}</p>
          <button type="button" className={s.reportsBtn} onClick={() => setDrawer(!drawer)} aria-expanded={drawer}>
            <Icon icon={drawer ? X : Menu} />
            Reports
          </button>
        </div>
        <nav className={s.nav} aria-label="Reports">
          <ul>
            {space.reports.map((r) => (
              <li key={r.key}>
                <a href={`#r=${encodeURIComponent(r.key)}`} className={s.item} aria-current={r.key === key ? "page" : undefined} onClick={(e) => { e.preventDefault(); open(r.key); }}>
                  <span className={s.itemTitle}>{r.title}</span>
                  <span className={s.itemDate}>{shortDate(r.publishedAt)}</span>
                </a>
              </li>
            ))}
          </ul>
        </nav>
        <div className={s.sideFoot}>
          <p>Reading as {space.reader}</p>
          <p>Figures are pictures; this link shows nothing else.</p>
        </div>
      </aside>

      <main className={s.main}>
        <div className={s.top}>
          <button type="button" className={s.commentsBtn} aria-pressed={panel} onClick={() => setPanel(!panel)}>
            <Icon icon={MessageSquare} />
            Comments {nComments}
          </button>
          <IconButton icon={theme === "dark" ? Sun : Moon} label={theme === "dark" ? "Light theme" : "Dark theme"} pressed={theme === "dark"} onClick={() => setTheme(theme === "dark" ? "light" : "dark")} />
        </div>
        {!rev ? <p className={s.quiet} role="status">Loading…</p> : (
          <>
            {old ? (
              <p className={s.notice} role="status">
                You are reading revision {rev.number} of {latest}. <button type="button" className={s.link} onClick={() => open(rev.report)}>Read the latest</button>
              </p>
            ) : null}
            <Document
              title={rev.title}
              meta={
                <>
                  <span className={s.revWrap}>
                    {(view?.revisions.length ?? 0) > 1 ? (
                      <button type="button" className={s.revBtn} aria-expanded={revMenu} onClick={() => setRevMenu(!revMenu)}>
                        Revision {rev.number} <Icon icon={ChevronDown} />
                      </button>
                    ) : <span>Revision {rev.number}</span>}
                    {revMenu ? (
                      <ul className={s.revMenu} aria-label="Revisions">
                        {[...(view?.revisions ?? [])].sort((a, b) => b.number - a.number).map((r) => (
                          <li key={r.number}>
                            <button type="button" className={s.revItem} aria-current={r.number === rev.number ? "true" : undefined} onClick={() => open(rev.report, r.number === latest ? 0 : r.number)}>
                              <span>Revision {r.number}{r.number === latest ? " (latest)" : ""}</span>
                              <span className={s.itemDate}>{shortDate(r.publishedAt)}</span>
                              {r.number === rev.number ? <Icon icon={Check} /> : null}
                            </button>
                          </li>
                        ))}
                      </ul>
                    ) : null}
                  </span>
                  <span>{longDate(rev.publishedAt)}</span>
                  {rev.authors ? <span>{rev.authors}</span> : null}
                </>
              }
              blocks={blocks}
              env={env}
              counts={counts}
              activeBlock={selected}
              onComment={(id) => { setSelected(id); setPanel(true); }}
              commentLabel="Comment"
              onOpenLink={(k) => open(k)}
            />
          </>
        )}
      </main>

      {panel ? (
        <CommentsPanel
          threads={threads}
          selected={selected}
          selText={selText}
          revBlocks={rev?.blocks ?? []}
          stale={!!stale}
          lastSync={space.lastSync}
          onClose={() => setPanel(false)}
          onSend={send}
        />
      ) : null}
    </div>
  );
}

type Thread = { root: ReaderComment; replies: ReaderComment[] };

function CommentsPanel({ threads, selected, selText, revBlocks, stale, lastSync, onClose, onSend }: {
  threads: { current: Thread[]; earlier: Thread[] };
  selected: string | null;
  selText: string;
  revBlocks: ReaderRevision["blocks"];
  stale: boolean;
  lastSync?: string;
  onClose: () => void;
  onSend: (body: string, block: string, parentId?: number) => Promise<void>;
}) {
  const head = useRef<HTMLHeadingElement>(null);
  useEffect(() => head.current?.focus(), []);
  const hasThread = threads.current.some((t) => t.root.block === selected);
  const text = (id: string) => quote(blockText(revBlocks.find((b) => b.id === id) ?? { id, kind: "text" }));
  const total = threads.current.length + threads.earlier.length;
  return (
    <aside className={s.panel} aria-label="Comments">
      <div className={s.panelHead}>
        <h2 className={s.panelTitle} tabIndex={-1} ref={head}>Comments</h2>
        <IconButton icon={X} label="Close comments" onClick={onClose} />
      </div>
      {selected && !hasThread ? <Composer label={`Comment on: ${selText}`} action="Comment" onSend={(b) => onSend(b, selected)} /> : null}
      {selected && !hasThread && stale && lastSync ? <p className={s.sync}>The author's workspace last synced {relative(lastSync)}; your comment will reach them at the next sync.</p> : null}
      {total === 0 && !selected ? <p className={s.quiet}>No comments yet. Use the comment button beside a paragraph to start one.</p> : null}
      <ul className={s.threads}>
        {threads.current.map((t) => <ThreadView key={t.root.id} t={t} excerpt={text(t.root.block)} active={t.root.block === selected} onSend={onSend} />)}
      </ul>
      {threads.earlier.length ? (
        <>
          <h3 className={s.sub}>On earlier revisions</h3>
          <ul className={s.threads}>
            {threads.earlier.map((t) => <ThreadView key={t.root.id} t={t} excerpt="this paragraph changed since" active={false} onSend={onSend} />)}
          </ul>
        </>
      ) : null}
    </aside>
  );
}

function ThreadView({ t, excerpt, active, onSend }: { t: Thread; excerpt: string; active: boolean; onSend: (body: string, block: string, parentId?: number) => Promise<void> }) {
  return (
    <li className={s.thread} data-active={active ? "true" : undefined}>
      <p className={s.excerpt}>{excerpt}</p>
      {[t.root, ...t.replies].map((c) => (
        <div key={c.id} className={s.msg}>
          <p className={s.msgHead}><strong>{c.author}</strong> <span>{relative(c.createdAt)}</span></p>
          <p className={s.msgBody}>{c.body}</p>
        </div>
      ))}
      {t.root.resolvedAt ? <p className={s.resolved}>Resolved</p> : null}
      <Composer label="Reply" action="Reply" compact onSend={(b) => onSend(b, t.root.block, t.root.id)} />
    </li>
  );
}

function Composer({ label, action, compact, onSend }: { label: string; action: string; compact?: boolean; onSend: (body: string) => Promise<void> }) {
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit(e?: FormEvent) {
    e?.preventDefault();
    const b = body.trim();
    if (!b || busy) return;
    setBusy(true);
    try {
      await onSend(b);
      setBody("");
      setErr(null);
    } catch (x) {
      setErr(x instanceof ReadError && x.kind === "limited" ? "Too many attempts; try again in a minute." : "Your comment was not sent. Try again.");
    } finally {
      setBusy(false);
    }
  }
  const area = (
    <textarea
      className={s.area}
      rows={compact ? 2 : 3}
      maxLength={4000}
      value={body}
      placeholder={compact ? "Reply…" : undefined}
      aria-label={compact ? label : undefined}
      onChange={(e) => setBody(e.target.value)}
      onKeyDown={(e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void submit(); }}
    />
  );
  return (
    <form className={s.composer} onSubmit={submit}>
      {compact ? area : <label className={s.composeLabel}>{label}{area}</label>}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {body.trim() || !compact ? <div className={s.actions}><Button type="submit" variant="primary" disabled={!body.trim() || busy}>{action}</Button></div> : null}
    </form>
  );
}

function Problem({ err, onRetry }: { err: ReadError; onRetry: () => void }) {
  if (err.kind === "notfound") {
    return (
      <main className={s.center}>
        <h1 className={s.calmTitle}>This link does not open anything.</h1>
        <p className={s.quiet}>It may have been revoked or mistyped. Ask the person who sent it for a new one.</p>
      </main>
    );
  }
  if (err.kind === "limited") return <main className={s.center}><p className={s.quiet} role="alert">Too many attempts from this network; try again in a minute.</p></main>;
  return (
    <main className={s.center}>
      <p className={s.quiet} role="alert">{err.kind === "network" ? "Could not reach the server." : err.message}</p>
      <Button onClick={onRetry}>Retry</Button>
    </main>
  );
}
