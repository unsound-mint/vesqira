import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, MouseEvent } from "react";
import { Button } from "../ui";
import { lut } from "./colormaps";
import { fit, pixelAt } from "./geometry";
import { limits, outputSize, renderPlane } from "./render";
import type { Rgb } from "./render";
import type { Viewer } from "./useViewer";
import { channelLabel } from "./format";
import s from "./PlaneViewer.module.css";

function token(name: string, fallback: Rgb): Rgb {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const m = /^#([0-9a-f]{6})$/i.exec(raw);
  return m ? [parseInt(m[1]!.slice(0, 2), 16), parseInt(m[1]!.slice(2, 4), 16), parseInt(m[1]!.slice(4, 6), 16)] : fallback;
}

/** The plane on screen: native aspect, square pixels, sharp edges, north up, "no value" pixels hatched. */
export function PlaneViewer({ v }: { v: Viewer }) {
  const box = useRef<HTMLDivElement>(null);
  const cv = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const { plane, view } = v;

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const measure = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const shape = plane ? fit(size.w, size.h, plane.head.width, plane.head.height) : { width: 0, height: 0, scale: 0 };

  useEffect(() => {
    const canvas = cv.current;
    if (!canvas || !plane || shape.scale <= 0) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const { outW, outH } = outputSize(plane.head.width, plane.head.height, shape.scale);
    const img = renderPlane(plane.data, plane.head.width, plane.head.height, {
      lut: lut(v.colormap),
      range: limits(plane.head.stats, v.range),
      stretch: v.stretch,
      outW,
      outH,
      nodata: [token("--nodata-a", [27, 34, 44]), token("--nodata-b", [85, 98, 117])],
    });
    canvas.width = img.width;
    canvas.height = img.height;
    ctx.putImageData(new ImageData(img.pixels, img.width, img.height), 0, 0);
  }, [plane, v.colormap, v.stretch, v.range, shape.scale]);

  function pick(e: MouseEvent<HTMLCanvasElement>) {
    if (!plane) return null;
    const r = e.currentTarget.getBoundingClientRect();
    return pixelAt(e.clientX - r.left, e.clientY - r.top, r.width, r.height, plane.head.width, plane.head.height);
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Escape" && v.pinned) {
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

  const probe = v.probe;
  const cell = plane ? shape.width / plane.head.width : 0;
  const mark = Math.max(cell, 10);
  const allNoValue = plane !== null && plane.head.stats.finite_count === 0;

  return (
    <div
      className={s.root}
      ref={box}
      tabIndex={0}
      role="group"
      aria-label="Plane viewer"
      aria-keyshortcuts="ArrowLeft ArrowRight PageUp PageDown Home End Escape"
      onKeyDown={onKeyDown}
    >
      {body}
      {plane && shape.width > 0 ? (
        <div className={s.frame} style={{ width: shape.width, height: shape.height }}>
          <canvas
            ref={cv}
            className={s.image}
            role="img"
            aria-label={`${channelLabel(plane.head.channel, plane.head.channels, view?.spectral ?? null, plane.head.spectral_value)}, ${plane.head.width} by ${plane.head.height} pixels`}
            style={{ width: shape.width, height: shape.height }}
            onMouseMove={(e) => v.setHover(pick(e))}
            onMouseLeave={() => v.setHover(null)}
            onClick={(e) => {
              const p = pick(e);
              v.setPinned(p && v.pinned && v.pinned.x === p.x && v.pinned.y === p.y ? null : p);
            }}
          />
          {probe && probe.x < plane.head.width && probe.y < plane.head.height ? (
            <span
              className={v.pinned ? s.pinned : s.marker}
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
        <p className={s.caption} aria-live="polite">
          {channelLabel(plane.head.channel, plane.head.channels, view?.spectral ?? null, plane.head.spectral_value)}
          {v.loading && v.channel !== plane.head.channel ? ` · loading ${v.channel + 1}…` : ""}
        </p>
      ) : null}
      {allNoValue ? <p className={s.warn} role="status">Every pixel in this plane has no value (NaN or BLANK).</p> : null}
    </div>
  );
}

function Problem({ text, onRetry }: { text: string; onRetry: () => void }) {
  return (
    <div className={s.problem} role="alert">
      <p>{text}</p>
      <Button onClick={onRetry}>Try again</Button>
    </div>
  );
}
