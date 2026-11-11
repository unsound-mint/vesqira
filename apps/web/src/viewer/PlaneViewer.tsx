import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, MouseEvent } from "react";
import { ZoomIn, ZoomOut, Maximize2 } from "lucide-react";
import { Button, IconButton } from "../ui";
import { lut } from "./colormaps";
import { pixelAt } from "./geometry";
import { limits, outputSize, renderPlane } from "./render";
import { GlPlaneRenderer, cpuNote, forcedCpu, gpuSupported, setRendererNote } from "./glRenderer";
import type { Rgb } from "./render";
import type { Viewer } from "./useViewer";
import { channelLabel } from "./format";
import { fromDrag, lineFromDrag, lineValid } from "./region";
import type { Pt } from "./region";
import type { LineShape, RegionShape } from "../api/planes";
import { dashOf, LABEL_PX, lineMarks, nearestMarker, sceneMarks } from "../scene/overlays";
import { placeLabels } from "../scene/labelPlacement";
import type { Box, PlacedLabel } from "../scene/labelPlacement";
import { SourcePopover } from "./SourceRow";
import type { Mark } from "../scene/overlays";
import { dragCentre, placement } from "../scene/view";
import s from "./PlaneViewer.module.css";

function token(name: string, fallback: Rgb): Rgb {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const m = /^#([0-9a-f]{6})$/i.exec(raw);
  return m ? [parseInt(m[1]!.slice(0, 2), 16), parseInt(m[1]!.slice(2, 4), 16), parseInt(m[1]!.slice(4, 6), 16)] : fallback;
}

/** The plane on screen: native aspect, square pixels, sharp edges, north up, "no value" pixels hatched. */
export function PlaneViewer({ v, onProbe, onDrawn, onSize }: { v: Viewer; onProbe?: (p: Pt | null) => void; onDrawn?: (plane: NonNullable<Viewer["plane"]>) => void; onSize?: (size: { w: number; h: number }) => void }) {
  const box = useRef<HTMLDivElement>(null);
  const frameEl = useRef<HTMLDivElement>(null);
  const chromeEls = useRef<(HTMLElement | null)[]>([]); // the zoom bar, the channel caption and the warning: drawn over the picture, so labels keep clear of them
  const [chrome, setChrome] = useState<Box[]>([]);
  const cv = v.canvas;
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [draft, setDraft] = useState<RegionShape | null>(null);
  const [lineDraft, setLineDraft] = useState<LineShape | null>(null);
  const extend = useRef(false); // Shift held when the line gesture started: the new point is one more vertex of the path
  const anchor = useRef<Pt | null>(null);
  const { plane, view } = v;
  const [gpuOff, setGpuOff] = useState(false); // set when the GPU renderer could not start or its context was lost
  const gl = useRef<GlPlaneRenderer | null>(null);
  const drawn = useRef(onDrawn); // told when a plane has been put on the canvas, so a caller can save exactly what is shown
  drawn.current = onDrawn;
  const sized = useRef(onSize); // told the pane's size, so a linked pane can say which part of the sky it shows
  sized.current = onSize;

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const measure = () => {
      const next = { w: el.clientWidth, h: el.clientHeight };
      setSize(next);
      sized.current?.(next);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // the scene's view (an image window, independent of the screen) placed in this pane
  const sceneView = v.scene?.view ?? { zoom: 1 };
  const image = plane ? { w: plane.head.width, h: plane.head.height } : { w: 0, h: 0 };
  const shape = placement(sceneView, size, image);
  const moved = useRef(false);
  const panStart = useRef<{ x: number; y: number; ox: number; oy: number } | null>(null);

  // the wheel zooms; it has to be a native listener to stop the page scrolling
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const wheel = (e: WheelEvent) => {
      if (!plane) return;
      e.preventDefault();
      v.setZoom(v.zoom * (e.deltaY < 0 ? 1.2 : 1 / 1.2));
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  }, [plane, v.zoom]); // eslint-disable-line react-hooks/exhaustive-deps

  // A canvas keeps the kind of context it first gave out, so the GPU and CPU paths draw on different canvas elements.
  const outSize = plane && shape.scale > 0 ? outputSize(plane.head.width, plane.head.height, shape.scale) : null;
  const useGpu = !!plane && !!outSize && !gpuOff && GlPlaneRenderer.fits(plane.head.width, plane.head.height, outSize.outW, outSize.outH);
  const canvasKey = useGpu ? "gpu" : "cpu";

  useEffect(() => {
    const canvas = cv.current;
    if (!canvas || !plane || !outSize) return;
    const { outW, outH } = outSize;
    const opts = {
      lut: lut(v.interp ? "twilight" : v.colormap),
      range: v.interp ? null : limits(plane.head.stats, v.range, v.customLimits),
      axial: v.interp ? { toDegrees: v.interp.unit === "deg" ? 1 : 180 / Math.PI } : undefined,
      stretch: v.stretch,
      outW,
      outH,
      nodata: [token("--nodata-a", [27, 34, 44]), token("--nodata-b", [85, 98, 117])] as [Rgb, Rgb],
    };
    if (useGpu) {
      if (gl.current?.canvas !== canvas) {
        gl.current?.dispose();
        gl.current = GlPlaneRenderer.create(canvas, () => setGpuOff(true));
        if (!gl.current) { setGpuOff(true); return; }
      }
      gl.current.draw(plane.data, plane.head.width, plane.head.height, opts);
      setRendererNote(null);
      drawn.current?.(plane);
      return;
    }
    gl.current?.dispose();
    gl.current = null;
    setRendererNote(cpuNote({ forced: forcedCpu(), supported: gpuSupported(), failed: gpuOff }));
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const img = renderPlane(plane.data, plane.head.width, plane.head.height, opts);
    canvas.width = img.width;
    canvas.height = img.height;
    ctx.putImageData(new ImageData(img.pixels, img.width, img.height), 0, 0);
    drawn.current?.(plane);
  }, [plane, v.colormap, v.stretch, v.range, v.customLimits, v.interp, shape.scale, useGpu, canvasKey]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => () => { gl.current?.dispose(); gl.current = null; setRendererNote(null); }, []);

  function pick(e: MouseEvent<HTMLCanvasElement>) {
    if (!plane) return null;
    const r = e.currentTarget.getBoundingClientRect();
    return pixelAt(e.clientX - r.left, e.clientY - r.top, r.width, r.height, plane.head.width, plane.head.height);
  }

  function commit() {
    if (draft) v.setRegion(draft);
    if (lineDraft && lineValid(lineDraft)) v.setPath(lineDraft);
    anchor.current = null;
    setDraft(null);
    setLineDraft(null);
  }

  function moveCursor(dx: number, dy: number) {
    if (!plane) return;
    const from = v.pinned ?? v.hover ?? { x: Math.floor(plane.head.width / 2), y: Math.floor(plane.head.height / 2) };
    v.setPinned({ x: Math.min(plane.head.width - 1, Math.max(0, from.x + dx)), y: Math.min(plane.head.height - 1, Math.max(0, from.y + dy)) });
  }

  function onKeyDown(e: KeyboardEvent) {
    const step = e.altKey ? 10 : 1;
    if (e.shiftKey && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(e.key)) {
      moveCursor(e.key === "ArrowLeft" ? -step : e.key === "ArrowRight" ? step : 0, e.key === "ArrowUp" ? step : e.key === "ArrowDown" ? -step : 0);
      e.preventDefault();
      return;
    }
    if (e.key === "+" || e.key === "=") {
      v.setZoom(v.zoom * 1.25);
      e.preventDefault();
      return;
    }
    if (e.key === "-") {
      v.setZoom(v.zoom / 1.25);
      e.preventDefault();
      return;
    }
    if (e.key === "0") {
      v.setZoom(1);
      e.preventDefault();
      return;
    }
    if (e.key === "Escape" && anchor.current) {
      anchor.current = null;
      setDraft(null);
      setLineDraft(null);
      e.preventDefault();
    } else if (e.key === "Escape" && v.picked) {
      v.setPicked(null);
      e.preventDefault();
    } else if (e.key === "Escape" && v.pinned) {
      v.setPinned(null);
      e.preventDefault();
    } else if (v.onChannelKey(e.key)) {
      e.preventDefault();
    }
  }

  let body: React.ReactNode = null;
  if (v.viewError) {
    body = <Problem text={v.viewError} onRetry={v.retry} />;
  } else if (!view) {
    body = <p className={s.note}>Reading the cube layout…</p>;
  } else if (v.planeError) {
    body = <Problem text={v.planeError} onRetry={v.retry} />;
  } else if (!plane) {
    body = <p className={s.note}>Loading channel {v.channel + 1}…</p>;
  }

  // every overlay, in scene order, through the overlay registry (the same marks a saved figure strokes)
  const marks = useMemo(
    () => {
      if (!v.scene || !plane) return [];
      const layers = sceneMarks(v.scene, { plane, scale: shape.scale, interp: v.interp, reliability: v.reliability, draft, geometry: v.geometry });
      // the position-velocity path and the line being drawn are pane gestures, not scene layers: on top of everything
      const h = plane.head.height;
      return [...layers, ...(v.path ? lineMarks(v.path.points, h, "region") : []), ...(lineDraft ? lineMarks(lineDraft.points, h, "draft") : [])];
    },
    [v.scene?.overlays, v.scene?.base.datasetId, plane, shape.scale, v.interp, v.reliability, draft, v.geometry, v.path, lineDraft], // eslint-disable-line react-hooks/exhaustive-deps
  );
  const labels = useMemo(() => marks.filter((m): m is Extract<Mark, { type: "text" }> => m.type === "text"), [marks]);
  const sources = useMemo(() => marks.filter((m): m is Extract<Mark, { type: "marker" }> => m.type === "marker"), [marks]);
  const pickedMark = v.picked ? sources.find((m) => m.layer === v.picked!.overlayId && m.row === v.picked!.row) ?? null : null;
  const pickedOverlay = v.picked ? v.scene?.overlays.find((o) => o.id === v.picked!.overlayId) : undefined;
  // automatic orientation spacing of other datasets' lines follows the zoom, so the viewer says what scale it draws at
  const setDrawScale = v.setDrawScale;
  useEffect(() => setDrawScale(shape.scale), [shape.scale, setDrawScale]);

  // the viewer's own controls in frame pixels, measured after layout so overlay labels never sit under them
  useLayoutEffect(() => {
    const f = frameEl.current?.getBoundingClientRect();
    if (!f) { setChrome((c) => (c.length ? [] : c)); return; }
    const next = chromeEls.current.flatMap((el) => {
      if (!el) return [];
      const r = el.getBoundingClientRect();
      return [{ x: r.left - f.left, y: r.top - f.top, w: r.width, h: r.height }];
    });
    setChrome((c) => (c.length === next.length && c.every((b, i) => b.x === next[i]!.x && b.y === next[i]!.y && b.w === next[i]!.w && b.h === next[i]!.h) ? c : next));
  });

  const probe = v.probe;
  const cell = plane ? shape.width / plane.head.width : 0;
  const mark = Math.max(cell, 10);
  const allNoValue = plane !== null && plane.head.stats.finite_count === 0;
  const placed = useMemo(() => placeLabels(labels, cell, { w: shape.width, h: shape.height }, chrome), [labels, cell, shape.width, shape.height, chrome]);

  return (
    <div
      className={s.root}
      ref={box}
      tabIndex={0}
      role="group"
      aria-label="Plane viewer"
      aria-keyshortcuts="ArrowLeft ArrowRight PageUp PageDown Home End Escape Shift+ArrowLeft Shift+ArrowRight Shift+ArrowUp Shift+ArrowDown + - 0"
      onKeyDown={onKeyDown}
    >
      {body}
      {plane && shape.width > 0 ? (
        <div className={s.frame} ref={frameEl} style={{ width: shape.width, height: shape.height, transform: `translate(${shape.offset.x}px, ${shape.offset.y}px)` }}>
          <canvas
            key={canvasKey}
            ref={cv}
            className={s.image}
            role="img"
            aria-label={`${channelLabel(plane.head.channel, plane.head.channels, view?.spectral ?? null, plane.head.spectral_value)}, ${plane.head.width} by ${plane.head.height} pixels`}
            style={{ width: shape.width, height: shape.height }}
            onMouseDown={(e) => {
              if (v.tool === "pointer") {
                moved.current = false;
                if (v.zoom > 1) panStart.current = { x: e.clientX, y: e.clientY, ox: shape.offset.x, oy: shape.offset.y };
                return;
              }
              const p = pick(e);
              if (p) {
                anchor.current = p;
                if (v.tool === "line") {
                  extend.current = e.shiftKey;
                  setLineDraft(lineFromDrag(p, p, v.path, e.shiftKey));
                } else setDraft(fromDrag(v.tool, p, p));
              }
            }}
            onMouseMove={(e) => {
              if (panStart.current) {
                const dx = e.clientX - panStart.current.x;
                const dy = e.clientY - panStart.current.y;
                if (Math.abs(dx) + Math.abs(dy) > 3) moved.current = true;
                const c = moved.current ? dragCentre({ x: panStart.current.ox, y: panStart.current.oy }, dx, dy, sceneView, size, image) : null;
                if (c) v.setCentre(c);
              }
              const p = pick(e);
              v.setHover(p);
              onProbe?.(p);
              if (anchor.current && p && v.tool === "line") setLineDraft(lineFromDrag(anchor.current, p, v.path, extend.current));
              else if (anchor.current && p && (v.tool === "rect" || v.tool === "circle")) setDraft(fromDrag(v.tool, anchor.current, p));
            }}
            onMouseUp={() => {
              panStart.current = null;
              commit();
            }}
            onMouseLeave={() => {
              v.setHover(null);
              onProbe?.(null);
              commit();
            }}
            onClick={(e) => {
              if (v.tool !== "pointer" || moved.current) return;
              const box = e.currentTarget.getBoundingClientRect();
              const per = box.width / plane.head.width; // screen pixels per image pixel
              const hit = per > 0 ? nearestMarker(marks, (e.clientX - box.left) / per, (e.clientY - box.top) / per, per) : null;
              if (hit) {
                v.setPicked({ overlayId: hit.layer, row: hit.row });
                return;
              }
              if (v.picked) v.setPicked(null);
              const p = pick(e);
              v.setPinned(p && v.pinned && v.pinned.x === p.x && v.pinned.y === p.y ? null : p);
            }}
          />
          <svg className={s.overlay} viewBox={`0 0 ${plane.head.width} ${plane.head.height}`} preserveAspectRatio="none" aria-hidden="true" data-testid="overlay-marks">
            <Marks marks={marks} />
          </svg>
          {labels.length || sources.length ? (
            // labels and catalog markers in screen pixels, so text and dots keep their size at every zoom
            <svg className={s.overlay} width={shape.width} height={shape.height} aria-hidden="true">
              <Labels labels={placed} />
              <Sources sources={sources} cell={cell} picked={pickedMark} />
            </svg>
          ) : null}
          {pickedMark && pickedOverlay ? (
            <SourcePopover datasetId={pickedOverlay.datasetId} row={pickedMark.row} name="Catalog source" at={{ x: pickedMark.x * cell, y: pickedMark.y * cell }} frame={{ w: shape.width, h: shape.height }} onClose={() => v.setPicked(null)} />
          ) : null}
          {probe && probe.x < plane.head.width && probe.y < plane.head.height ? (
            <span
              className={v.pinned ? s.pinned : v.hover ? s.marker : s.linked}
              aria-hidden="true"
              style={{
                width: mark,
                height: mark,
                left: (probe.x + 0.5) * cell - mark / 2,
                top: (plane.head.height - 1 - probe.y + 0.5) * cell - mark / 2,
              }}
            />
          ) : null}
        </div>
      ) : null}
      {plane ? (
        <p className={s.caption} aria-live="polite" ref={(el) => { chromeEls.current[1] = el; }}>
          {channelLabel(plane.head.channel, plane.head.channels, view?.spectral ?? null, plane.head.spectral_value)}
          {v.loading && v.channel !== plane.head.channel ? ` · loading ${v.channel + 1}…` : ""}
          {plane.head.preview ? ` · preview, reduced ${plane.head.preview.step}× — full resolution loading` : ""}
        </p>
      ) : null}
      {plane ? (
        <div className={s.zoomBar} role="group" aria-label="Zoom" ref={(el) => { chromeEls.current[0] = el; }}>
          <IconButton icon={ZoomOut} label="Zoom out" disabled={v.zoom <= 1} onClick={() => v.setZoom(v.zoom / 1.25)} />
          <IconButton icon={ZoomIn} label="Zoom in" disabled={v.zoom >= 16} onClick={() => v.setZoom(v.zoom * 1.25)} />
          <IconButton icon={Maximize2} label="Fit to the pane" disabled={v.zoom === 1} onClick={() => v.setZoom(1)} />
        </div>
      ) : null}
      {allNoValue ? <p className={s.warn} role="status" ref={(el) => { chromeEls.current[2] = el; }}>Every pixel in this plane has no value (NaN or BLANK).</p> : null}
    </div>
  );
}

const ink = (c: string) => `var(--overlay-${c})`;

/** The SVG side of the overlay registry: one element per mark, styled by role with the design tokens. */
function Marks({ marks }: { marks: Mark[] }) {
  return marks.map((m, i) => {
    if (m.type === "text" || m.type === "marker") return null; // drawn in screen pixels by Labels and Sources
    if (m.type === "line") {
      const st = m.stroke;
      return (
        <g key={i} opacity={m.opacity}>
          <line className={s.lineHalo} x1={m.x1} y1={m.y1} x2={m.x2} y2={m.y2} style={st ? { strokeWidth: st.width + 2 } : undefined} />
          <line className={s.lineTop} x1={m.x1} y1={m.y1} x2={m.x2} y2={m.y2} style={st ? { stroke: ink(st.color), strokeWidth: st.width } : undefined} />
        </g>
      );
    }
    if (m.type === "path") {
      const pts = m.points.join(" ");
      const El = m.closed ? "polygon" : "polyline";
      const dash = m.stroke.dashed ? dashOf(m.stroke.width).join(" ") : undefined;
      return (
        <g key={i} data-role={m.role}>
          {m.fill !== undefined ? <El points={pts} className={s.pathFill} style={{ fill: ink(m.stroke.color), fillOpacity: m.fill }} /> : null}
          <El points={pts} className={s.pathHalo} style={{ strokeWidth: m.stroke.width + 2, strokeDasharray: dash }} />
          <El points={pts} className={s.pathTop} style={{ stroke: ink(m.stroke.color), strokeWidth: m.stroke.width, strokeDasharray: dash }} />
        </g>
      );
    }
    const cls = m.role === "region" ? s.region : m.role === "saved" ? s.saved : s.draft;
    return m.type === "rect" ? <rect key={i} className={cls} x={m.x} y={m.y} width={m.w} height={m.h} /> : <circle key={i} className={cls} cx={m.cx} cy={m.cy} r={m.r} />;
  });
}

/** Catalog sources in screen pixels: a ring in the overlay colour, filled by the colour column's value (hollow when unknown); the picked one is doubled. */
function Sources({ sources, cell, picked }: { sources: Extract<Mark, { type: "marker" }>[]; cell: number; picked: Extract<Mark, { type: "marker" }> | null }) {
  return (
    <g data-role="catalog">
      {sources.map((m) => {
        const x = m.x * cell, y = m.y * cell;
        const on = picked !== null && picked.layer === m.layer && picked.row === m.row;
        return (
          <g key={`${m.layer}:${m.row}`} data-row={m.row}>
            <circle className={s.sourceHalo} cx={x} cy={y} r={m.r + 1 + (on ? 3 : 0)} style={{ strokeWidth: m.ringWidth + 2 }} />
            <circle className={s.source} cx={x} cy={y} r={m.r}
              style={{ fill: m.fill === "none" ? "none" : m.fill === "ink" ? ink(m.ring) : m.fill, stroke: ink(m.ring), strokeWidth: on ? m.ringWidth + 1 : m.ringWidth }} />
          </g>
        );
      })}
    </g>
  );
}

/** Overlay labels (grid coordinates, the beam size) where placeLabels put them, with a dark halo. */
function Labels({ labels }: { labels: PlacedLabel[] }) {
  return labels.map((m, i) => {
    const baseline = m.baseline === "top" ? "hanging" : m.baseline === "bottom" ? "text-after-edge" : "central";
    return (
      <text key={i} className={s.label} x={m.sx} y={m.sy} textAnchor={m.anchor} dominantBaseline={baseline} style={{ fill: ink(m.color), fontSize: LABEL_PX }} data-role={m.role}>
        {m.text}
      </text>
    );
  });
}

function Problem({ text, onRetry }: { text: string; onRetry: () => void }) {
  return (
    <div className={s.problem} role="alert">
      <p>{text}</p>
      <Button onClick={onRetry}>Try again</Button>
    </div>
  );
}
