# Performance

The viewer must stay responsive on cubes far larger than memory (shape.md section 10, Q149, Q164, Q169, Q182). This page says what is measured, how, and what is not yet.

## What `make bench` measures

`tools/bench.py` builds a sparse FITS cube (default 1500 x 1500 x 600, 5 GiB logical; a few channels hold random data, the rest are holes) and drives the scientific worker over its own stdin/stdout protocol, the way the API does. It reports first-call, median and p95 time and the worker's resident memory for: header read (`view`), a plane, a coordinate readout, a small and a box-sized region spectrum. `--check` compares with `tools/bench_budgets.json`; CI runs it with budgets about five times the development-laptop numbers.

Limits of the numbers: a sparse file in the page cache measures access pattern and software overhead, not a disk or a network. No ssh, no 4D cube, no concurrent requests (contention lives in the Go worker pool, not here).

## What `make viewer-bench` measures

`tools/viewer_perf.mjs` runs the real app in headless Chromium on a 2048 x 2048 x 24 float32 cube (real data in every channel): key press to pixels on screen for 12 channel steps, the longest main-thread task per step, the CPU plane renderer (linear and asinh) and histogram on one plane, and the worst frame gap during 20 quick steps. Budgets are in `tools/viewer_perf_budgets.json`. Software rendering, same-machine API, page-cache reads: it measures main-thread cost, not disk, network or GPU. Not part of CI (needs Chromium and a free port 8787 and 5173). Before the GPU renderer (ADR 0034): key to pixels p95 250 ms, longest task 135 ms, worst frame gap 83 ms. After it and the histogram worker: p95 about 150 to 280 ms, no task over 50 ms, a new 4.2 MP plane drawn in about 20 ms and a stretch or range change in under one frame. The linear and asinh render and histogram rows are the CPU fallback path. `--profile` prints where the main thread spends its time while stepping.

## Reference numbers (development laptop, 2026-11-05)

| operation | before | after |
|---|---|---|
| plane (2.25 MP) | 42 ms | 42 ms (dominated by the percentile statistics) |
| spectrum, 10 x 10 px, 600 channels | 49 ms | 4 ms |
| spectrum, 187 x 187 px, 600 channels | 791 ms | 150 ms |

The gain comes from slicing the memory-mapped file instead of astropy's section reader and from reading channels in blocks with one vectorised mean per block. Files that cannot be mapped still fall back to the section reader.

## Not yet done

A browser-side plane cache (the server cache is on loopback, and a browser copy would need its own staleness rule), and measurement over real disks, ssh, 4D cubes and concurrent load. See `docs/roadmap.md` stage P.

## Preview and prefetch

On a remote host, a plane larger than about a quarter of a megapixel is first requested reduced (`step=N`, at most 64; the worker returns the mean of the finite pixels of each N x N block, NaN where a block has none, and marks `preview` in the header as "not for measurement"). The viewer draws it on the full grid, so pixel indices, readouts and regions keep their meaning, shows "preview, reduced N×" under the picture, and requests the full plane 150 ms after the last channel change. When the full plane arrives, the next and previous channels are requested so the server cache already holds them. Readouts of coordinates always come from the full data (`coord`), never from the preview. Local datasets skip the preview: the reduction still reads the whole plane, so it only helps when the link is the bottleneck.

## Workload classes

Scientific work has three classes (ADR 0055), each with its own concurrency budget and its own worker processes, so capacity reserved for interactive viewing is never taken by heavier work:

| Class | Work | Budget flag (default) |
| --- | --- | --- |
| interactive | planes, coordinate readouts, overlay geometry, small readouts | `--workers-interactive` (2 worker processes) |
| heavy | spectra over large regions, orientation statistics, PV diagrams, channel grids, catalog and ROHSA index reads, and analysis programs (moment, cutout, convolve, relative orientation, ...) | `--workers-heavy` (2 tasks at once) |
| background | header reads that catalog files, fingerprints and full file hashes (a project scan queues up to a thousand) | `--workers-background` (1) |

Each budget is 1 to 16. With the defaults at most 5 Python worker processes exist, and analysis programs run inside the heavy budget. Interactive capacity is reserved by construction: there is no shared pool a heavy task could take a slot from. A request that exceeds the timeout, or any broken reply, kills only that process; a heavy or background request whose caller gives up is stopped by killing only its own process (about 0.3 s cold start next time), an interactive one is allowed to finish. Transfers (rsync) and remote host workers are outside these budgets (ADR 0051).

When a request waits for a slot the API knows why: `GET /api/workload?request=<id>` (the id the client sent as `X-Request-Id`) answers `waiting` with a reason such as "waiting: 2 heavy analyses are running (relative orientation, PV diagram); interactive viewing is unaffected". The panel that asked polls it after 700 ms and shows the sentence in place of a silent spinner. The reason names operations only, never files or datasets. Without an id the route lists budget, running and waiting counts per class.

Covered by `TestInteractiveRequestIsNotDelayedByAFloodOfHeavyAndBackgroundWork` (a flood of heavy and background requests against fake workers that take 300 ms each; a plane request must finish within 150 ms), `TestQueueReasonNamesWhatIsRunning`, `TestBudgetLimitsConcurrencyPerClass`, `TestBudgetsAreValidated`, `TestAHeavyRequestNeverBlocksInteractiveOnesAndCancelKillsOnlyTheHeavyLane` and `TestHeaderUsesItsOwnLaneAndMapsErrors`.

`make bench` has a `plane_under_load` row: planes on one worker while a second worker serves large-region spectra and a third header reads, back to back. Budget p95 450 ms (`tools/bench_budgets.json`); measured on a loaded laptop at about 130 to 200 ms against about 50 to 100 ms for an unloaded plane. It catches contention the separate processes do not isolate (CPU, memory bandwidth, page cache), not a queue. On a machine busy with other work the unloaded `spectrum_small` first call can already exceed its own budget; that is independent of the load case.

Registering a file used to start one `fits_header.py` process per file (median 188 ms on the development laptop); the registration lane keeps one worker, and `tools/bench.py` reports the `register` row (300 small files per request, budget p95 50 ms). The reply is the same JSON, checked by `TestPersistentHeaderEqualsOneShotScript`; a crash on one file fails only that file (`TestHeaderCrashMidBatchFailsOnlyThatFile`).

## Memory cap

The plane worker and each analysis run (`analysis.py`) set, through the shared `memcap.py`, `RLIMIT_DATA` to what it holds once numpy and astropy are loaded (about 2 GB of address space) plus `VESQIRA_WORKER_MEM_MB` (default 3072; 0 disables). Files are opened read-only and memory-mapped, which the limit does not count, so a 10 GB cube opens as before; an allocation over the cap (for example a 3000 x 3000 plane with a 40 MB budget) makes that request fail with "over the memory cap (N MB)" and the worker serves the next one. Linux only; elsewhere nothing is limited. Covered by `test_analysis.py` and `test_a_request_over_the_memory_cap_fails_alone_and_the_worker_keeps_serving`.

A reduced preview is a display aid only: while it is shown the pixel readout says "preview only", the histogram and the compared pixel counts are hidden, and a figure cannot be saved. Orientation maps (declared axial) are never previewed, since a block mean of angles is wrong; the API refuses it. If the preview fails after the full plane arrived, or succeeds after the full plane failed, it changes nothing.

Files read through the memory map must be as long as their header says; a shorter file (copied part way, or still being written) is read through astropy's section reader, which serves the planes that exist.
