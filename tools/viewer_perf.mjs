#!/usr/bin/env node
// Viewer benchmark in a real Chromium: how long a channel step takes from key press to pixels on screen, how long the
// main thread is blocked, and what the CPU plane renderer and histogram cost on a large plane (ADR 0034).
//
//   node tools/viewer_perf.mjs                  # print a table
//   node tools/viewer_perf.mjs --check          # also fail when a budget in tools/viewer_perf_budgets.json is exceeded
//   node tools/viewer_perf.mjs --json out.json  # keep the numbers
//   node tools/viewer_perf.mjs --size 2048x2048x24
//   node tools/viewer_perf.mjs --profile       # also print where the main thread spends its time while stepping channels
//
// Needs: python3 with numpy and astropy, Go, `cd apps/web && npm ci`, `cd e2e && npm ci && npx playwright install chromium`.
// Uses ports 8787 (API) and 5173 (Vite; its proxy is fixed to 8787), so stop `tools/demo.sh` first.
//
// Limits of the numbers: headless Chromium with software rendering, the API and worker on the same machine, planes
// read from the page cache. They measure software overhead on the main thread, not a disk, a network or a GPU.
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const flag = (n) => args.includes(n);
const opt = (n, d) => (args.includes(n) ? args[args.indexOf(n) + 1] : d);
const [NX, NY, NC] = opt("--size", "2048x2048x24").split("x").map(Number);
const STEPS = 12;

const { chromium } = createRequire(join(root, "e2e", "package.json"))("playwright");
const work = mkdtempSync(join(tmpdir(), "vesqira-viewer-perf-"));
mkdirSync(join(work, "data"));
const procs = [];
// each child leads its own process group: `npx vite` starts vite as a grandchild, which a plain kill would leave running
const cleanup = () => { for (const p of procs) { try { process.kill(-p.pid, "SIGTERM"); } catch { /* already gone */ } } rmSync(work, { recursive: true, force: true }); };
process.on("exit", cleanup);
for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => process.exit(130));

function sh(cmd, argv, cwd = root) {
  const r = spawnSync(cmd, argv, { cwd, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`${cmd} ${argv.join(" ")} failed:\n${r.stdout}${r.stderr}`);
}
async function up(url, what) {
  for (let i = 0; i < 100; i++) {
    try { await fetch(url); return; } catch { await new Promise((r) => setTimeout(r, 100)); }
  }
  throw new Error(`${what} did not start (is the port already in use?)`);
}

// A cube with real data in every channel (a sparse file would be served from holes), NaN block, TAN + VRAD WCS.
sh("python3", ["-c", `
import numpy as np
from astropy.io import fits
nx, ny, nc = ${NX}, ${NY}, ${NC}
rng = np.random.default_rng(1)
yy, xx = np.mgrid[0:ny, 0:nx]
base = np.exp(-(((xx - nx / 2) ** 2 + (yy - ny / 2) ** 2) / (2 * (nx / 7.0) ** 2))).astype(np.float32)
d = np.empty((nc, ny, nx), np.float32)
for c in range(nc):
    d[c] = base * (c + 1) * 0.3 + rng.normal(0, 0.2, (ny, nx)).astype(np.float32)
d[:, 100:140, 100:140] = np.nan
h = fits.PrimaryHDU(d).header
h["BUNIT"] = "K"
for i, (ct, cv, cd, cp, cu) in enumerate([("RA---TAN", 150.0, -0.001, nx / 2, "deg"), ("DEC--TAN", 2.0, 0.001, ny / 2, "deg"), ("VRAD", -32000.0, 1000.0, 1.0, "m/s")], 1):
    h[f"CTYPE{i}"], h[f"CRVAL{i}"], h[f"CDELT{i}"], h[f"CRPIX{i}"], h[f"CUNIT{i}"] = ct, cv, cd, cp, cu
fits.PrimaryHDU(d, header=h).writeto(${JSON.stringify(join(work, "data", "perf_cube.fits"))})
`]);
sh("go", ["build", "-buildvcs=false", "-o", join(work, "api"), "."], join(root, "services/api"));
for (const [port, what] of [[8787, "API"], [5173, "Vite"]]) {
  if (await fetch(`http://127.0.0.1:${port}/`).then(() => true, () => false)) throw new Error(`port ${port} is already in use; stop the other ${what} (tools/demo.sh?) and run again`);
}
procs.push(spawn(join(work, "api"), ["--addr", "127.0.0.1:8787", "--db", join(work, "p.db"), "--data-dir", work, "--root", join(work, "data"),
  "--worker", join(root, "services/scientific-worker/fits_header.py"), "--plane-worker", join(root, "services/scientific-worker/plane_worker.py")], { cwd: root, stdio: "ignore", detached: true }));
procs.push(spawn("npx", ["vite", "--port", "5173", "--strictPort", "--host", "127.0.0.1"], { cwd: join(root, "apps/web"), stdio: "ignore", detached: true }));
await up("http://127.0.0.1:8787/api/datasets", "API");
await up("http://127.0.0.1:5173/", "Vite");
const reg = await fetch("http://127.0.0.1:8787/api/datasets", { method: "POST", headers: { "Content-Type": "application/json", Origin: "http://127.0.0.1:8787" }, body: JSON.stringify({ path: join(work, "data", "perf_cube.fits") }) });
if (!reg.ok) throw new Error(`register failed: ${reg.status} ${await reg.text()}`);

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.addInitScript(() => {
  window.__ev = [];
  const put = CanvasRenderingContext2D.prototype.putImageData;
  CanvasRenderingContext2D.prototype.putImageData = function (...a) {
    const t = performance.now(); const r = put.apply(this, a); window.__ev.push(["put", t, performance.now() - t]); return r;
  };
  // the GPU path draws with drawArrays instead of putImageData; either one marks "the plane is drawn"
  const draw = WebGL2RenderingContext.prototype.drawArrays;
  WebGL2RenderingContext.prototype.drawArrays = function (...a) {
    const t = performance.now(); const r = draw.apply(this, a); window.__ev.push(["put", t, performance.now() - t]); return r;
  };
  addEventListener("keydown", (e) => window.__ev.push(["key", performance.now(), e.key]), true);
  new PerformanceObserver((l) => l.getEntries().forEach((e) => window.__ev.push(["long", e.startTime, e.duration]))).observe({ type: "longtask", buffered: true });
});
await page.goto("http://127.0.0.1:5173/");
await page.getByRole("button", { name: /^perf_cube\.fits/ }).click();
await page.locator('canvas[aria-label*="pixels"]').first().waitFor();
await page.waitForTimeout(1500);
const slider = page.getByRole("slider", { name: "Channel" });
await slider.focus();
await page.evaluate(() => (window.__ev.length = 0));
const cdp = flag("--profile") ? await page.context().newCDPSession(page) : null;
if (cdp) { await cdp.send("Profiler.enable"); await cdp.send("Profiler.setSamplingInterval", { interval: 200 }); await cdp.send("Profiler.start"); }
for (let i = 0; i < STEPS; i++) {
  const before = await page.evaluate(() => performance.now());
  await page.keyboard.press("ArrowRight");
  await page.waitForFunction((b) => window.__ev.some((e) => e[0] === "put" && e[1] > b), before, { timeout: 20000 });
  await page.waitForTimeout(400);
}
if (cdp) { // self time per function while stepping channels: where the main thread goes
  const { profile } = await cdp.send("Profiler.stop");
  const self = new Map(), byId = new Map(profile.nodes.map((n) => [n.id, n]));
  profile.samples.forEach((id, i) => { const f = byId.get(id).callFrame; const k = `${f.functionName || "(anonymous)"} ${f.url.split("/").slice(-2).join("/")}:${f.lineNumber + 1}`; self.set(k, (self.get(k) ?? 0) + (profile.timeDeltas[i] ?? 0) / 1000); });
  console.log("self time (ms, all steps):"); [...self].sort((a, b) => b[1] - a[1]).slice(0, 12).forEach(([k, v]) => console.log(`  ${Math.round(v).toString().padStart(6)}  ${k}`));
}
const ev = await page.evaluate(() => window.__ev);
const keys = ev.filter((e) => e[0] === "key"), puts = ev.filter((e) => e[0] === "put"), longs = ev.filter((e) => e[0] === "long");
const lat = [], blocked = [];
keys.forEach((k, i) => {
  const next = keys[i + 1]?.[1] ?? Infinity;
  const p = puts.find((x) => x[1] > k[1] && x[1] < next);
  if (p) lat.push(p[1] + p[2] - k[1]);
  blocked.push(longs.filter((x) => x[1] >= k[1] && x[1] < next).reduce((m, x) => Math.max(m, x[2]), 0));
});
const p95 = (xs) => [...xs].sort((a, b) => a - b)[Math.min(xs.length - 1, Math.ceil(xs.length * 0.95) - 1)];

const cpu = await page.evaluate(async ([w, h]) => {
  const r = await import("/src/viewer/render.ts");
  const d = new Float32Array(w * h);
  for (let i = 0; i < d.length; i++) d[i] = Math.sin(i * 0.001) * 3 + (i % 997 === 0 ? NaN : 0);
  const lut = new Uint8ClampedArray(256 * 3).map((_, i) => i % 256);
  const nodata = [[27, 34, 44], [85, 98, 117]];
  const t = (f, n = 5) => { const xs = []; for (let i = 0; i < n; i++) { const s = performance.now(); f(); xs.push(performance.now() - s); } xs.sort((a, b) => a - b); return xs[Math.floor(n / 2)]; };
  const o = (stretch) => ({ lut, range: [-3, 3], stretch, outW: w, outH: h, nodata });
  return {
    render_linear_ms: t(() => r.renderPlane(d, w, h, o("linear"))),
    render_asinh_ms: t(() => r.renderPlane(d, w, h, o("asinh"))),
    histogram_ms: t(() => r.histogram(d, -3, 3, 256)),
  };
}, [NX, NY]);

const gpu = await page.evaluate(async ([w, h]) => {
  const G = await import("/src/viewer/glRenderer.ts");
  if (!G.gpuSupported()) return {};
  const d = new Float32Array(w * h);
  for (let i = 0; i < d.length; i++) d[i] = Math.sin(i * 0.001) * 3 + (i % 997 === 0 ? NaN : 0);
  const lut = new Uint8ClampedArray(256 * 3).map((_, i) => i % 256);
  const nodata = [[27, 34, 44], [85, 98, 117]];
  const c = document.createElement("canvas");
  const g = G.GlPlaneRenderer.create(c, () => {});
  const gl = c.getContext("webgl2");
  const o = (stretch, range) => ({ lut, range, stretch, outW: w, outH: h, nodata });
  const t = (f, n = 5) => { const xs = []; for (let i = 0; i < n; i++) { const s = performance.now(); f(); gl.finish(); xs.push(performance.now() - s); } xs.sort((a, b) => a - b); return xs[Math.floor(n / 2)]; };
  // a new Float32Array each time, like a new channel: upload plus draw. Then the same plane with a new stretch: draw only.
  const upload = t(() => g.draw(new Float32Array(d), w, h, o("linear", [-3, 3])));
  let k = 0;
  const restyle = t(() => g.draw(d, w, h, o(["linear", "sqrt", "asinh"][k++ % 3], [-3 + k * 0.01, 3])));
  return { gpu_new_plane_ms: upload, gpu_restyle_ms: restyle };
}, [NX, NY]);
await page.evaluate(() => { window.__ev.length = 0; window.__raf = []; const f = (t) => { window.__raf.push(t); requestAnimationFrame(f); }; requestAnimationFrame(f); });
for (let i = 0; i < 20; i++) { await page.keyboard.press("ArrowLeft"); await page.waitForTimeout(40); }
await page.waitForTimeout(1500);
const rapid = await page.evaluate(() => { const r = window.__raf; return Math.max(...r.slice(1).map((t, i) => t - r[i])); });
await browser.close();

const result = {
  size: `${NX}x${NY}x${NC}`,
  key_to_pixels_p95_ms: p95(lat),
  long_task_max_ms: Math.max(...blocked),
  ...cpu,
  ...gpu,
  rapid_scrub_worst_frame_gap_ms: rapid,
};
for (const k of Object.keys(result)) if (typeof result[k] === "number") result[k] = Math.round(result[k]);
console.table(result);
if (opt("--json")) writeFileSync(opt("--json"), JSON.stringify(result, null, 2) + "\n");
if (flag("--check")) {
  const budgets = JSON.parse(readFileSync(join(root, "tools/viewer_perf_budgets.json"), "utf8")).ops;
  const bad = Object.entries(budgets).filter(([k, max]) => !(result[k] <= max));
  for (const [k, max] of bad) console.error(`over budget: ${k} = ${result[k]} (max ${max})`);
  if (bad.length) process.exit(1);
  console.log("within budgets");
}
process.exit(0); // the exit handler stops the API and Vite; without this the running children keep the process alive
