import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

// ADR 0034: the GPU renderer must draw what the CPU renderer draws. "No value" pixels must be identical (they are
// decided from the bits, never from a colormap entry); colormap indices may differ by one at most, because the CPU path
// computes in float64 and the shader in float32, and only on a tiny share of pixels.
const DEV = "http://127.0.0.1:5198/";

type Case = { name: string; w: number; h: number; outW: number; outH: number; stretch: "linear" | "sqrt" | "asinh"; range: [number, number] | null; axial?: number; data: (i: number, w: number, h: number) => number };

type Arg = Omit<Case, "data"> & { dataSrc: string };

const cases: Case[] = [
  { name: "mixed values, one to one", w: 37, h: 23, outW: 37, outH: 23, stretch: "linear", range: [-1, 1], data: (i) => [NaN, Infinity, -Infinity, 0, -0, 0.5, -0.5, 2, -2, 1e-45, 3e38, -3e38][i % 12]! },
  { name: "gradient, upscaled 3x, sqrt", w: 64, h: 48, outW: 192, outH: 144, stretch: "sqrt", range: [0, 1], data: (i, w) => ((i % w) + Math.floor(i / w)) / (w + 47) },
  { name: "gradient, downscaled to a non-integer ratio, asinh", w: 256, h: 256, outW: 101, outH: 77, stretch: "asinh", range: [0.1, 0.9], data: (i, w) => (i % w) / w },
  { name: "constant plane", w: 16, h: 16, outW: 16, outH: 16, stretch: "linear", range: [5, 5], data: () => 5 },
  { name: "all no value", w: 16, h: 16, outW: 32, outH: 32, stretch: "linear", range: [0, 1], data: () => NaN },
  { name: "no range: everything is no value", w: 16, h: 16, outW: 16, outH: 16, stretch: "linear", range: null, data: (i) => i },
  { name: "axial degrees: 0, 180, -10, 170 are the same orientation", w: 8, h: 8, outW: 8, outH: 8, stretch: "linear", range: null, axial: 1, data: (i) => [0, 180, -10, 170, 90, 359.9, NaN, 1e-7][i % 8]! },
  { name: "axial radians", w: 9, h: 7, outW: 18, outH: 14, stretch: "linear", range: null, axial: 180 / Math.PI, data: (i) => (i % 9) * 0.35 - 1 },
];

for (const c of cases) {
  test(`GPU and CPU renderers agree: ${c.name}`, async ({ page }) => {
    await page.goto(DEV);
    const r = await page.evaluate(async (c: Arg) => {
      const render = "/src/viewer/render.ts";
      const gl = "/src/viewer/glRenderer.ts";
      const R = await import(/* @vite-ignore */ render);
      const G = await import(/* @vite-ignore */ gl);
      const data = new Float32Array(c.w * c.h);
      // the function body travels as text because functions do not survive serialisation
      const f = new Function("i", "w", "h", `return (${c.dataSrc})(i, w, h)`) as (i: number, w: number, h: number) => number;
      for (let i = 0; i < data.length; i++) data[i] = f(i, c.w, c.h);
      const lut = new Uint8ClampedArray(256 * 3);
      for (let i = 0; i < 256; i++) lut.set([i, i, i], i * 3); // identity: a pixel value is its colormap index
      const nodata = [[200, 10, 10], [10, 10, 200]];
      const opts = { lut, range: c.range, stretch: c.stretch, outW: c.outW, outH: c.outH, nodata, axial: c.axial ? { toDegrees: c.axial } : undefined };
      const cpu = R.renderPlane(data, c.w, c.h, opts).pixels as Uint8ClampedArray;
      if (!G.gpuSupported()) return { supported: false } as const;
      const canvas = document.createElement("canvas");
      const g = G.GlPlaneRenderer.create(canvas, () => {});
      if (!g) return { supported: false } as const;
      g.draw(data, c.w, c.h, opts);
      const copy = document.createElement("canvas");
      copy.width = c.outW; copy.height = c.outH;
      const ctx = copy.getContext("2d")!;
      ctx.drawImage(canvas, 0, 0);
      const gpu = ctx.getImageData(0, 0, c.outW, c.outH).data;
      let maxDiff = 0, differing = 0, maskMismatch = 0;
      const isNodata = (a: ArrayLike<number>, p: number) => nodata.some((n) => a[p] === n[0] && a[p + 1] === n[1] && a[p + 2] === n[2]);
      for (let p = 0; p < cpu.length; p += 4) {
        const nc = isNodata(cpu, p), ng = isNodata(gpu, p);
        if (nc !== ng) maskMismatch++;
        if (nc && ng) { if (cpu[p] !== gpu[p] || cpu[p + 1] !== gpu[p + 1] || cpu[p + 2] !== gpu[p + 2]) maskMismatch++; continue; }
        const d = Math.max(Math.abs(cpu[p]! - gpu[p]!), Math.abs(cpu[p + 1]! - gpu[p + 1]!), Math.abs(cpu[p + 2]! - gpu[p + 2]!));
        if (d > 0) differing++;
        maxDiff = Math.max(maxDiff, d);
      }
      g.dispose();
      return { supported: true, maxDiff, differing, maskMismatch, pixels: cpu.length / 4 } as const;
    }, (({ data, ...rest }) => ({ ...rest, dataSrc: data.toString() }))(c) as Arg);
    expect(r.supported, "WebGL2 must be available in the test browser").toBe(true);
    if (!r.supported) return;
    expect(r.maskMismatch, "no-value pixels must be identical").toBe(0);
    expect(r.maxDiff).toBeLessThanOrEqual(1);
    expect(r.differing / r.pixels).toBeLessThan(0.001);
  });
}

// The status bar says so when drawing is on the CPU, and says nothing in the normal GPU case.
test("the status bar names the CPU fallback and its reason, and is silent when the GPU draws", async ({ page, request }) => {
  const file = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../.run/data/hi_cube.fits");
  const reg = await request.post("http://127.0.0.1:8787/api/datasets", { data: { path: file } });
  expect(reg.ok()).toBe(true);
  const status = page.getByRole("contentinfo");

  await page.goto(DEV);
  await page.getByRole("button", { name: /^hi_cube\.fits/ }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  await expect(status.getByText(/CPU drawing/)).toHaveCount(0);

  await page.evaluate(() => localStorage.setItem("vesqira.renderer", "cpu"));
  await page.reload();
  await page.getByRole("button", { name: /^hi_cube\.fits/ }).click();
  await expect(page.locator('canvas[aria-label*="pixels"]').first()).toBeVisible();
  await expect(status.getByText("CPU drawing (set by you)")).toBeVisible();
  await page.evaluate(() => localStorage.removeItem("vesqira.renderer"));
});
