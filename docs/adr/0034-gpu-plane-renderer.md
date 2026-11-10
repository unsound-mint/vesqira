# ADR 0034: GPU plane renderer

- Status: accepted (implemented: renderer, histogram worker, equivalence test, benchmark; tiles not started)
- Date: 2026-11-10

## Context
The viewer builds every displayed plane in JavaScript: `viewer/render.ts` loops over each output pixel (stretch, lookup table, no-value stripes) and `putImageData` copies the result. `Histogram` also loops over the whole plane on the main thread. Changing channel, colormap, stretch or display limits repeats all of it.

Measured in headless Chromium 1440x900, software rendering, one local machine, 2048 x 2048 float32 planes from a 400 MB, 24-channel cube (API and worker local, planes cached or read from page cache; the harness is a throwaway script, not committed yet):

| measurement | result |
|---|---|
| ArrowRight to pixels on screen, 12 steps | 213 to 243 ms |
| plane response (16.8 MB, over loopback) | 36 to 52 ms |
| main-thread long task per step | 105 to 127 ms |
| `renderPlane`, linear, 4.2 MP | 57 ms |
| `renderPlane`, asinh | 127 ms |
| `renderPlane`, downscaled to 1000 x 1000 | 14 ms |
| `histogram`, 256 bins, 4.2 MP | 42 ms |
| `putImageData`, `ImageData` construction | under 1 ms |
| 20 quick steps, 40 ms apart | 1 frame drawn, one 103 ms stall, worst frame gap 100 ms |

So a plane change blocks the main thread for about 110 ms (render plus histogram), which is above the 50 ms long-task threshold and well above a 16 ms frame, and a stretch change on a large plane costs 57 to 127 ms while the data has not changed. Not measured: cold reads from a real disk or ssh, other browsers, devices with weaker CPUs, GPU memory limits. The 4.2 MP size is the typical large plane; the worker supports 16.7 MP, four times worse.

## Decision
Render planes with **WebGL2** and keep the existing semantics exactly.

- The float32 plane is uploaded once, as raw 32-bit words in an `R32UI` texture (the same buffer viewed as `Uint32Array`, no conversion). Stretch (linear, sqrt, asinh), display range and colormap lookup run in a fragment shader; the colormap is a 256-entry 1-D texture. Changing stretch, range or colormap updates uniforms and redraws; it does not touch pixels on the CPU or re-upload data.
- **No-value pixels are decided in the shader from the exponent bits (`(bits & 0x7f800000) == 0x7f800000`), never from a colormap entry and never with `isnan`**, which a driver may optimise away. This also needs no float-texture support. NaN and infinities draw the diagonal stripe pattern in canvas-pixel space at any zoom, exactly as today, with the same two token colors. Zero is an ordinary value. Sampling is nearest-neighbour (no filtering), so a displayed value is never blended with a neighbour or with "no value".
- **Axial orientation maps** keep their cyclic treatment: the shader reduces the angle modulo 180 degrees (from degrees or radians) and uses the cyclic map, as `axialDegrees` does now. The same angle table is the CPU test oracle.
- **Float precision:** the bits are the float32 plane exactly. Limits and the normalized value are computed in the shader at `highp` float32 where the CPU path uses float64, so a colormap index can differ by one on a very small share of pixels (checked below).
- **Fallback:** if WebGL2 is unavailable, context creation or the shader fails, the context is lost, or a plane or its canvas exceeds the device's texture or viewport limits, use the current CPU renderer unchanged on a fresh canvas (a canvas keeps the kind of context it first gave out). A stored choice `vesqira.renderer` = `cpu` forces it. The CPU renderer is not deleted: it is the equivalence oracle in tests and the fallback in production. Telling the user in the status line which renderer is active is not done yet.
- **Histogram:** computed in a Web Worker (`useHistogram`), 60 ms after the last change so scrubbing counts only where it stops. The plane is copied to the worker (a 16.8 MB structured clone costs about 12 ms of main thread per count; sending it without a copy needs `SharedArrayBuffer`, hence cross-origin isolation, which is not worth it yet). Counting it on the server with the plane statistics is the alternative if that copy ever matters. The plot stays empty while counting: it never shows another channel's counts. Without `Worker` (tests) it counts on the calling thread.
- **Figures and export** must keep producing the same pixels. Saved figures and print render through the CPU path (or read back from the GL canvas at the figure size), and a golden test compares them.
- Resource limits: one texture per visible plane; compared panels share one context where possible, textures are deleted when a plane is replaced, and a plane over the device's `MAX_TEXTURE_SIZE` uses the CPU path until tiling exists (ADR to follow).
- Not in this ADR: tiles and a multi-resolution pyramid for zoom and pan on large planes, worker pooling, WebGPU. They are separate decisions that can use this renderer.

## Alternatives
- **OffscreenCanvas + Web Worker running the current loop:** removes the main-thread stall with the smallest change and no new failure modes, but still costs 57 to 127 ms of CPU per change and four times that at 16.7 MP, and a stretch drag stays laggy. A reasonable first step if WebGL2 hits a blocker, and it composes with the GPU path (the histogram needs a worker either way).
- **WebGPU:** the better long-term target for compute (histograms, reductions) but support, driver quirks and Linux availability are not yet dependable for a research tool that must run on laptops; revisit behind the same interface.
- **Use a library (regl, three.js, deck.gl):** heavier than a single textured quad and hides the exact semantics we must control (no-value, axial, nearest sampling). A small hand-written renderer is easier to verify against the CPU oracle.
- **Do nothing:** acceptable on small cubes; the numbers above are the cost on large ones.

## Consequences
A second rendering implementation to keep equivalent to the CPU one, with GL context loss and driver differences as new failure modes. Better latency and lower CPU on every plane change, an instant display-limit drag, a path to larger planes and later to tiles. Pixel readouts, regions and coordinates are unchanged because they use the data, not the picture. Layout code that reads canvas size or `getContext("2d")` (`PlaneViewer`, figure and review anchors) is touched.

## Result

Same cube and browser as the table above, GPU renderer and histogram worker on, three runs of `make viewer-bench`: key to pixels p95 149 to 277 ms (before: 250), longest main-thread task 0 to 129 ms in the first run before the histogram moved and 0 in the next two (before: 135; a task over 50 ms counts), `gpu_new_plane` (upload and draw of 4.2 MP) 14 to 29 ms, a stretch or range change 0 ms, worst frame gap while stepping quickly 50 to 100 ms (before: 83 to 100; not improved in any clear way, so something else still holds a frame for about 80 ms and is not yet identified). A CPU profile while stepping showed the histogram as the largest remaining main-thread cost before the worker, and `renderPlane` absent after the renderer. The run-to-run spread is large; the budgets in `tools/viewer_perf_budgets.json` are about three times the numbers. Dev-mode React (`jsxDEV`) was the second-largest item in the profile; the measurement runs the Vite dev server, so a production build may do better.

## Validation
- **Equivalence** (`e2e/tests/renderer.spec.ts`, real Chromium with WebGL2; breaking the stripe period in the shader makes it fail): a test that renders the same planes through both paths and compares the output, with an agreed tolerance for the colormap quantization (the CPU path rounds to 256 steps) and exact equality of which pixels are no-value. Cases: NaN, BLANK, infinities, zero, constant plane, all-NaN plane, a value just outside the range, float32 extremes, axial angles at 0, 180, -10, 170 degrees and in radians, every stretch.
- **Visual:** screenshots of both paths side by side across the fixtures and colormaps, light and dark, 1440x900, 1280x800, 390x844.
- **Performance:** extend the profiling script to a committed tool (`tools/`), with budgets in a bench file like `tools/bench_budgets.json`: key-to-pixels p95, long-task duration, rapid-scrub frame gaps, stretch change cost, peak GPU/JS memory. Targets proposed from the table above: no long task over 50 ms on a plane change at 4.2 MP, stretch or range change within one frame, key-to-pixels dominated by the network.
- **Failure paths:** context loss and restore, WebGL2 disabled, a texture over the size limit, a plane replaced during upload, a channel changed mid-load. The fallback is exercised in an end-to-end test.
- Existing viewer, figure, compare and review tests and the Playwright walk-through still pass, plus an axe scan.

## Rollback / Migration
Behind one function that returns a drawn canvas, selected by a setting and by capability detection. Turning the GPU path off restores today's behavior with no data or API change. Stored figures and previews are not affected.
