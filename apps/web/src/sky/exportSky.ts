import { pathData, SIZE } from "./skyScene";
import type { SkyScene } from "./skyScene";
import type { Survey } from "./surveys";
import { download } from "../catalog/runExport";

/**
 * The saved sky image (ADR 0072). It is a file for the person, not a registered figure: it carries no scientific numbers and is
 * not pinned evidence. By default it holds the footprints on the plain grid only. The survey image is included only when the
 * person asks for it for this one file, and then the caption says what it is, in the picture itself: the survey name, its HiPS id,
 * its provider, its credit, and that it is context and not data from the person's files.
 */
export type SkyImage = { scene: SkyScene; lines: string[] };

const CANVAS = 720;
const CAPTION_LINE = 22;

/** The caption of a saved image: where it looks, and, only with the background, what the background is. */
export function captionOf(opts: { frame: string; centre: string; fov: string; selected: string | null; survey: Survey | null }): string[] {
  const lines = [`Sky context, ${opts.frame}${opts.selected ? `, selected: ${opts.selected}` : ""}`, `Centre ${opts.centre}, ${opts.fov} across. Outlines are each dataset's celestial WCS.`];
  if (opts.survey) {
    lines.push(`Background (context only, not data from your files, never used in a measurement): ${opts.survey.name}, HiPS ${opts.survey.id}, served by ${opts.survey.provider}.`);
    lines.push(`Credit: ${opts.survey.credit}.`);
  }
  return lines;
}

const css = (name: string, fallback: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;

function wrapText(ctx: CanvasRenderingContext2D, text: string, width: number): string[] {
  const out: string[] = [];
  let line = "";
  for (const word of text.split(" ")) {
    const t = line ? `${line} ${word}` : word;
    if (ctx.measureText(t).width > width && line) {
      out.push(line);
      line = word;
    } else line = t;
  }
  return line ? [...out, line] : out;
}

/** The PNG: the plain scene drawn on the canvas, or the survey view Aladin captured (its footprints are already on it) with the caption added. */
export async function renderPng(img: SkyImage, backgroundDataUrl: string | null): Promise<Blob> {
  const probe = document.createElement("canvas").getContext("2d")!;
  probe.font = "16px sans-serif";
  const caption = img.lines.flatMap((l) => wrapText(probe, l, CANVAS - 24));
  const c = document.createElement("canvas");
  c.width = CANVAS;
  c.height = CANVAS + caption.length * CAPTION_LINE + 20;
  const ctx = c.getContext("2d")!;
  ctx.fillStyle = css("--canvas", "#0e141b");
  ctx.fillRect(0, 0, c.width, c.height);
  if (backgroundDataUrl) {
    const im = new Image();
    im.src = backgroundDataUrl;
    await im.decode();
    ctx.drawImage(im, 0, 0, CANVAS, CANVAS);
  } else {
    const k = CANVAS / SIZE;
    ctx.save();
    ctx.scale(k, k);
    ctx.lineJoin = "round";
    ctx.strokeStyle = css("--canvas-text-2", "#aab4c0");
    ctx.lineWidth = 1;
    ctx.globalAlpha = 0.28; // the grid is quiet, as on the screen
    for (const run of img.scene.grid.runs) ctx.stroke(new Path2D(pathData(run, false)));
    ctx.globalAlpha = 1;
    ctx.fillStyle = css("--canvas-text-2", "#aab4c0");
    ctx.font = "9px sans-serif";
    for (const l of img.scene.grid.labels) ctx.fillText(l.text, l.at[0] + 3, l.at[1] - 3);
    for (const f of img.scene.footprints) {
      ctx.strokeStyle = f.selected ? css("--accent", "#27c3e6") : css("--canvas-text-2", "#aab4c0");
      ctx.lineWidth = f.selected ? 3 : 1.25;
      ctx.stroke(new Path2D(pathData(f.path, true)));
    }
    if (img.scene.cursor) {
      const [x, y] = img.scene.cursor;
      ctx.strokeStyle = css("--accent", "#27c3e6");
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.arc(x, y, 5, 0, 2 * Math.PI);
      ctx.moveTo(x - 9, y);
      ctx.lineTo(x + 9, y);
      ctx.moveTo(x, y - 9);
      ctx.lineTo(x, y + 9);
      ctx.stroke();
    }
    ctx.restore();
  }
  ctx.fillStyle = css("--canvas-text", "#e7ecf2");
  ctx.font = "16px sans-serif";
  caption.forEach((l, i) => ctx.fillText(l, 12, CANVAS + 22 + i * CAPTION_LINE));
  return await new Promise<Blob>((res, rej) => c.toBlob((b) => (b ? res(b) : rej(new Error("The image could not be made."))), "image/png"));
}

export async function saveSkyImage(img: SkyImage, backgroundDataUrl: string | null) {
  download(`sky-context${backgroundDataUrl ? "-with-background" : ""}.png`, await renderPng(img, backgroundDataUrl));
}
