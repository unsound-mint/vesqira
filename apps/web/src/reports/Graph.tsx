import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";
import { Focus, List, Minus, Plus } from "lucide-react";
import type { Graph as GraphData, GraphNode } from "../api/reports";
import { Checkbox, Icon, IconButton } from "../ui";
import { layoutGraph, nearestInDirection } from "./graphLayout";
import s from "./Graph.module.css";

const PAD = 48; // vertical margin
const PAD_X = 110; // horizontal margin: report labels are centred under their node and need room
const MIN_K = 0.4;
const MAX_K = 4;
const LABEL_ZOOM = 1.6;
const FALLBACK = { w: 720, h: 520 };

const KIND_WORD = { report: "Report", run: "Run", figure: "Figure" } as const;
const clip = (t: string) => (t.length > 28 ? `${t.slice(0, 27)}…` : t);
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
const nameOf = (n: GraphNode) => (n.missing ? `${n.label} (missing)` : n.label);

type View = { k: number; x: number; y: number };
const HOME: View = { k: 1, x: 0, y: 0 };

export function Graph({ graph, current, onOpen }: { graph: GraphData; current?: string | null; onOpen: (node: GraphNode) => void }) {
  const [showRuns, setShowRuns] = useState(true);
  const [showFigures, setShowFigures] = useState(true);
  const [showUnlinked, setShowUnlinked] = useState(false);
  const [listOpen, setListOpen] = useState(false);
  const [view, setView] = useState<View>(HOME);
  const [size, setSize] = useState(FALLBACK);
  const [hover, setHover] = useState<string | null>(null);
  const [focus, setFocus] = useState<string | null>(null);
  const stage = useRef<HTMLDivElement>(null);
  const svg = useRef<SVGSVGElement>(null);
  const nodeEls = useRef(new Map<string, SVGGElement>());
  const drag = useRef<{ x: number; y: number; moved: boolean } | null>(null);

  const degree = useMemo(() => {
    const d = new Map<string, number>();
    for (const e of graph.edges) {
      d.set(e.from, (d.get(e.from) ?? 0) + 1);
      d.set(e.to, (d.get(e.to) ?? 0) + 1);
    }
    return d;
  }, [graph]);
  // Reports that link nothing are left out by default: they are noise in a picture of links, and the list keeps them.
  const placed = useMemo(() => graph.nodes.filter((n) => showUnlinked || (degree.get(n.id) ?? 0) > 0 || n.id === current), [graph, degree, showUnlinked, current]);
  const unlinked = graph.nodes.length - graph.nodes.filter((n) => (degree.get(n.id) ?? 0) > 0 || n.id === current).length;
  // Positions come from the placed nodes, so the run and figure filters never move anything.
  const unit = useMemo(() => layoutGraph(placed, graph.edges), [placed, graph.edges]);
  const visible = useMemo(
    () => placed.filter((n) => (n.kind === "run" ? showRuns : n.kind === "figure" ? showFigures : true)),
    [placed, showRuns, showFigures],
  );
  const shown = useMemo(() => new Set(visible.map((n) => n.id)), [visible]);
  const edges = useMemo(() => graph.edges.filter((e) => shown.has(e.from) && shown.has(e.to)), [graph, shown]);
  const neighbours = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const e of edges) {
      (m.get(e.from) ?? m.set(e.from, new Set()).get(e.from)!).add(e.to);
      (m.get(e.to) ?? m.set(e.to, new Set()).get(e.to)!).add(e.from);
    }
    return m;
  }, [edges]);

  useEffect(() => {
    const el = stage.current;
    if (!el) return;
    const read = () => {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) setSize({ w: r.width, h: r.height });
    };
    read();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(read);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const zoomAt = useCallback((factor: number, cx: number, cy: number) => {
    setView((v) => {
      const k = clamp(v.k * factor, MIN_K, MAX_K);
      const f = k / v.k;
      return { k, x: cx - (cx - v.x) * f, y: cy - (cy - v.y) * f };
    });
  }, []);

  useEffect(() => {
    const el = svg.current;
    if (!el) return;
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      zoomAt(Math.exp(-e.deltaY * 0.0015), e.clientX - r.left, e.clientY - r.top);
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  }, [zoomAt]);

  const pos = useMemo(() => {
    const out: Record<string, { x: number; y: number }> = {};
    for (const n of visible) {
      const u = unit[n.id] ?? { x: 0.5, y: 0.5 };
      out[n.id] = {
        x: (PAD_X + u.x * Math.max(1, size.w - 2 * PAD_X)) * view.k + view.x,
        y: (PAD + u.y * Math.max(1, size.h - 2 * PAD)) * view.k + view.y,
      };
    }
    return out;
  }, [visible, unit, size, view]);

  const active = hover ?? focus;
  const near = active ? neighbours.get(active) : undefined;
  const isNear = (id: string) => id === active || !!near?.has(id);

  const onKey = (e: KeyboardEvent, n: GraphNode) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onOpen(n);
      return;
    }
    const dir = ({ ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" } as const)[e.key as "ArrowLeft"];
    if (!dir) return;
    e.preventDefault();
    const next = nearestInDirection(n.id, pos, dir);
    if (next) nodeEls.current.get(next)?.focus();
  };

  const down = (e: PointerEvent<SVGSVGElement>) => {
    if (e.target !== e.currentTarget && !(e.target as Element).hasAttribute("data-bg")) return;
    drag.current = { x: e.clientX, y: e.clientY, moved: false };
    e.currentTarget.setPointerCapture?.(e.pointerId);
  };
  const move = (e: PointerEvent<SVGSVGElement>) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    const dy = e.clientY - d.y;
    d.x = e.clientX;
    d.y = e.clientY;
    d.moved = true;
    setView((v) => ({ ...v, x: v.x + dx, y: v.y + dy }));
  };
  const up = () => {
    drag.current = null;
  };

  const mid = { x: size.w / 2, y: size.h / 2 };

  if (graph.nodes.length === 0) {
    return (
      <div className={s.empty}>
        <p>No links yet. Link reports with [[Title]], embed figures with ![[figure:…]] and runs with [[run:…]].</p>
      </div>
    );
  }

  const groups = (["report", "run", "figure"] as const).map((kind) => ({
    kind,
    title: kind === "report" ? "Reports" : kind === "run" ? "Runs" : "Figures",
    items: graph.nodes.filter((n) => n.kind === kind && (kind === "run" ? showRuns : kind === "figure" ? showFigures : true)), // the list keeps unlinked reports
  }));
  // Labels are placed greedily, most important first (the current report, then the most linked); a label that would
  // overlap one already placed waits for hover or focus. Widths are estimated from the 12–13px label font.
  const labelSet = new Set<string>();
  {
    const boxes: { x0: number; x1: number; y0: number; y1: number }[] = [];
    const order = [...visible].sort((a, b) => Number(b.id === current) - Number(a.id === current) || (degree.get(b.id) ?? 0) - (degree.get(a.id) ?? 0));
    for (const n of order) {
      if (n.kind !== "report" && view.k < LABEL_ZOOM) continue;
      const p = pos[n.id]!;
      const w = Math.min(clip(nameOf(n)).length, 30) * 7 + 8;
      const box = { x0: p.x - w / 2, x1: p.x + w / 2, y0: p.y + 8, y1: p.y + 28 };
      if (boxes.some((b) => box.x0 < b.x1 && box.x1 > b.x0 && box.y0 < b.y1 && box.y1 > b.y0)) continue;
      boxes.push(box);
      labelSet.add(n.id);
    }
  }
  const labelled = (n: GraphNode) => labelSet.has(n.id) || (!!active && isNear(n.id));
  const aria = (n: GraphNode) => {
    const c = degree.get(n.id) ?? 0;
    return `${KIND_WORD[n.kind]}: ${nameOf(n)}, ${c} ${c === 1 ? "link" : "links"}`;
  };

  return (
    <div className={s.root}>
      <div className={s.main}>
        <div className={s.bar}>
          <span className={s.show}>Show</span>
          <span className={s.fixed}>Reports</span>
          <Checkbox label="Runs" checked={showRuns} onChange={(e) => setShowRuns(e.target.checked)} />
          <Checkbox label="Figures" checked={showFigures} onChange={(e) => setShowFigures(e.target.checked)} />
          {unlinked > 0 ? <Checkbox label={`Unlinked reports (${unlinked})`} checked={showUnlinked} onChange={(e) => setShowUnlinked(e.target.checked)} /> : null}
          <span className={s.spacer} />
          <span className={s.legend} aria-hidden="true">
            <i className={s.keyReport} /> report <i className={s.keyRun} /> run <i className={s.keyFigure} /> figure
          </span>
          <button type="button" className={s.listToggle} aria-expanded={listOpen} onClick={() => setListOpen((o) => !o)}>
            <Icon icon={List} /> List
          </button>
        </div>
        <div className={s.stage} ref={stage}>
          <svg
            ref={svg}
            className={s.svg}
            width={size.w}
            height={size.h}
            role="group"
            aria-label="Link graph"
            onPointerDown={down}
            onPointerMove={move}
            onPointerUp={up}
            onPointerCancel={up}
          >
            <rect data-bg="" className={s.bg} width={size.w} height={size.h} />
            <g className={s.edges}>
              {edges.map((e) => {
                const a = pos[e.from]!;
                const b = pos[e.to]!;
                const lit = !!active && (e.from === active || e.to === active);
                const cls = [e.kind === "link" ? s.link : s.embeds, lit ? s.lit : active ? s.dim : ""].join(" ");
                return <line key={`${e.from}>${e.to}`} className={cls} x1={a.x} y1={a.y} x2={b.x} y2={b.y} />;
              })}
            </g>
            <g>
              {visible.map((n) => {
                const p = pos[n.id]!;
                const isCur = n.id === current;
                const cls = [
                  s.node,
                  n.published ? s.published : n.kind === "report" ? s.draft : "",
                  n.missing ? s.missing : "",
                  isCur ? s.current : "",
                  active && !isNear(n.id) ? s.dim : "",
                ].join(" ");
                const r = n.kind === "report" ? (isCur ? 8 : 6) : 6;
                return (
                  <g
                    key={n.id}
                    ref={(el) => {
                      if (el) nodeEls.current.set(n.id, el);
                      else nodeEls.current.delete(n.id);
                    }}
                    className={cls}
                    transform={`translate(${p.x} ${p.y})`}
                    tabIndex={0}
                    role="button"
                    aria-label={aria(n)}
                    aria-current={isCur ? "true" : undefined}
                    data-kind={n.kind}
                    data-id={n.id}
                    onClick={() => onOpen(n)}
                    onKeyDown={(e) => onKey(e, n)}
                    onPointerEnter={() => setHover(n.id)}
                    onPointerLeave={() => setHover(null)}
                    onFocus={() => setFocus(n.id)}
                    onBlur={() => setFocus((f) => (f === n.id ? null : f))}
                  >
                    <title>{`${KIND_WORD[n.kind]}: ${nameOf(n)}${n.kind === "report" ? (n.published ? " (published)" : " (draft)") : ""}`}</title>
                    {isCur && <circle className={s.halo} r={r + 5} />}
                    {n.kind === "report" && <circle className={s.shape} r={r} />}
                    {n.kind === "run" && <rect className={s.shape} x={-r} y={-r} width={2 * r} height={2 * r} />}
                    {n.kind === "figure" && <polygon className={s.shape} points={`0,${-r - 2} ${r + 2},0 0,${r + 2} ${-r - 2},0`} />}
                    {labelled(n) && (
                      <text className={n.kind === "report" ? s.label : s.labelSmall} y={r + 16} textAnchor="middle">
                        {clip(nameOf(n))}
                      </text>
                    )}
                  </g>
                );
              })}
            </g>
          </svg>
          <div className={s.zoom}>
            <IconButton icon={Plus} label="Zoom in" onClick={() => zoomAt(1.3, mid.x, mid.y)} />
            <IconButton icon={Minus} label="Zoom out" onClick={() => zoomAt(1 / 1.3, mid.x, mid.y)} />
            <IconButton icon={Focus} label="Fit" onClick={() => setView(HOME)} />
          </div>
        </div>
      </div>
      <aside className={s.list} data-open={listOpen} aria-label="Nodes">
        {groups.map((g) => (
          <section key={g.kind} className={s.group}>
            <h3 className={s.groupHead}>
              <span>{g.title}</span>
              <span className={s.count}>{g.items.length}</span>
            </h3>
            <ul>
              {g.items.map((n) => {
                const c = degree.get(n.id) ?? 0;
                return (
                  <li key={n.id}>
                    <button
                      type="button"
                      className={s.item}
                      aria-current={n.id === current ? "true" : undefined}
                      onClick={() => onOpen(n)}
                      onPointerEnter={() => setHover(n.id)}
                      onPointerLeave={() => setHover(null)}
                    >
                      <span className={s.itemName} data-missing={n.missing || undefined}>{nameOf(n)}</span>
                      {n.id === current ? <span className={s.here}>this page</span> : <span className={s.count}>{c}</span>}
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        ))}
      </aside>
    </div>
  );
}
