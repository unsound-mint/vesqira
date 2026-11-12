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
 * class) is a flat dark grey, and the pixels of a selected diagram cell carry a checkerboard, so the highlight is a pattern and not a colour.
 * Click, or arrow keys and Enter, pin a pixel in the viewer.
 */
export function ClassMapView({ title, unit, width, height, data, mask, pinned, onPick }: Props) {
  const ref = useRef<HTMLCanvasElement>(null);
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
        if (mask && mask[at]) rgb = (x + y) & 1 ? [255, 255, 255] : [0, 0, 0];
        if (rgb) {
          img.data[o] = rgb[0];
          img.data[o + 1] = rgb[1];
          img.data[o + 2] = rgb[2];
          img.data[o + 3] = 255;
        }
      }
    }
    g.putImageData(img, 0, 0);
    const mark = (p: { x: number; y: number }, style: string) => {
      g.strokeStyle = style;
      g.lineWidth = Math.max(1, width / 160);
      g.strokeRect(p.x + 0.5 - 1.5, height - 1 - p.y + 0.5 - 1.5, 3, 3);
    };
    if (pinned && pinned.x < width && pinned.y < height) mark(pinned, "#ff3b30");
  }, [data, mask, pinned, range, map, width, height]);

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
      <p className={s.note}>
        {range ? `log scale, ${num(10 ** range.lo)} to ${num(10 ** range.hi)} ${unit}` : `no pixel above zero (${unit})`}. Grey: components there, none in this class
        ({counts.zero.toLocaleString("en-US")} pixels). Blank: no valid component ({counts.nan.toLocaleString("en-US")}).{mask ? " Checkerboard: pixels of the selected cell." : ""}
      </p>
    </figure>
  );
}
