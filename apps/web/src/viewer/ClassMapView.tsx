import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, MouseEvent } from "react";
import { lut } from "./colormaps";
import s from "./GaussianReview.module.css";

const num = (v: number) => String(Number(v.toPrecision(4)));

/** The log10 range of the values above zero in a class map, or null when there are none. */
export function positiveRange(data: ArrayLike<number>): { lo: number; hi: number } | null {
  let lo = Infinity;
  let hi = -Infinity;
  for (let i = 0; i < data.length; i++) {
    const x = data[i]!;
    if (x > 0 && Number.isFinite(x)) {
      const l = Math.log10(x);
      if (l < lo) lo = l;
      if (l > hi) hi = l;
    }
  }
  return Number.isFinite(lo) ? { lo, hi } : null;
}

export type Counts = { positive: number; zero: number; nan: number };

/** How many pixels hold a value above zero, exactly zero (valid components, none in this class) and NaN (no valid component). */
export function countPixels(data: ArrayLike<number>): Counts {
  const c = { positive: 0, zero: 0, nan: 0 };
  for (let i = 0; i < data.length; i++) {
    const x = data[i]!;
    if (Number.isNaN(x)) c.nan++;
    else if (x > 0) c.positive++;
    else c.zero++;
  }
  return c;
}

type Props = {
  title: string;
  unit: string;
  width: number;
  height: number;
  /** Row 0 is the bottom row of the map, the way the viewer's planes are stored. */
  data: Float32Array;
  /** One byte per pixel in the same order; 1 marks the pixels behind the selected diagram cell. */
  mask: Uint8Array | null;
  pinned: { x: number; y: number } | null;
  onPick: (x: number, y: number) => void;
};

/**
 * One width-class map on a log scale. NaN (no valid component there) stays the page background, zero (components there, none in this
 * class) is a flat dark grey, and the pixels of a selected diagram cell carry stripes drawn over the values, so the highlight is a pattern and not a colour.
 * Click, or arrow keys and Enter, pin a pixel in the viewer.
 */
export function ClassMapView({ title, unit, width, height, data, mask, pinned, onPick }: Props) {
  const ref = useRef<HTMLCanvasElement>(null);
  const over = useRef<HTMLCanvasElement>(null);
  const [cursor, setCursor] = useState<{ x: number; y: number }>({ x: 0, y: 0 });
  const range = useMemo(() => positiveRange(data), [data]);
  const counts = useMemo(() => countPixels(data), [data]);
  const map = useMemo(() => lut("viridis"), []);

  useEffect(() => {
    const cv = ref.current;
    const g = cv?.getContext("2d");
    if (!cv || !g) return;
    const img = g.createImageData(width, height);
    for (let y = 0; y < height; y++) {
      for (let x = 0; x < width; x++) {
        const at = y * width + x;
        const o = ((height - 1 - y) * width + x) * 4;
        const v = data[at]!;
        let rgb: [number, number, number] | null = null;
        if (Number.isNaN(v)) rgb = null;
        else if (v > 0 && range) {
          const t = range.hi === range.lo ? 0.5 : (Math.log10(v) - range.lo) / (range.hi - range.lo);
          const i = Math.round(Math.min(1, Math.max(0, t)) * 255) * 3;
          rgb = [map[i]!, map[i + 1]!, map[i + 2]!];
        } else rgb = [58, 58, 62];
        if (rgb) {
          img.data[o] = rgb[0];
          img.data[o + 1] = rgb[1];
          img.data[o + 2] = rgb[2];
          img.data[o + 3] = 255;
        }
      }
    }
    g.putImageData(img, 0, 0);
  }, [data, range, map, width, height]);

  // The highlight and the pin are drawn at screen resolution over the data (never into it), so the values stay visible under the
  // stripes and a marker keeps its size whether the crop is 6 pixels wide or 2000.
  const [box, setBox] = useState<{ w: number; h: number }>({ w: 0, h: 0 });
  useEffect(() => {
    const cv = ref.current;
    if (!cv || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setBox({ w: cv.clientWidth, h: cv.clientHeight }));
    ro.observe(cv);
    return () => ro.disconnect();
  }, []);
  useEffect(() => {
    const ov = over.current;
    const g = ov?.getContext("2d");
    if (!ov || !g || box.w === 0) return;
    const dpr = window.devicePixelRatio || 1;
    ov.width = Math.round(box.w * dpr);
    ov.height = Math.round(box.h * dpr);
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, box.w, box.h);
    const sx = box.w / width;
    const sy = box.h / height;
    if (mask) {
      const area = new Path2D();
      for (let y = 0; y < height; y++) {
        let x = 0;
        while (x < width) {
          if (!mask[y * width + x]) { x++; continue; }
          const x0 = x;
          while (x < width && mask[y * width + x]) x++;
          area.rect(x0 * sx, (height - 1 - y) * sy, (x - x0) * sx, sy);   // one rectangle per run of marked pixels in a row
        }
      }
      g.save();
      g.clip(area);
      g.lineWidth = 3;
      g.strokeStyle = "rgba(0, 0, 0, 0.85)";
      g.beginPath();
      for (let d = -box.h; d < box.w; d += 7) { g.moveTo(d, box.h); g.lineTo(d + box.h, 0); }
      g.stroke();
      g.lineWidth = 1.25;
      g.strokeStyle = "rgba(255, 255, 255, 0.95)";
      g.stroke();
      g.restore();
    }
    if (pinned && pinned.x < width && pinned.y < height) {
      const cx = (pinned.x + 0.5) * sx;
      const cy = (height - 1 - pinned.y + 0.5) * sy;
      const r = Math.max(5, Math.min(sx, sy) / 2 + 2);
      g.lineWidth = 3.5;
      g.strokeStyle = "rgba(255, 255, 255, 0.95)";
      g.strokeRect(cx - r, cy - r, 2 * r, 2 * r);
      g.lineWidth = 1.75;
      g.strokeStyle = "#ff3b30";
      g.strokeRect(cx - r, cy - r, 2 * r, 2 * r);
    }
  }, [mask, pinned, box, width, height]);

  const pick = (e: MouseEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return;
    const x = Math.min(width - 1, Math.max(0, Math.floor(((e.clientX - r.left) / r.width) * width)));
    const y = Math.min(height - 1, Math.max(0, height - 1 - Math.floor(((e.clientY - r.top) / r.height) * height)));
    setCursor({ x, y });
    onPick(x, y);
  };
  const key = (e: KeyboardEvent<HTMLCanvasElement>) => {
    const d: Record<string, [number, number]> = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowUp: [0, 1], ArrowDown: [0, -1] };
    const st = d[e.key];
    if (st) {
      e.preventDefault();
      setCursor((c) => ({ x: Math.min(width - 1, Math.max(0, c.x + st[0])), y: Math.min(height - 1, Math.max(0, c.y + st[1])) }));
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onPick(cursor.x, cursor.y);
    }
  };

  return (
    <figure className={s.map}>
      <figcaption>{title}</figcaption>
      <div className={s.stack}>
      <canvas
        ref={ref}
        width={width}
        height={height}
        className={s.canvas}
        style={{ aspectRatio: `${width} / ${height}` }}
        tabIndex={0}
        role="application"
        aria-label={`${title}. ${width} by ${height} pixels. Click, or arrow keys and Enter, to open the pixel's spectrum with its components. Cursor at pixel ${cursor.x + 1}, ${cursor.y + 1}.`}
        onClick={pick}
        onKeyDown={key}
      />
      <canvas ref={over} className={s.overlay} aria-hidden="true" />
      </div>
      <p className={s.note}>
        {range ? `log scale, ${num(10 ** range.lo)} to ${num(10 ** range.hi)} ${unit}` : `no pixel above zero (${unit})`}. Grey: components there, none in this class
        ({counts.zero.toLocaleString("en-US")} pixels). Blank: no valid component ({counts.nan.toLocaleString("en-US")}).{mask ? " Stripes: pixels of the selected cell." : ""}
      </p>
    </figure>
  );
}
